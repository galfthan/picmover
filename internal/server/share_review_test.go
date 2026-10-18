package server

// Regression tests for the findings of the security and robustness
// reviews of sharing on the home Wi-Fi (docs/design/lan-share.md, "Review
// fixes"). Each test names its finding (S1-S5, R1-R16).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// phoneFrom is phone with a chosen remote IP.
func (f *shareFixture) phoneFrom(ip, method, target, body, cookie string) *httptest.ResponseRecorder {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.Host = f.phoneHost()
	r.RemoteAddr = ip + ":40000"
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

// S4: an escaped slash in /assets/ or /icons/ never reaches a file outside
// that folder; the allowlisted files still load.
func TestShareReviewStaticEncodedSlash(t *testing.T) {
	f := newShareFixture(t)
	for _, p := range []string{
		"/assets/..%2Fsecret.txt", "/icons/..%2Fsecret.txt", "/assets/..%2fsecret.txt",
		"/assets/..%2Fother%2Fpage.html", "/assets/..%2Findex.html", "/assets/%2e%2e%2Fsecret.txt",
		"/assets/..%5Csecret.txt", "/assets/sub%2F..%2F..%2Fsecret.txt", "/assets/%2Fsecret.txt",
		"/assets/.hidden/x.js", "/assets/", "/icons/",
	} {
		w := f.phone(http.MethodGet, p, "", "")
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "not for phones") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body)
		}
	}
	for p, want := range map[string]string{"/assets/share-1.js": "console.log(1)", "/icons/icon-192.png": "png",
		"/favicon.png": "png", "/manifest.webmanifest": "{}", "/": "phone", "/share.html": "phone"} {
		w := f.phone(http.MethodGet, p, "", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %q", p, w.Code, w.Body)
		}
	}
}
