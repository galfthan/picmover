package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

const (
	cookieName   = "__Host-picmover"
	cookieMaxAge = 400 * 24 * 3600 // 34560000 s, Chrome's cap
	maxBody      = 256 << 10
)

// device is a phone browser that paired with the probe. It is kept in
// devices.json so the "reopen the browser" step works across probe
// restarts.
type device struct {
	ID        string    `json:"id"`
	SecretSHA string    `json:"secretSha256"`
	Browser   string    `json:"browser"`
	UA        string    `json:"ua"`
	PairedAt  time.Time `json:"pairedAt"`
	LastSeen  time.Time `json:"lastSeen"`
	Loads     int       `json:"loads"`
	LocalSeen time.Time `json:"localSeen,omitzero"` // a page load on the .local name reported this device
}

// mediaObs counts what the server saw for one kind of media request in
// one page run.
type mediaObs struct {
	Requests    int      `json:"requests"`
	WithCookie  int      `json:"withCookie"`
	NoCookie    int      `json:"noCookie"`
	Ranges      int      `json:"ranges"`
	RangeCookie int      `json:"rangesWithCookie"`
	NotModified int      `json:"notModified"`
	Statuses    []int    `json:"statuses,omitempty"`
	RangeHdrs   []string `json:"rangeHeaders,omitempty"` // the first few
}

// probe is the server state.
type probe struct {
	dir      string
	token    string // pairing token (URL fragment)
	code     string // typed fallback
	urls     pageURLs
	clips    []clip
	extraDir string
	verbose  bool
	out      io.Writer
	now      func() time.Time

	caDER, caPEM                []byte
	imgPNG, thumbJPEG, origJPEG []byte

	mu       sync.Mutex
	devices  map[string]*device
	obs      map[string]map[string]*mediaObs // run id → kind → counts
	cache    map[string]int                  // cache key → full responses (200)
	cache304 map[string]int                  // cache key → 304 responses
	runOrder []string
	resultsF *os.File
	mediaF   *os.File
}

// pageURLs are the absolute URLs the page links to.
type pageURLs struct {
	IP      string `json:"ip"`      // https://192.168.1.20:7443
	Local   string `json:"local"`   // https://host.local:7443, or ""
	Outside string `json:"outside"` // https://192.168.1.20:7444 (constraint test)
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// randCode is an 8-character code from the design's alphabet.
func randCode() string {
	const alpha = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func normCode(s string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
}

func newProbe(dir string, certs *certSet, urls pageURLs, clips []clip, extraDir string, out io.Writer) (*probe, error) {
	p := &probe{
		caDER: certs.CA.Raw, caPEM: certs.caPEM(),
		dir: dir, token: randToken(32), code: randCode(), urls: urls, clips: clips, extraDir: extraDir,
		out: out, now: time.Now,
		devices: map[string]*device{}, obs: map[string]map[string]*mediaObs{},
		cache: map[string]int{}, cache304: map[string]int{},
		imgPNG:    encodePNG(testImage(200, 120, 40)),
		thumbJPEG: encodeJPEG(testImage(256, 192, 140)),
		origJPEG:  encodeJPEG(testImage(1600, 1200, 220)),
	}
	if b, err := os.ReadFile(filepath.Join(dir, "devices.json")); err == nil {
		var ds []*device
		if err := json.Unmarshal(b, &ds); err == nil {
			for _, d := range ds {
				p.devices[d.ID] = d
			}
		}
	}
	var err error
	if p.resultsF, err = os.OpenFile(p.resultsPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
		return nil, err
	}
	if p.mediaF, err = os.OpenFile(filepath.Join(dir, "media.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *probe) resultsPath() string { return filepath.Join(p.dir, "results.jsonl") }

func (p *probe) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saveDevicesLocked()
	p.resultsF.Close()
	p.mediaF.Close()
}

func (p *probe) saveDevicesLocked() {
	ds := make([]*device, 0, len(p.devices))
	for _, d := range p.devices {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].PairedAt.Before(ds[j].PairedAt) })
	b, _ := json.MarshalIndent(ds, "", "  ")
	if err := writeAtomic(filepath.Join(p.dir, "devices.json"), b, 0o600); err != nil {
		fmt.Fprintln(p.out, "saving devices.json:", err)
	}
}

// deviceOf returns the device named by the request's cookie, or nil.
func (p *probe) deviceOf(r *http.Request) *device {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	id, secret, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.devices[id]
	if d == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(d.SecretSHA)) != 1 {
		return nil
	}
	d.LastSeen = p.now()
	return d
}

// browserLabel is a coarse label from a User-Agent, for the terminal.
func browserLabel(ua string) string {
	switch {
	case strings.Contains(ua, "Sailfish"):
		return "Sailfish Browser"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		if strings.Contains(ua, "CriOS") {
			return "Chrome on iOS"
		}
		if strings.Contains(ua, "FxiOS") {
			return "Firefox on iOS"
		}
		return "Safari on iOS"
	case strings.Contains(ua, "Android"):
		if strings.Contains(ua, "Firefox") {
			return "Firefox on Android"
		}
		if strings.Contains(ua, "SamsungBrowser") {
			return "Samsung Internet"
		}
		return "Chrome on Android"
	case strings.Contains(ua, "HeadlessChrome"):
		return "Headless Chrome"
	case strings.Contains(ua, "Firefox"):
		return "Firefox"
	case strings.Contains(ua, "Chrome"):
		return "Chrome"
	case strings.Contains(ua, "curl"):
		return "curl"
	}
	if len(ua) > 40 {
		return ua[:40]
	}
	return ua
}

func (p *probe) handler() http.Handler {
	mux := http.NewServeMux()
	for _, pat := range []string{"GET /{$}", "GET /pair", "GET /check", "GET /local", "GET /constraint"} {
		mux.HandleFunc(pat, p.page)
	}
	static, _ := fs.Sub(staticFS, "static")
	fileSrv := http.FileServer(http.FS(static))
	mux.Handle("GET /static/", http.StripPrefix("/static", noCache(fileSrv)))
	mux.HandleFunc("GET /ca.crt", p.serveCADER)
	mux.HandleFunc("GET /ca.pem", p.serveCAPEM)
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		// The page fetches this from its other origins (.local, the
		// constraint port) with mode: no-cors; same-origin CORP would
		// block that regardless of the certificate.
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/pair", p.pair)
	mux.HandleFunc("GET /api/whoami", p.whoami)
	mux.HandleFunc("GET /api/obs", p.getObs)
	mux.HandleFunc("GET /api/cache", p.getCache)
	mux.HandleFunc("POST /api/results", p.results)
	mux.HandleFunc("GET /m/img.png", p.media("img", "image/png", func() []byte { return p.imgPNG }))
	mux.HandleFunc("GET /m/original.jpg", p.media("download", "image/jpeg", func() []byte { return p.origJPEG }))
	mux.HandleFunc("GET /m/thumb.jpg", p.thumb)
	mux.HandleFunc("GET /m/clip/{name...}", p.clip)
	return headers(mux)
}

// headers sets the planned production response headers (minus HSTS,
// which the design rules out).
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cross-Origin-Resource-Policy", "same-origin")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.ServeHTTP(w, r)
	})
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// page serves the test page with this load's server-side facts.
func (p *probe) page(w http.ResponseWriter, r *http.Request) {
	d := p.deviceOf(r)
	_, hadCookie := r.Cookie(cookieName)
	devID := ""
	if d != nil {
		p.mu.Lock()
		d.Loads++
		devID = d.ID
		p.mu.Unlock()
	}
	cfg := map[string]any{
		"path":         r.URL.Path,
		"host":         r.Host,
		"cookieSent":   hadCookie == nil,
		"device":       devID,
		"serverTime":   p.now().UTC().Format(time.RFC3339),
		"secFetchSite": r.Header.Get("Sec-Fetch-Site"),
		"secFetchMode": r.Header.Get("Sec-Fetch-Mode"),
		"secFetchDest": r.Header.Get("Sec-Fetch-Dest"),
		"secFetchUser": r.Header.Get("Sec-Fetch-User"),
		"urls":         p.urls,
		"clips":        append(append([]clip{}, p.clips...), extraClips(p.extraDir)...),
		"proto":        r.Proto,
		"tls":          r.TLS != nil,
	}
	if d != nil {
		p.mu.Lock()
		cfg["pairedAt"] = d.PairedAt.UTC().Format(time.RFC3339)
		cfg["loads"] = d.Loads
		p.mu.Unlock()
	}
	b, _ := json.Marshal(cfg) // escapes <, > and & so it cannot end the element
	html, _ := staticFS.ReadFile("static/index.html")
	html = bytes.Replace(html, []byte("{/*CONFIG*/}"), b, 1)
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	connect := "'self'"
	for _, u := range []string{p.urls.Local, p.urls.Outside} {
		if u != "" {
			connect += " " + u
		}
	}
	hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src "+connect+
		"; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	_, _ = w.Write(html)
	if p.verbose {
		fmt.Fprintf(p.out, "%s page %s %s cookie=%v device=%q sec-fetch-site=%q %s\n",
			p.now().Format("15:04:05"), r.Host, r.URL.Path, hadCookie == nil, devID, r.Header.Get("Sec-Fetch-Site"), browserLabel(r.UserAgent()))
	}
}

func (p *probe) serveCADER(w http.ResponseWriter, r *http.Request) {
	// application/x-x509-ca-cert: iOS Safari offers a configuration
	// profile, Firefox/Gecko its CA import dialog, Android Chrome a
	// download that Settings → Install a certificate accepts.
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(p.caDER)
}

func (p *probe) serveCAPEM(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="picmover-shareprobe-ca.pem"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(p.caPEM)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("X-Picmover") != "1" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "needs Content-Type: application/json and X-Picmover: 1"})
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return false
	}
	return true
}

// pair exchanges the fragment token (or the typed code) for the device
// cookie. Unlike the product, the probe's token is reusable for the run,
// so several phones can use one QR code.
func (p *probe) pair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	via := ""
	switch {
	case req.Token != "" && subtle.ConstantTimeCompare([]byte(req.Token), []byte(p.token)) == 1:
		via = "token"
	case req.Code != "" && normCode(req.Code) == normCode(p.code):
		via = "code"
	default:
		writeJSON(w, http.StatusGone, map[string]string{"error": "wrong or old pairing token/code; use the one printed by the running probe"})
		return
	}
	secret := randToken(32)
	sum := sha256.Sum256([]byte(secret))
	idb := make([]byte, 8)
	_, _ = rand.Read(idb)
	d := &device{ID: "d_" + hex.EncodeToString(idb), SecretSHA: hex.EncodeToString(sum[:]),
		Browser: browserLabel(r.UserAgent()), UA: r.UserAgent(), PairedAt: p.now(), LastSeen: p.now()}
	p.mu.Lock()
	p.devices[d.ID] = d
	p.saveDevicesLocked()
	p.mu.Unlock()
	w.Header().Add("Set-Cookie", cookieName+"="+d.ID+"."+secret+"; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age="+strconv.Itoa(cookieMaxAge))
	fmt.Fprintf(p.out, "%s paired %s (%s) from %s via %s\n", p.now().Format("15:04:05"), d.ID, d.Browser, remoteIP(r), via)
	writeJSON(w, http.StatusOK, map[string]string{"device": d.ID, "via": via})
}

func remoteIP(r *http.Request) string {
	if i := strings.LastIndexByte(r.RemoteAddr, ':'); i > 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}

func (p *probe) whoami(w http.ResponseWriter, r *http.Request) {
	_, hadCookie := r.Cookie(cookieName)
	d := p.deviceOf(r)
	res := map[string]any{"cookieSent": hadCookie == nil, "device": nil}
	if d != nil {
		p.mu.Lock()
		res["device"] = d.ID
		res["pairedAt"] = d.PairedAt.UTC().Format(time.RFC3339)
		if !d.LocalSeen.IsZero() {
			res["localSeen"] = d.LocalSeen.UTC().Format(time.RFC3339)
		}
		p.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, res)
}

var runRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// observe records one media request.
func (p *probe) observe(r *http.Request, kind string, status int) {
	run := r.URL.Query().Get("run")
	_, err := r.Cookie(cookieName)
	cookie := err == nil
	rng := r.Header.Get("Range")
	line, _ := json.Marshal(map[string]any{
		"time": p.now().UTC().Format(time.RFC3339Nano), "remote": remoteIP(r), "path": r.URL.Path,
		"run": run, "kind": kind, "cookie": cookie, "range": rng, "status": status,
		"secFetchDest": r.Header.Get("Sec-Fetch-Dest"), "browser": browserLabel(r.UserAgent()),
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.mediaF.Write(append(line, '\n'))
	if p.verbose {
		fmt.Fprintf(p.out, "%s media %-20s run=%s cookie=%v range=%q → %d\n", p.now().Format("15:04:05"), kind, run, cookie, rng, status)
	}
	if !runRE.MatchString(run) {
		return
	}
	m := p.obs[run]
	if m == nil {
		m = map[string]*mediaObs{}
		p.obs[run] = m
		p.runOrder = append(p.runOrder, run)
		if len(p.runOrder) > 500 { // bound memory
			delete(p.obs, p.runOrder[0])
			p.runOrder = p.runOrder[1:]
		}
	}
	o := m[kind]
	if o == nil {
		o = &mediaObs{}
		m[kind] = o
	}
	o.Requests++
	if cookie {
		o.WithCookie++
	} else {
		o.NoCookie++
	}
	if rng != "" {
		o.Ranges++
		if cookie {
			o.RangeCookie++
		}
		if len(o.RangeHdrs) < 6 {
			o.RangeHdrs = append(o.RangeHdrs, rng)
		}
	}
	if status == http.StatusNotModified {
		o.NotModified++
	}
	if len(o.Statuses) < 12 {
		o.Statuses = append(o.Statuses, status)
	}
}

// statusWriter remembers the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (p *probe) media(kind, ctype string, data func() []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		sw.Header().Set("Content-Type", ctype)
		sw.Header().Set("Cache-Control", "private, no-store")
		if kind == "download" && r.URL.Query().Get("download") == "1" {
			sw.Header().Set("Content-Disposition", `attachment; filename="shareprobe-original.jpg"`)
		}
		http.ServeContent(sw, r, "", time.Time{}, bytes.NewReader(data()))
		p.observe(r, kind, sw.status)
	}
}

// thumb is the HTTP cache test: private, max-age=3600 with an ETag, as
// renditions will be served (with a shorter max-age). Hits are counted per
// "key" so the page can tell whether its second fetch reached the server.
func (p *probe) thumb(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	const etag = `"shareprobe-thumb-1"`
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "image/jpeg")
	status := http.StatusOK
	if r.Header.Get("If-None-Match") == etag {
		status = http.StatusNotModified
	}
	if runRE.MatchString(key) {
		p.mu.Lock()
		if status == http.StatusOK {
			p.cache[key]++
		} else {
			p.cache304[key]++
		}
		p.mu.Unlock()
	}
	p.observe(r, "thumb", status)
	if status == http.StatusNotModified {
		w.WriteHeader(status)
		return
	}
	_, _ = w.Write(p.thumbJPEG)
}

func (p *probe) getCache(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	p.mu.Lock()
	full, revalidated := p.cache[key], p.cache304[key]
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"full": full, "notModified": revalidated})
}

func (p *probe) clip(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var file string
	if rest, ok := strings.CutPrefix(name, "x/"); ok && p.extraDir != "" {
		if rest != path.Base(rest) || strings.HasPrefix(rest, ".") {
			http.NotFound(w, r)
			return
		}
		file = filepath.Join(p.extraDir, rest)
	} else {
		for _, c := range p.clips {
			if c.Name == name {
				file = filepath.Join(p.dir, "clips", c.Name)
			}
		}
	}
	f, err := os.Open(file)
	if file == "" || err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	sw.Header().Set("Content-Type", mimeOf(name))
	sw.Header().Set("Cache-Control", "private, no-store")
	if r.URL.Query().Get("download") == "1" {
		sw.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(name)+`"`)
	}
	http.ServeContent(sw, r, "", fi.ModTime(), f)
	p.observe(r, "clip:"+name, sw.status)
}

func (p *probe) getObs(w http.ResponseWriter, r *http.Request) {
	run := r.URL.Query().Get("run")
	p.mu.Lock()
	m := p.obs[run]
	out := map[string]mediaObs{}
	for k, v := range m {
		out[k] = *v
	}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// results appends the page's report to results.jsonl and prints a
// one-line summary.
func (p *probe) results(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !readJSON(w, r, &body) {
		return
	}
	_, hadCookie := r.Cookie(cookieName)
	d := p.deviceOf(r)
	devID, _ := body["device"].(string)
	if d != nil {
		devID = d.ID
	}
	host := r.Host
	// A load on the .local name has no cookie (another host); the page
	// passes its device id in the URL so it can be credited.
	if strings.Contains(strings.Split(host, ":")[0], ".local") {
		if claimed, _ := body["device"].(string); claimed != "" {
			p.mu.Lock()
			if cd := p.devices[claimed]; cd != nil {
				cd.LocalSeen = p.now()
				p.saveDevicesLocked()
			}
			p.mu.Unlock()
		}
	}
	rec := map[string]any{
		"time":       p.now().UTC().Format(time.RFC3339),
		"remote":     remoteIP(r),
		"host":       host,
		"userAgent":  r.UserAgent(),
		"browser":    browserLabel(r.UserAgent()),
		"cookieSent": hadCookie == nil,
		"device":     devID,
		"report":     body,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p.mu.Lock()
	_, err = p.resultsF.Write(append(line, '\n'))
	p.mu.Unlock()
	if err != nil {
		log.Printf("writing results: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write results"})
		return
	}
	fmt.Fprintln(p.out, summaryLine(rec))
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

// summaryLine is "15:04:05 192.168.1.34 Safari on iOS d_… [phase] 1:PASS 2:FAIL …".
func summaryLine(rec map[string]any) string {
	report, _ := rec["report"].(map[string]any)
	items, _ := report["items"].(map[string]any)
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.SplitN(keys[i], ".", 2)[0])
		b, _ := strconv.Atoi(strings.SplitN(keys[j], ".", 2)[0])
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	var b strings.Builder
	t, _ := time.Parse(time.RFC3339, fmt.Sprint(rec["time"]))
	fmt.Fprintf(&b, "%s %s %s %v", t.Local().Format("15:04:05"), rec["remote"], rec["browser"], rec["device"])
	if ph, ok := report["phase"].(string); ok && ph != "" {
		fmt.Fprintf(&b, " [%s]", ph)
	}
	for _, k := range keys {
		st := "?"
		if m, ok := items[k].(map[string]any); ok {
			st = fmt.Sprint(m["status"])
		}
		fmt.Fprintf(&b, " %s:%s", strings.SplitN(k, ".", 2)[0], st)
	}
	return b.String()
}
