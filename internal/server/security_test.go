package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestHostCheck(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		host string
		ok   bool
	}{
		{"127.0.0.1:7439", true}, {"localhost:7439", true}, {"LOCALHOST:7439", true}, {"[::1]:7439", true},
		{"127.0.0.1:7440", false}, {"localhost:80", false}, {"localhost", false}, {"evil.example:7439", false},
		{"evil.example", false}, {"192.168.1.5:7439", false}, {"127.0.0.2:7439", false}, {"", false},
	} {
		for _, p := range []string{"/api/l/lib/library", "/", "/api/l/lib/thumb/0000000000000001"} {
			r := newReq(http.MethodGet, p)
			r.Host = c.host
			w := serve(f, r)
			if c.ok {
				if w.Code == http.StatusMisdirectedRequest {
					t.Errorf("host %q %s refused", c.host, p)
				}
				continue
			}
			if w.Code != http.StatusMisdirectedRequest || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
				!strings.Contains(w.Body.String(), "misdirected") {
				t.Errorf("host %q %s: %d %q", c.host, p, w.Code, w.Body)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("host %q: security headers missing on 421: %v", c.host, w.Header())
			}
		}
	}

	// A wildcard listener accepts IP literals (DNS rebinding needs a
	// name), never other names.
	g, err := newGuard("0.0.0.0:7439", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !g.hostOK("192.168.1.5:7439") || !g.hostOK("localhost:7439") || g.hostOK("evil.example:7439") || g.hostOK("192.168.1.5:80") {
		t.Error("wildcard host rules")
	}
	g, _ = newGuard("myhost.lan:8080", nil)
	if !g.hostOK("myhost.lan:8080") || !g.hostOK("MyHost.lan:8080") || g.hostOK("other.lan:8080") {
		t.Error("named listener host rules")
	}
	if _, err := newGuard("127.0.0.1:0", nil); err == nil {
		t.Error("port 0 accepted")
	}
	if _, err := newGuard("127.0.0.1:1", []string{"http://x/path"}); err == nil {
		t.Error("bad cors origin accepted")
	}
}

func TestOriginCheck(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		origin string
		ok     bool
	}{
		{"http://127.0.0.1:7439", true}, {"http://localhost:7439", true}, {"http://[::1]:7439", true},
		{"http://evil.example", false}, {"http://evil.example:7439", false}, {"https://127.0.0.1:7439", false},
		{"http://127.0.0.1:5173", false}, {"http://localhost:5173", false}, {"null", false}, {"file://", false},
	} {
		r := newReq(http.MethodGet, "/api/l/lib/library")
		r.Header.Set("Origin", c.origin)
		w := serve(f, r)
		if c.ok && w.Code != http.StatusOK {
			t.Errorf("origin %q: %d", c.origin, w.Code)
		}
		if !c.ok {
			wantError(t, w, http.StatusForbidden, "origin not allowed")
		}
		if h := corsHeaders(w.Header()); len(h) > 0 {
			t.Errorf("origin %q without --cors-origin got CORS headers %v", c.origin, h)
		}
	}
	// A static page request from a foreign origin is refused too (text).
	r := newReq(http.MethodGet, "/")
	r.Header.Set("Origin", "http://evil.example")
	if w := serve(f, r); w.Code != http.StatusForbidden || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("static foreign origin: %d %v", w.Code, w.Header())
	}
}

func corsHeaders(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		if strings.HasPrefix(k, "Access-Control-") {
			out[k] = v
		}
	}
	return out
}

func TestPreflightWithoutCORS(t *testing.T) {
	f := newFixture(t)
	r := newReq(http.MethodOptions, "/api/jobs")
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "content-type, x-picmover")
	w := serve(f, r)
	if w.Code != http.StatusForbidden || len(corsHeaders(w.Header())) != 0 || w.Header().Get("Vary") != "" {
		t.Fatalf("preflight without --cors-origin: %d %v", w.Code, w.Header())
	}
}

func TestCORSOrigin(t *testing.T) {
	f := newFixtureWith(t, func(o *Options) { o.CORSOrigins = []string{"http://localhost:5173/"} })
	r := newReq(http.MethodOptions, "/api/jobs")
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := serve(f, r)
	h := w.Header()
	if w.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		h.Get("Access-Control-Allow-Methods") != "GET, POST" || h.Get("Access-Control-Allow-Headers") != "Content-Type, X-Picmover" ||
		h.Get("Vary") != "Origin" {
		t.Fatalf("preflight: %d %v", w.Code, h)
	}
	r = newReq(http.MethodGet, "/api/l/lib/library")
	r.Header.Set("Origin", "http://localhost:5173")
	w = serve(f, r)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || w.Header().Get("Vary") != "Origin" {
		t.Fatalf("cors GET: %d %v", w.Code, w.Header())
	}
	// Same-origin requests get no Allow-Origin; other origins stay refused.
	r = newReq(http.MethodGet, "/api/l/lib/library")
	r.Header.Set("Origin", "http://127.0.0.1:7439")
	if w = serve(f, r); w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("same origin: %d %v", w.Code, w.Header())
	}
	r = newReq(http.MethodGet, "/api/l/lib/library")
	r.Header.Set("Origin", "http://localhost:5174")
	if w = serve(f, r); w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("other origin: %d %v", w.Code, w.Header())
	}
}

func TestMutationHeaders(t *testing.T) {
	f := newFixture(t)
	body := `{"kind":"bogus"}`
	for _, c := range []struct {
		hdr  map[string]string
		code int
	}{
		{map[string]string{}, http.StatusForbidden},
		{map[string]string{"Content-Type": "application/json"}, http.StatusForbidden},
		{map[string]string{"X-Picmover": "1"}, http.StatusForbidden},
		{map[string]string{"Content-Type": "text/plain", "X-Picmover": "1"}, http.StatusForbidden},
		{map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Picmover": "1"}, http.StatusForbidden},
		{map[string]string{"Content-Type": "application/json", "X-Picmover": "0"}, http.StatusForbidden},
		// Passes the checks; the body is then refused by the handler.
		{map[string]string{"Content-Type": "application/json", "X-Picmover": "1"}, http.StatusBadRequest},
		{map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Picmover": "1"}, http.StatusBadRequest},
	} {
		for _, p := range []string{"/api/l/lib/jobs", "/api/jobs/1/cancel"} {
			w := f.request(http.MethodPost, p, strings.NewReader(body), c.hdr)
			want := c.code
			if p != "/api/l/lib/jobs" && want == http.StatusBadRequest {
				want = http.StatusNotFound // no job 1
			}
			if w.Code != want {
				t.Errorf("%s %v: %d %s, want %d", p, c.hdr, w.Code, w.Body, want)
			}
		}
	}
	// Body cap.
	big := `{"kind":"import","source":"` + strings.Repeat("x", 70<<10) + `"}`
	w := f.post("/api/l/lib/jobs", big)
	wantError(t, w, http.StatusRequestEntityTooLarge, "too large")
	// Wrong methods.
	if w := f.request(http.MethodDelete, "/api/l/lib/jobs", nil, map[string]string{"Content-Type": "application/json", "X-Picmover": "1"}); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/jobs: %d", w.Code)
	}
	if w := f.post("/api/l/lib/library", "{}"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/l/lib/library: %d", w.Code)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:7439": true, "localhost:1": true, "[::1]:7439": true, "0.0.0.0:7439": false, ":7439": false,
		"192.168.1.2:80": false, "nonsense": false,
	} {
		if got := IsLoopbackAddr(addr); got != want {
			t.Errorf("IsLoopbackAddr(%q) = %v", addr, got)
		}
	}
}
