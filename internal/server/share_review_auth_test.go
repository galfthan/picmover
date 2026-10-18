package server

import (
	"net/http"
	"strings"
	"testing"

	"picmover/internal/share"
)

// deviceIDOf is the device id in a cookie "__Host-picmover=d_x.secret".
func deviceIDOf(cookie string) string {
	_, v, _ := strings.Cut(cookie, "=")
	id, _, _ := strings.Cut(v, ".")
	return id
}

// R1, S5: a revoked phone's stale requests and unpaired requests are not
// counted against the address's lockout, so the phone can pair again and
// a paired phone at the same address keeps working; malformed cookies and
// wrong secrets still are, and the lockout spares /api/pair and the
// static files.
func TestShareReviewLockout(t *testing.T) {
	f := newShareFixture(t)
	s := f.libraryShare()
	cookie := f.pair(s.ID, "Anna")
	other := f.pair(s.ID, "Ben")
	wantStatus(t, f.admin(http.MethodPost, "/api/share/devices/"+deviceIDOf(cookie)+"/revoke", "{}"), http.StatusNoContent)
	h := hexHash(f.hashes["peter/a.jpg"])
	for i := 0; i < 60; i++ {
		wantStatus(t, f.phone(http.MethodGet, "/api/l/peter/thumb/"+h, "", cookie), http.StatusUnauthorized)
		wantStatus(t, f.phone(http.MethodGet, "/api/session", "", ""), http.StatusUnauthorized)
	}
	wantStatus(t, f.phone(http.MethodGet, "/api/session", "", other), http.StatusOK)
	tok := f.issue(s.ID)
	wantStatus(t, f.phone(http.MethodPost, "/api/pair", `{"token": "`+tok+`", "name": "Anna"}`, ""), http.StatusOK)

	// A wrong secret for an existing device, and malformed cookies, count.
	forged := share.CookieName + "=" + deviceIDOf(other) + "." + strings.Repeat("A", 43)
	for i := 0; i < 31; i++ {
		f.phoneFrom("10.9.9.9", http.MethodGet, "/api/session", "", forged)
	}
	wantStatus(t, f.phoneFrom("10.9.9.9", http.MethodGet, "/api/session", "", other), http.StatusTooManyRequests)
	wantStatus(t, f.phoneFrom("10.9.9.9", http.MethodGet, "/", "", ""), http.StatusOK)
	wantStatus(t, f.phoneFrom("10.9.9.9", http.MethodGet, "/assets/share-1.js", "", ""), http.StatusOK)
	tok = f.issue(s.ID)
	wantStatus(t, f.phoneFrom("10.9.9.9", http.MethodPost, "/api/pair", `{"token": "`+tok+`", "name": "C"}`, ""), http.StatusOK)
	for i := 0; i < 31; i++ {
		f.phoneFrom("10.9.9.8", http.MethodGet, "/api/libraries", "", share.CookieName+"=junk")
	}
	wantStatus(t, f.phoneFrom("10.9.9.8", http.MethodGet, "/api/libraries", "", other), http.StatusTooManyRequests)
	// Other addresses are not affected.
	wantStatus(t, f.phone(http.MethodGet, "/api/session", "", other), http.StatusOK)
}

// R2: a grid's burst of 200 thumbnails is within the per-device budget of
// the media routes; the JSON routes keep their smaller one.
func TestShareReviewThumbBurst(t *testing.T) {
	f := newShareFixture(t)
	cookie := f.pair(f.libraryShare().ID, "Anna")
	h := hexHash(f.hashes["peter/a.jpg"])
	for i := 0; i < 200; i++ {
		if w := f.phone(http.MethodGet, "/api/l/peter/thumb/"+h, "", cookie); w.Code != http.StatusOK {
			t.Fatalf("thumb %d: %d", i, w.Code)
		}
	}
	limited := 0
	for i := 0; i < 150; i++ {
		if f.phone(http.MethodGet, "/api/session", "", cookie).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("150 session requests in a burst were not limited")
	}
}
