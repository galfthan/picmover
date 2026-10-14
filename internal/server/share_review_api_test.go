package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"picmover/internal/share"
)

// Every 429 of the share listener says when to try again.
func TestShareReviewRetryAfter(t *testing.T) {
	f := newShareFixture(t)
	s := f.libraryShare()
	cookie := f.pair(s.ID, "Anna")
	retry := func(name string, code int, h http.Header) {
		t.Helper()
		n, err := strconv.Atoi(h.Get("Retry-After"))
		if code != http.StatusTooManyRequests || err != nil || n < 1 {
			t.Errorf("%s: %d, Retry-After %q", name, code, h.Get("Retry-After"))
		}
	}
	// The per-device limit.
	var w = f.phone(http.MethodGet, "/api/session", "", cookie)
	for i := 0; i < 200 && w.Code != http.StatusTooManyRequests; i++ {
		w = f.phone(http.MethodGet, "/api/session", "", cookie)
	}
	retry("device", w.Code, w.Header())
	// The lockout.
	for i := 0; i < 31; i++ {
		f.phoneFrom("10.3.3.3", http.MethodGet, "/api/session", "", share.CookieName+"=junk")
	}
	w = f.phoneFrom("10.3.3.3", http.MethodGet, "/api/session", "", "")
	retry("lockout", w.Code, w.Header())
	if n, _ := strconv.Atoi(w.Header().Get("Retry-After")); n > 300 || n < 200 {
		t.Errorf("lockout Retry-After %d, want about 300", n)
	}
	// Pairing: wrong codes, then the per-address limit.
	for i := 0; i < 5; i++ {
		f.phoneFrom("10.3.3.4", http.MethodPost, "/api/pair", `{"code":"2222-2222","name":"x"}`, "")
	}
	w = f.phoneFrom("10.3.3.4", http.MethodPost, "/api/pair", `{"code":"2222-2222","name":"x"}`, "")
	retry("wrong codes", w.Code, w.Header())
	for i := 0; i < 5; i++ {
		f.phoneFrom("10.3.3.5", http.MethodPost, "/api/pair", `{"token":"x","name":"x"}`, "")
	}
	w = f.phoneFrom("10.3.3.5", http.MethodPost, "/api/pair", `{"token":"x","name":"x"}`, "")
	retry("pair limit", w.Code, w.Header())
}

// A browser paired with the share it scans again is told sameShare.
func TestShareReviewAlreadyPairedSameShare(t *testing.T) {
	f := newShareFixture(t)
	s := f.libraryShare()
	a := f.albumShare()
	cookie := f.pair(s.ID, "Anna")
	for sid, same := range map[string]bool{s.ID: true, a.ID: false} {
		w := f.phone(http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": "Anna"}`, f.issue(sid)), cookie)
		b := decode[alreadyPairedJSON](t, w)
		if w.Code != http.StatusConflict || b.Current.SameShare != same || b.Current.Share != "My phone" {
			t.Errorf("share %s: %d %s", sid, w.Code, w.Body)
		}
		if !same && strings.Contains(w.Body.String(), "sameShare") {
			t.Errorf("sameShare present when false: %s", w.Body)
		}
	}
}

// GET /api/share: every address carries wireless.
func TestShareReviewAddressesWireless(t *testing.T) {
	f := newShareFixture(t)
	w := f.admin(http.MethodGet, "/api/share", "")
	m := jsonMap(t, w)
	addrs, _ := m["addresses"].([]any)
	if len(addrs) == 0 {
		t.Fatalf("no addresses: %s", w.Body)
	}
	for _, a := range addrs {
		if v, ok := a.(map[string]any)["wireless"].(bool); !ok || v {
			t.Errorf("address %v: wireless %v", a, v)
		}
	}
	if _, ok := m["phones"].(float64); !ok {
		t.Errorf("no phones in %s", w.Body)
	}
}
