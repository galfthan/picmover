package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"picmover/internal/share"
)

// S2: wrong codes from one address never cancel the pairings of others,
// and junk from a few addresses does not use up the global budget.
func TestShareReviewPairAbuse(t *testing.T) {
	f := newShareFixture(t)
	lib := f.libraryShare()
	alb := f.albumShare()
	tok, tok2 := f.issue(lib.ID), f.issue(alb.ID)
	for i := 0; i < 5; i++ {
		f.phoneFrom("10.0.0.9", http.MethodPost, "/api/pair", fmt.Sprintf(`{"code":"AAAA-AAA%d","name":"x"}`, i+2), "")
	}
	wantStatus(t, f.phoneFrom("10.0.0.9", http.MethodPost, "/api/pair", `{"code":"AAAA-AAA2","name":"x"}`, ""),
		http.StatusTooManyRequests)
	wantStatus(t, f.phone(http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": "legit"}`, tok), ""), http.StatusOK)
	wantStatus(t, f.phoneFrom("10.0.0.10", http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": "legit"}`, tok2), ""),
		http.StatusOK)

	// Malformed and unknown tokens from many addresses: the global budget
	// is untouched, and so is the next phone.
	for ip := 11; ip < 31; ip++ {
		for i := 0; i < 5; i++ {
			f.phoneFrom(fmt.Sprintf("10.0.0.%d", ip), http.MethodPost, "/api/pair", `{"token":"x","name":"x"}`, "")
			f.phoneFrom(fmt.Sprintf("10.0.1.%d", ip), http.MethodPost, "/api/pair",
				`{"token":"`+strings.Repeat("A", 43)+`","name":"x"}`, "")
		}
	}
	tok = f.issue(lib.ID)
	wantStatus(t, f.phoneFrom("10.0.2.1", http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": "n"}`, tok), ""),
		http.StatusOK)
}

// R12: a full share refuses a redeem without using up the pairing; once a
// phone is revoked, the same token pairs.
func TestShareReviewFullShareKeepsPairing(t *testing.T) {
	f := newShareFixture(t)
	f.srv.share.pairIP = share.PerMinute(10000, f.srv.share.clock)
	s := f.libraryShare()
	var first string
	for i := 0; i < share.MaxDevicesPerShare; i++ {
		c := f.pair(s.ID, fmt.Sprintf("p%d", i))
		if i == 0 {
			first = c
		}
	}
	tok := f.issue(s.ID)
	body := fmt.Sprintf(`{"token": %q, "name": "x"}`, tok)
	wantStatus(t, f.phone(http.MethodPost, "/api/pair", body, ""), http.StatusConflict)
	wantStatus(t, f.admin(http.MethodPost, "/api/share/devices/"+deviceIDOf(first)+"/revoke", "{}"), http.StatusNoContent)
	wantStatus(t, f.phone(http.MethodPost, "/api/pair", body, ""), http.StatusOK)
}

// R8: a redeem waiting for approval when sharing is turned off is told
// so (410, state "off"), not that the computer denied it; a paused share
// gives its state.
func TestShareReviewApprovalEndedBySharingOff(t *testing.T) {
	f := newShareFixture(t)
	s := f.createShare(`{"name": "Ask", "scope": {"kind": "libraries", "libraries": ["peter"]}, "approve": true}`)
	wait := func(tok string, end func()) *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- f.phone(http.MethodPost, "/api/pair", fmt.Sprintf(`{"token": %q, "name": "Anna"}`, tok), "")
		}()
		deadline := time.Now().Add(5 * time.Second)
		for len(f.srv.share.pairs.Requests(s.ID)) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		end()
		return <-done
	}
	w := wait(f.issue(s.ID), func() { f.update(s.ID, `{"paused": true}`) })
	if w.Code != http.StatusGone || decode[pairGoneJSON](t, w).State != share.StatePaused {
		t.Fatalf("share paused during the wait: %d %s", w.Code, w.Body)
	}
	f.update(s.ID, `{"paused": false}`)
	w = wait(f.issue(s.ID), f.srv.share.Stop)
	if b := decode[pairGoneJSON](t, w); w.Code != http.StatusGone || b.State != "off" || !strings.Contains(b.Error, "turned off") {
		t.Fatalf("sharing off during the wait: %d %s", w.Code, w.Body)
	}
}

// R9: a pairing asked for while sharing is paused says paused.
func TestShareReviewPairingWhilePaused(t *testing.T) {
	f := newShareFixture(t)
	s := f.libraryShare()
	f.net.set("10.1.2.3", "10.0.0.0/8", "")
	f.srv.share.checkNetwork()
	w := f.admin(http.MethodPost, "/api/share/shares/"+s.ID+"/pairings", "{}")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "paused") {
		t.Fatalf("issue while paused: %d %s", w.Code, w.Body)
	}
}
