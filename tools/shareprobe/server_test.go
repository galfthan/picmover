package main

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestProbe(t *testing.T) (*probe, *bytes.Buffer, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	cs, _, err := loadOrCreateCerts(dir, net.IPv4(192, 168, 1, 20), "terra", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// One small "clip" (any bytes do for Range and cookie checks).
	if err := os.MkdirAll(filepath.Join(dir, "clips"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clips", "h264-aac.mp4"), bytes.Repeat([]byte("0123456789"), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	urls := pageURLs{IP: "https://192.168.1.20:7443", Local: "https://terra.local:7443", Outside: "https://192.168.1.20:7444"}
	p, err := newProbe(dir, cs, urls, clips[:1], "", &out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, &out, p.handler()
}

func do(h http.Handler, method, target string, body any, cookie *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "https://192.168.1.20:7443"+target, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Picmover", "1")
	}
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func pairCookie(t *testing.T, h http.Handler, body any) *http.Cookie {
	t.Helper()
	w := do(h, "POST", "/api/pair", body, nil, nil)
	if w.Code != 200 {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	sc := w.Header().Values("Set-Cookie")
	if len(sc) != 1 {
		t.Fatalf("Set-Cookie %v", sc)
	}
	val, attrs, _ := strings.Cut(sc[0], ";")
	for _, want := range []string{"Path=/", "Secure", "HttpOnly", "SameSite=Strict", "Max-Age=34560000"} {
		if !strings.Contains(attrs, " "+want) {
			t.Errorf("cookie lacks %s: %s", want, sc[0])
		}
	}
	if strings.Contains(attrs, "Domain") {
		t.Errorf("__Host- cookie has a Domain: %s", sc[0])
	}
	name, v, _ := strings.Cut(val, "=")
	if name != "__Host-picmover" {
		t.Fatalf("cookie name %q", name)
	}
	return &http.Cookie{Name: name, Value: v}
}

func TestPairing(t *testing.T) {
	p, out, h := newTestProbe(t)

	if w := do(h, "POST", "/api/pair", map[string]string{"token": "wrong"}, nil, nil); w.Code != http.StatusGone {
		t.Errorf("wrong token: %d", w.Code)
	}
	// Without X-Picmover the POST is refused, as in the product.
	r := httptest.NewRequest("POST", "/api/pair", strings.NewReader(`{"token":"`+p.token+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST without X-Picmover: %d", w.Code)
	}

	c := pairCookie(t, h, map[string]string{"token": p.token})
	if !strings.Contains(out.String(), "paired d_") || !strings.Contains(out.String(), "Safari on iOS") {
		t.Errorf("terminal output %q", out.String())
	}
	var who map[string]any
	w = do(h, "GET", "/api/whoami", nil, c, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &who)
	if who["cookieSent"] != true || who["device"] == nil {
		t.Errorf("whoami with cookie: %v", who)
	}
	// A forged secret is not a device.
	bad := &http.Cookie{Name: c.Name, Value: strings.Split(c.Value, ".")[0] + ".forged"}
	w = do(h, "GET", "/api/whoami", nil, bad, nil)
	who = nil
	_ = json.Unmarshal(w.Body.Bytes(), &who)
	if who["device"] != nil {
		t.Errorf("forged cookie accepted: %v", who)
	}
	// The typed code works too, with or without the dash.
	pairCookie(t, h, map[string]string{"code": strings.ToLower(strings.ReplaceAll(p.code, "-", ""))})

	// Devices survive a restart of the probe.
	b, err := os.ReadFile(filepath.Join(p.dir, "devices.json"))
	if err != nil || !strings.Contains(string(b), `"secretSha256"`) || strings.Contains(string(b), strings.Split(c.Value, ".")[1]) {
		t.Errorf("devices.json: err %v, has hash only? %s", err, b)
	}
}

func TestPageCarriesLoadFacts(t *testing.T) {
	p, _, h := newTestProbe(t)
	c := pairCookie(t, h, map[string]string{"token": p.token})
	w := do(h, "GET", "/check", nil, c, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if w.Code != 200 {
		t.Fatalf("page: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{`"cookieSent":true`, `"device":"d_`, `"secFetchSite":"same-origin"`, `h264-aac.mp4`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "https://terra.local:7443") {
		t.Errorf("CSP %q", csp)
	}
	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must not be sent")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("page Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	for _, path := range []string{"/static/probe.js", "/static/probe.css", "/static/svelte/probe.js"} {
		if w := do(h, "GET", path, nil, nil, nil); w.Code != 200 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

func TestCADownload(t *testing.T) {
	p, _, h := newTestProbe(t)
	w := do(h, "GET", "/ca.crt", nil, nil, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("ca.crt: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	c, err := x509.ParseCertificate(w.Body.Bytes())
	if err != nil || !c.IsCA || !bytes.Equal(c.Raw, p.caDER) {
		t.Errorf("ca.crt is not the CA in DER: %v", err)
	}
	w = do(h, "GET", "/ca.pem", nil, nil, nil)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("ca.pem: %d", w.Code)
	}
}

func TestMediaObservations(t *testing.T) {
	p, _, h := newTestProbe(t)
	c := pairCookie(t, h, map[string]string{"token": p.token})
	if w := do(h, "GET", "/m/img.png?run=r1", nil, c, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("img: %d", w.Code)
	}
	w := do(h, "GET", "/m/clip/h264-aac.mp4?run=r1", nil, c, map[string]string{"Range": "bytes=0-1"})
	if w.Code != http.StatusPartialContent || w.Body.Len() != 2 {
		t.Errorf("Range: %d len %d", w.Code, w.Body.Len())
	}
	do(h, "GET", "/m/clip/h264-aac.mp4?run=r1", nil, nil, map[string]string{"Range": "bytes=2-"})
	if w := do(h, "GET", "/m/clip/../devices.json?run=r1", nil, c, nil); w.Code == 200 {
		t.Error("clip route served a file outside the clips")
	}
	w = do(h, "GET", "/m/original.jpg?run=r1&download=1", nil, c, nil)
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("download disposition %q", w.Header().Get("Content-Disposition"))
	}

	var obs map[string]mediaObs
	w = do(h, "GET", "/api/obs?run=r1", nil, nil, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &obs); err != nil {
		t.Fatal(err)
	}
	if o := obs["img"]; o.Requests != 1 || o.WithCookie != 1 {
		t.Errorf("img obs %+v", o)
	}
	if o := obs["clip:h264-aac.mp4"]; o.Requests != 2 || o.Ranges != 2 || o.RangeCookie != 1 || o.NoCookie != 1 {
		t.Errorf("clip obs %+v", o)
	}
	if o := obs["download"]; o.WithCookie != 1 {
		t.Errorf("download obs %+v", o)
	}
	b, _ := os.ReadFile(filepath.Join(p.dir, "media.log"))
	if n := strings.Count(string(b), "\n"); n != 4 {
		t.Errorf("media.log has %d lines, want 4", n)
	}
}

func TestThumbCache(t *testing.T) {
	_, _, h := newTestProbe(t)
	w := do(h, "GET", "/m/thumb.jpg?key=k1", nil, nil, nil)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, max-age=3600" || w.Header().Get("ETag") == "" {
		t.Fatalf("thumb: %d %v", w.Code, w.Header())
	}
	if w := do(h, "GET", "/m/thumb.jpg?key=k1", nil, nil, map[string]string{"If-None-Match": w.Header().Get("ETag")}); w.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d", w.Code)
	}
	var c map[string]int
	_ = json.Unmarshal(do(h, "GET", "/api/cache?key=k1", nil, nil, nil).Body.Bytes(), &c)
	if c["full"] != 1 || c["notModified"] != 1 {
		t.Errorf("cache counts %v", c)
	}
}

func TestResultsEndpoint(t *testing.T) {
	p, out, h := newTestProbe(t)
	c := pairCookie(t, h, map[string]string{"token": p.token})
	report := map[string]any{
		"phase": "auto",
		"items": map[string]any{
			"1.fragment":        map[string]any{"status": "PASS"},
			"10.ca-constraints": map[string]any{"status": "WAIT"},
			"2.cookie":          map[string]any{"status": "FAIL"},
		},
	}
	w := do(h, "POST", "/api/results", report, c, nil)
	if w.Code != 200 {
		t.Fatalf("results: %d %s", w.Code, w.Body)
	}
	// Not JSON / no X-Picmover: refused, nothing written.
	r := httptest.NewRequest("POST", "/api/results", strings.NewReader("x"))
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, r)
	if wr.Code != http.StatusForbidden {
		t.Errorf("plain POST: %d", wr.Code)
	}

	f, err := os.Open(p.resultsPath())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []map[string]any
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("results line is not JSON: %v", err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 1 {
		t.Fatalf("%d result lines, want 1", len(lines))
	}
	l := lines[0]
	if l["cookieSent"] != true || !strings.HasPrefix(l["device"].(string), "d_") || !strings.Contains(l["userAgent"].(string), "iPhone") || l["browser"] != "Safari on iOS" {
		t.Errorf("result line %v", l)
	}
	last := out.String()[strings.LastIndex(strings.TrimSpace(out.String()), "\n")+1:]
	if !strings.Contains(last, "Safari on iOS") || !strings.Contains(last, "[auto] 1:PASS 2:FAIL 10:WAIT") {
		t.Errorf("summary line %q", last)
	}

	// A report from the .local host credits the device named in it.
	dev := l["device"].(string)
	b, _ := json.Marshal(map[string]any{"phase": "local", "device": dev, "items": map[string]any{"6.local": map[string]any{"status": "PASS"}}})
	r = httptest.NewRequest("POST", "https://terra.local:7443/api/results", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Picmover", "1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	var who map[string]any
	_ = json.Unmarshal(do(h, "GET", "/api/whoami", nil, c, nil).Body.Bytes(), &who)
	if who["localSeen"] == nil {
		t.Errorf("localSeen not recorded: %v", who)
	}
}
