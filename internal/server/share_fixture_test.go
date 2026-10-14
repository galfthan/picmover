package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/share"
	"picmover/internal/testimg"
	"picmover/internal/userdata"
)

// fakeNetif is a Netif with fixed answers that tests change.
type fakeNetif struct {
	mu    sync.Mutex
	cands []share.Candidate
	def   net.IP
	gw    net.IP
}

func (f *fakeNetif) Candidates() ([]share.Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]share.Candidate(nil), f.cands...), nil
}

func (f *fakeNetif) DefaultIP() net.IP {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.def
}

func (f *fakeNetif) Gateway() net.IP {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gw
}

func (f *fakeNetif) set(ip, subnet, gw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, n, _ := net.ParseCIDR(subnet)
	f.cands = []share.Candidate{{IP: net.ParseIP(ip).To4(), Iface: "test0", Subnet: n}}
	f.def = net.ParseIP(ip).To4()
	f.gw = net.ParseIP(gw).To4()
}

// lockedBuffer is a log sink safe for concurrent writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// shareFixture is a workspace of three libraries served with sharing on
// (loopback stands in for the LAN):
//
//	peter: a.jpg (GPS), b.jpg, c.jpg, d.jpg imported (renditions exist),
//	       v.mp4 (a video row with a file), t.jpg (trashed);
//	       albums "Other" {c} and "Summer" {a, b, v, t} (Summer has the
//	       higher id and an old created_at)
//	penny: e.jpg
//	other: f.jpg (never shared)
type shareFixture struct {
	t    *testing.T
	root string
	srv  *Server
	net  *fakeNetif
	log  *lockedBuffer

	ids    map[string]int64  // "peter/a.jpg" -> row id
	hashes map[string]uint64 // "peter/a.jpg" -> hash
	albums map[string]int64  // "peter/Summer" -> album id
}

// importLib imports the named test images into a new library at dir and
// returns its rows by base name.
func importLib(t *testing.T, dir string, names ...string) map[string]catalog.Media {
	t.Helper()
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "src")
	seed := 0
	for _, c := range filepath.Base(dir) {
		seed += int(c)
	}
	for i, n := range names {
		opt := testimg.Options{Width: 64, Height: 48, Seed: byte(seed*7 + i), Make: "Canon", Model: "EOS R5",
			DateTime: wall(2024, 7, 10+i, 12, 0, 0)}
		if n == "a.jpg" {
			opt.GPS, opt.Lat, opt.Lon = true, latA, lonA
		}
		writeFile(t, filepath.Join(src, n), testimg.JPEG(opt))
	}
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := importer.New(lib, db, importer.Options{Source: src, Workers: 2,
		Extractor: metadata.Default{DefaultZone: time.UTC}}).Run(ctx)
	if err != nil || st.Imported != int64(len(names)) {
		t.Fatalf("import into %s: %+v %v", dir, st, err)
	}
	rows, err := db.List(ctx, catalog.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]catalog.Media{}
	for _, m := range rows {
		out[filepath.Base(m.OriginalPath)] = m
	}
	return out
}

// insertLibFile writes data at rel in the library at dir and records a
// row for it.
func insertLibFile(t *testing.T, dir, rel string, data []byte, m catalog.Media) catalog.Media {
	t.Helper()
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFile(t, lib.Abs(rel), data)
	h, size, err := library.HashFile(lib.Abs(rel))
	if err != nil {
		t.Fatal(err)
	}
	m.Hash, m.Size, m.RelPath, m.Ext = h, size, rel, strings.TrimPrefix(filepath.Ext(rel), ".")
	if ok, err := db.InsertMedia(context.Background(), nil, &m); err != nil || !ok {
		t.Fatalf("insert %s: %v %v", rel, ok, err)
	}
	return m
}

func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "media")
	f := &shareFixture{t: t, root: root, ids: map[string]int64{}, hashes: map[string]uint64{}, albums: map[string]int64{}}
	add := func(lib string, rows map[string]catalog.Media) {
		for n, m := range rows {
			f.ids[lib+"/"+n], f.hashes[lib+"/"+n] = m.ID, m.Hash
		}
	}
	peter := filepath.Join(root, "peter")
	add("peter", importLib(t, peter, "a.jpg", "b.jpg", "c.jpg", "d.jpg"))
	video := insertLibFile(t, peter, "video/2024/07/v.mp4", bytes.Repeat([]byte("not really a video "), 200),
		catalog.Media{FileType: "video", TakenAt: wall(2024, 7, 30, 9, 0, 0), DurationMS: 4000})
	trashed := insertLibFile(t, peter, "image/2024/07/t.jpg", testimg.JPEG(testimg.Options{Seed: 77}),
		catalog.Media{FileType: "image", TakenAt: wall(2024, 7, 2, 0, 0, 0), DeletedAt: time.Now()})
	add("peter", map[string]catalog.Media{"v.mp4": video, "t.jpg": trashed})
	add("penny", importLib(t, filepath.Join(root, "penny"), "e.jpg"))
	add("other", importLib(t, filepath.Join(root, "other"), "f.jpg"))

	// Albums of peter, with created_at moved into the past so that a new
	// album made by a test has another one.
	u, err := userdata.Open(ctx, filepath.Join(peter, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	other, err := u.CreateAlbum(ctx, "Other", []uint64{f.hashes["peter/c.jpg"]})
	if err != nil {
		t.Fatal(err)
	}
	summer, err := u.CreateAlbum(ctx, "Summer", []uint64{f.hashes["peter/a.jpg"], f.hashes["peter/b.jpg"],
		f.hashes["peter/v.mp4"], f.hashes["peter/t.jpg"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.SetRating(ctx, []uint64{f.hashes["peter/a.jpg"]}, 4); err != nil {
		t.Fatal(err)
	}
	u.Close()
	raw, err := sql.Open("sqlite", filepath.Join(peter, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE albums SET created_at = 1700000000"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	f.albums["peter/Other"], f.albums["peter/Summer"] = other.ID, summer.ID

	f.log = &lockedBuffer{}
	f.srv, err = New(Options{Workspace: root, Addr: testAddr, ShareDir: filepath.Join(base, "share"),
		Logger: slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Static: fstest.MapFS{
			"index.html":            {Data: []byte("<!doctype html>admin")},
			"share.html":            {Data: []byte("<!doctype html>phone")},
			"assets/share-1.js":     {Data: []byte("console.log(1)")},
			"favicon.png":           {Data: []byte("png")},
			"manifest.webmanifest":  {Data: []byte("{}")},
			"icons/icon-192.png":    {Data: []byte("png")},
			"secret.txt":            {Data: []byte("not for phones")},
			"assets/.hidden/x.js":   {Data: []byte("x")},
			"other/page.html":       {Data: []byte("x")},
			"assets/admin-chunk.js": {Data: []byte("admin")},
		}})
	if err != nil {
		t.Fatal(err)
	}
	f.net = &fakeNetif{}
	f.net.set("127.0.0.1", "127.0.0.0/8", "")
	sh := f.srv.share
	sh.netif = f.net
	sh.hostname = "testhost"
	sh.listen = func(addr string) (net.Listener, error) {
		host, _, _ := net.SplitHostPort(addr)
		return net.Listen("tcp4", net.JoinHostPort(host, "0"))
	}
	t.Cleanup(func() { f.srv.Shutdown(context.Background()) })
	if err := sh.Start(); err != nil {
		t.Fatalf("start sharing: %v", err)
	}
	return f
}

// phoneHost is the Host of phone requests.
func (f *shareFixture) phoneHost() string {
	h := f.srv.share.host.Load()
	if h == nil {
		f.t.Fatal("sharing is not on")
	}
	return net.JoinHostPort(h.ip, h.port)
}

// admin runs a request on the admin handler from loopback.
func (f *shareFixture) admin(method, target, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.Host = testAddr
	r.RemoteAddr = "127.0.0.1:50000"
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Picmover", "1")
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}

// phone runs a request on the share handler as a phone with cookie
// ("" for none).
func (f *shareFixture) phone(method, target, body, cookie string) *httptest.ResponseRecorder {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.Host = f.phoneHost()
	r.RemoteAddr = "127.0.0.1:40000"
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Picmover", "1")
	}
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	f.srv.share.handler.ServeHTTP(w, r)
	return w
}

// createShare creates a share through the admin API and returns it.
func (f *shareFixture) createShare(body string) adminShareJSON {
	f.t.Helper()
	w := f.admin(http.MethodPost, "/api/share/shares", body)
	wantStatus(f.t, w, http.StatusCreated)
	return decode[adminShareJSON](f.t, w)
}

// libraryShare creates "My phone" over peter and penny with all
// capabilities.
func (f *shareFixture) libraryShare() adminShareJSON {
	return f.createShare(`{"name": "My phone", "scope": {"kind": "libraries", "libraries": ["peter", "penny"]}}`)
}

// albumShare creates "Summer guests" over peter's Summer album with the
// album defaults (video only).
func (f *shareFixture) albumShare() adminShareJSON {
	return f.createShare(fmt.Sprintf(`{"name": "Summer guests", "scope": {"kind": "album", "library": "peter", "album": %d}}`,
		f.albums["peter/Summer"]))
}

// pair pairs a phone with share sid through a token and returns its
// cookie ("__Host-picmover=<value>").
func (f *shareFixture) pair(sid, name string) string {
	f.t.Helper()
	tok := f.issue(sid)
	w := f.phone(http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": %q}`, tok, name), "")
	wantStatus(f.t, w, http.StatusOK)
	return cookieOf(f.t, w)
}

// issue opens a pairing for sid and returns its token.
func (f *shareFixture) issue(sid string) string {
	f.t.Helper()
	w := f.admin(http.MethodPost, "/api/share/shares/"+sid+"/pairings", "{}")
	wantStatus(f.t, w, http.StatusOK)
	p := decode[pairingJSON](f.t, w)
	_, tok, ok := strings.Cut(p.URL, "#/pair/")
	if !ok || !strings.HasPrefix(p.URL, "https://127.0.0.1:") {
		f.t.Fatalf("pairing url %q", p.URL)
	}
	return tok
}

// cookieOf returns the Cookie header value of a response's device cookie.
func cookieOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == share.CookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("no device cookie in %v", w.Header())
	return ""
}

// update changes a share through the admin API.
func (f *shareFixture) update(sid, body string) {
	f.t.Helper()
	wantStatus(f.t, f.admin(http.MethodPost, "/api/share/shares/"+sid, body), http.StatusOK)
}

// jsonMap decodes a JSON object.
func jsonMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return m
}
