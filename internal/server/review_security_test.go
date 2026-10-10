package server

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSecFetchSite: cross-site requests are refused even without an
// Origin header (img/video loads, navigations); same-site only from a
// --cors-origin.
func TestSecFetchSite(t *testing.T) {
	f := newFixtureWith(t, func(o *Options) { o.CORSOrigins = []string{"http://localhost:5173"} })
	hex := hexHash(f.hashes["a.jpg"])
	paths := []string{"/api/library", "/api/fs?path=" + url.QueryEscape("/"), "/api/thumb/" + hex,
		"/api/preview/" + hex, "/api/file/" + itoa64(f.ids["a.jpg"]), "/"}
	for _, p := range paths {
		for _, c := range []struct {
			site, origin string
			ok           bool
		}{
			{"cross-site", "", false}, {"Cross-Site", "", false},
			{"cross-site", "http://localhost:5173", false}, // a --cors-origin is never cross-site
			{"same-site", "", false}, {"same-site", "http://localhost:5174", false},
			{"same-site", "http://localhost:5173", true},
			{"same-origin", "", true}, {"same-origin", "http://127.0.0.1:7439", true},
			{"none", "", true}, {"", "", true},
		} {
			hdr := map[string]string{}
			if c.site != "" {
				hdr["Sec-Fetch-Site"] = c.site
			}
			if c.origin != "" {
				hdr["Origin"] = c.origin
			}
			w := f.request(http.MethodGet, p, nil, hdr)
			if c.ok {
				if w.Code == http.StatusForbidden {
					t.Errorf("%s site=%q origin=%q refused: %s", p, c.site, c.origin, w.Body)
				}
				continue
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("%s site=%q origin=%q: %d, want 403", p, c.site, c.origin, w.Code)
				continue
			}
			if strings.HasPrefix(p, "/api/") {
				wantError(t, w, http.StatusForbidden, "")
			}
		}
	}

	// Mutations too, even with the right headers.
	w := f.request(http.MethodPost, "/api/jobs", strings.NewReader(`{"kind":"thumbs"}`), map[string]string{
		"Content-Type": "application/json", "X-Picmover": "1", "Sec-Fetch-Site": "cross-site"})
	wantError(t, w, http.StatusForbidden, "cross-site")
	// A preflight from the --cors-origin (same-site) passes.
	w = f.request(http.MethodOptions, "/api/jobs", nil, map[string]string{
		"Origin": "http://localhost:5173", "Sec-Fetch-Site": "same-site", "Access-Control-Request-Method": "POST"})
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("cors preflight: %d %v", w.Code, w.Header())
	}

	// A top-level cross-site navigation may load the app shell, never an
	// API path; other destinations (iframe) stay refused.
	nav := func(p, dest string) int {
		hdr := map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}
		if dest != "" {
			hdr["Sec-Fetch-Dest"] = dest
		}
		w := f.request(http.MethodGet, p, nil, hdr)
		if w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Errorf("navigate %s: no CORP", p)
		}
		return w.Code
	}
	for _, c := range []struct {
		path, dest string
		code       int
	}{
		{"/", "document", http.StatusOK}, {"/", "", http.StatusOK}, {"/view/3", "document", http.StatusOK},
		{"/", "iframe", http.StatusForbidden},
		{"/api/fs?path=" + url.QueryEscape("/"), "document", http.StatusForbidden},
		{"/api/fs?path=" + url.QueryEscape("/"), "", http.StatusForbidden},
		{"/api/file/" + itoa64(f.ids["a.jpg"]), "document", http.StatusForbidden},
	} {
		if got := nav(c.path, c.dest); got != c.code {
			t.Errorf("cross-site navigate %s dest=%q: %d, want %d", c.path, c.dest, got, c.code)
		}
	}

	// Without --cors-origin, same-site is always refused.
	g := newEmptyServer(t)
	w = g.request(http.MethodGet, "/api/library", nil, map[string]string{"Sec-Fetch-Site": "same-site"})
	wantError(t, w, http.StatusForbidden, "cross-site")
}

// TestCORP: every response, whatever its status, carries
// Cross-Origin-Resource-Policy: same-origin.
func TestCORP(t *testing.T) {
	f := newFixtureWith(t, func(o *Options) {
		o.Static = staticFS()
	})
	hex := hexHash(f.hashes["a.jpg"])
	reqs := []*http.Request{
		newReq(http.MethodGet, "/api/library"), newReq(http.MethodGet, "/api/thumb/"+hex),
		newReq(http.MethodGet, "/api/preview/"+hex), newReq(http.MethodGet, "/api/file/"+itoa64(f.ids["a.jpg"])),
		newReq(http.MethodGet, "/"), newReq(http.MethodGet, "/assets/x.js"), newReq(http.MethodGet, "/assets/missing.js"),
		newReq(http.MethodGet, "/api/nope"), newReq(http.MethodPost, "/api/jobs"),
	}
	bad := newReq(http.MethodGet, "/api/library")
	bad.Host = "evil.example:7439"
	cross := newReq(http.MethodGet, "/api/library")
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	reqs = append(reqs, bad, cross)
	seen := map[int]bool{}
	for _, r := range reqs {
		w := serve(f, r)
		seen[w.Code] = true
		if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
			t.Errorf("%s %s (%d): CORP %q", r.Method, r.URL, w.Code, got)
		}
	}
	for _, c := range []int{200, 403, 404, 421} {
		if !seen[c] {
			t.Errorf("no response with status %d checked", c)
		}
	}
}

func TestValidSourcePath(t *testing.T) {
	for _, c := range []struct {
		p, err string
	}{
		{`\\attacker.example\x`, "network"}, {`//attacker.example/x`, "network"}, {`\\?\C:\x`, "network"},
		{`\\.\pipe\x`, "network"}, {`/\attacker\x`, "network"}, {`\/attacker/x`, "network"},
		{`\??\C:\x`, "network"}, {`\\`, "network"}, {"//", "network"},
		{"/tmp/a\x00b", "NUL"}, {"", "absolute"}, {"relative/dir", "absolute"},
	} {
		err := ValidSourcePath(c.p)
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("ValidSourcePath(%q) = %v, want %q", c.p, err, c.err)
		}
	}
	abs := t.TempDir()
	if err := ValidSourcePath(abs); err != nil {
		t.Errorf("ValidSourcePath(%q) = %v", abs, err)
	}
}

// TestFSRefusesNetworkPaths: /api/fs answers 400 for UNC and device paths
// before touching the filesystem.
func TestFSRefusesNetworkPaths(t *testing.T) {
	f := newEmptyServer(t)
	for _, p := range []string{`\\attacker.example\share`, `//attacker.example/share`, `\\?\UNC\attacker\x`,
		`\\.\PhysicalDrive0`, `\??\C:\`} {
		wantError(t, f.get("/api/fs?path="+url.QueryEscape(p)), http.StatusBadRequest, "network")
	}
	wantError(t, f.get("/api/fs?path="+url.QueryEscape("/tmp\x00")), http.StatusBadRequest, "NUL")
}

func TestNormalizeOriginDefaultPorts(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:80":       "http://localhost",
		"https://x.example:443":     "https://x.example",
		"HTTP://LocalHost:80/":      "http://localhost",
		"http://localhost:443":      "http://localhost:443",
		"https://x.example:80":      "https://x.example:80",
		"http://localhost:5173":     "http://localhost:5173",
		"http://[::1]:80":           "http://[::1]",
		"http://[::1]:5173":         "http://[::1]:5173",
		"http://localhost:":         "http://localhost",
		"http://127.0.0.1:8080":     "http://127.0.0.1:8080",
		"https://x.example:4430":    "https://x.example:4430",
		"http://localhost:8080/":    "http://localhost:8080",
		"http://localhost":          "http://localhost",
		"https://example.com:00443": "https://example.com:00443",
	} {
		got, err := NormalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://:80", "ftp://x", "http://x/p", "http://u@x"} {
		if got, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("NormalizeOrigin(%q) = %q, want error", bad, got)
		}
	}
	// A --cors-origin with an explicit default port matches what browsers send.
	f := newFixtureWith(t, func(o *Options) { o.CORSOrigins = []string{"http://localhost:80"} })
	w := f.request(http.MethodGet, "/api/library", nil, map[string]string{"Origin": "http://localhost"})
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost" {
		t.Fatalf("default-port cors origin: %d %v", w.Code, w.Header())
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func loggerAt(level slog.Level) (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})), buf
}

// TestRefusalLogging: refused requests log only at Debug (serve
// --verbose), so a hostile page cannot flood the terminal.
func TestRefusalLogging(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		log, buf := loggerAt(level)
		f := newFixtureWith(t, func(o *Options) { o.Logger = log })
		for i := 0; i < 5; i++ {
			f.request(http.MethodGet, "/api/library", nil, map[string]string{"Origin": "http://evil.example"})
			f.request(http.MethodGet, "/api/library", nil, map[string]string{"Sec-Fetch-Site": "cross-site"})
			f.request(http.MethodPost, "/api/jobs", strings.NewReader("{}"), map[string]string{"Content-Type": "text/plain"})
			r := newReq(http.MethodGet, "/api/library")
			r.Host = "evil.example:7439"
			serve(f, r)
		}
		got := buf.String()
		if verbose != strings.Contains(got, "request refused") {
			t.Errorf("verbose=%v: log %q", verbose, got)
		}
		if !verbose && got != "" {
			t.Errorf("non-verbose log not empty: %q", got)
		}
	}
}

// TestInternalErrorGeneric: a 500 never carries the error detail (which
// is logged), and an abandoned request writes no 500 and logs at Debug.
func TestInternalErrorGeneric(t *testing.T) {
	log, buf := loggerAt(slog.LevelInfo)
	f := newFixtureWith(t, func(o *Options) { o.Logger = log })
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := f.get("/api/library")
	wantStatus(t, w, http.StatusInternalServerError)
	if w.Body.String() != "{\"error\":\"internal error\"}\n" {
		t.Fatalf("500 body %q", w.Body)
	}
	if got := buf.String(); !strings.Contains(got, "request failed") || !strings.Contains(got, "closed") {
		t.Fatalf("500 not logged with detail: %q", got)
	}

	log, buf = loggerAt(slog.LevelInfo)
	f = newFixtureWith(t, func(o *Options) { o.Logger = log })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newReq(http.MethodGet, "/api/library").WithContext(ctx)
	w = serve(f, r)
	if w.Code == http.StatusInternalServerError || strings.Contains(w.Body.String(), "error") {
		t.Fatalf("abandoned request answered %d %q", w.Code, w.Body)
	}
	if got := buf.String(); got != "" {
		t.Fatalf("abandoned request logged at Info: %q", got)
	}
}

// TestBodyReadDeadline: a client trickling a POST body is cut off.
func TestBodyReadDeadline(t *testing.T) {
	f := newEmptyServer(t)
	f.srv.bodyTimeout = 200 * time.Millisecond
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	_, err = conn.Write([]byte("POST /api/jobs HTTP/1.1\r\nHost: " + testAddr + "\r\n" +
		"Content-Type: application/json\r\nX-Picmover: 1\r\nContent-Length: 100\r\n\r\n{\"kind\""))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("trickled body: status %d", resp.StatusCode)
		}
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("server still waiting for the body after 5 s")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("body read took %v", d)
	}
}
