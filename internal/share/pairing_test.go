package share

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairingTokenSingleUseAndTTL(t *testing.T) {
	clk := newClock()
	p := NewPairings(clk.now)
	tk := p.Issue("s_a")
	if len(tk.Token) != 43 || len(tk.Code) != 8 || !tk.ExpiresAt.Equal(clk.t.Add(PairingTTL)) {
		t.Fatalf("ticket %+v", tk)
	}
	c, err := p.Find(tk.Token, "", "10.0.0.1")
	if err != nil || c.ShareID != "s_a" {
		t.Fatalf("find: %v %+v", err, c)
	}
	// Finding does not use it up; using does, once.
	if _, err := p.Find(tk.Token, "", "10.0.0.1"); err != nil {
		t.Fatal("find used the token up")
	}
	if err := p.Use(c); err != nil {
		t.Fatal(err)
	}
	if err := p.Use(c); !errors.Is(err, ErrPairingGone) {
		t.Fatalf("second use: %v", err)
	}
	if _, err := p.Find(tk.Token, "", "10.0.0.1"); !errors.Is(err, ErrPairingGone) {
		t.Fatalf("replay: %v", err)
	}
	// Expiry at TTL+1s; the code dies with the token.
	tk = p.Issue("s_a")
	clk.add(PairingTTL + time.Second)
	if _, err := p.Find(tk.Token, "", "10.0.0.1"); !errors.Is(err, ErrPairingGone) {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := p.Find("", tk.Code, "10.0.0.1"); !errors.Is(err, ErrPairingGone) {
		t.Fatalf("expired code: %v", err)
	}
	// Malformed values.
	for _, tc := range [][2]string{{"short", ""}, {"", "K7QM"}, {"", "K7QM-3XR0"}, {"", ""}} {
		if _, err := p.Find(tc[0], tc[1], "10.0.0.1"); !errors.Is(err, ErrMalformed) {
			t.Errorf("Find(%q, %q) = %v", tc[0], tc[1], err)
		}
	}
	// A restart (a new registry) knows no tokens.
	tk = p.Issue("s_a")
	if _, err := NewPairings(clk.now).Find(tk.Token, "", "10.0.0.1"); !errors.Is(err, ErrPairingGone) {
		t.Fatal("token survived a restart")
	}
}

func TestPairingConcurrentRedeem(t *testing.T) {
	p := NewPairings(nil)
	tk := p.Issue("s_a")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if c, err := p.Find(tk.Token, "", "10.0.0.1"); err == nil && p.Use(c) == nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d winners", wins.Load())
	}
}

func TestPairingCodes(t *testing.T) {
	clk := newClock()
	p := NewPairings(clk.now)
	tk := p.Issue("s_a")
	typed := strings.ToLower(FormatCode(tk.Code))
	if !strings.Contains(typed, "-") {
		t.Fatalf("format %q", typed)
	}
	c, err := p.Find("", " "+typed+" ", "10.0.0.1")
	if err != nil || c.ShareID != "s_a" {
		t.Fatalf("typed code %q: %v", typed, err)
	}
	// Five wrong codes from one address block that address (S2), and
	// cancel nobody's pairing.
	other := p.Issue("s_b")
	wrong := "22222222"
	if wrong == tk.Code || wrong == other.Code {
		wrong = "33333333"
	}
	for i := 0; i < 5; i++ {
		if p.Blocked("10.0.0.9") {
			t.Fatalf("blocked after %d wrong codes", i)
		}
		if _, err := p.Find("", wrong, "10.0.0.9"); !errors.Is(err, ErrPairingGone) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if !p.Blocked("10.0.0.9") || p.Blocked("10.0.0.1") {
		t.Fatal("the address with 5 wrong codes is not the one blocked")
	}
	if _, err := p.Find(other.Token, "", "10.0.0.1"); err != nil {
		t.Fatal("wrong codes cancelled another pairing")
	}
	if _, err := p.Find("", other.Code, "10.0.0.1"); err != nil {
		t.Fatal("wrong codes cancelled another pairing's code")
	}
	if p.Notice("s_a") != WrongCodesNotice("10.0.0.9") || p.Notice("s_b") != WrongCodesNotice("10.0.0.9") {
		t.Fatal("no notice for the Share page")
	}
	p.Issue("s_a")
	if p.Notice("s_a") != "" {
		t.Fatal("a new pairing kept the notice")
	}
	clk.add(CodeBlockFor)
	if p.Blocked("10.0.0.9") {
		t.Fatal("still blocked after CodeBlockFor")
	}
}

func TestPairingLimits(t *testing.T) {
	p := NewPairings(newClock().now)
	first := p.Issue("s_a")
	for i := 0; i < 3; i++ {
		p.Issue("s_a")
	}
	if _, err := p.Find(first.Token, "", "10.0.0.1"); !errors.Is(err, ErrPairingGone) {
		t.Fatal("4th pairing of a share did not drop the oldest")
	}
	var all []Ticket
	for i := 0; i < 12; i++ {
		all = append(all, p.Issue("s_"+string(rune('b'+i))))
	}
	open := 0
	for _, tk := range all {
		if _, err := p.Find(tk.Token, "", "10.0.0.1"); err == nil {
			open++
		}
	}
	if open != 10 {
		t.Fatalf("%d open, want 10 in total", open)
	}
	p.Cancel("s_m")
	if _, ok := p.Open("s_m"); ok {
		t.Fatal("cancel left a pairing open")
	}
}

func TestPairingApproval(t *testing.T) {
	p := NewPairings(nil)
	ctx := context.Background()

	r := p.Ask("s_a", "Mia's phone", "Chrome on Android", "192.168.1.40")
	if got := p.Requests("s_a"); len(got) != 1 || got[0].Name != "Mia's phone" {
		t.Fatalf("requests %+v", got)
	}
	go func() {
		for len(p.Requests("s_a")) == 0 {
		}
		p.Decide("s_a", r.ID, true)
	}()
	if err := p.Wait(ctx, r, time.Second); err != nil {
		t.Fatalf("approved: %v", err)
	}
	if len(p.Requests("s_a")) != 0 {
		t.Fatal("decided request still listed")
	}

	r = p.Ask("s_a", "x", "", "")
	if err := p.Decide("s_b", r.ID, true); !errors.Is(err, ErrNoRequest) {
		t.Fatal("decided through another share")
	}
	p.Decide("s_a", r.ID, false)
	if err := p.Wait(ctx, r, time.Second); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied: %v", err)
	}

	r = p.Ask("s_a", "x", "", "")
	if err := p.Wait(ctx, r, 10*time.Millisecond); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("timeout: %v", err)
	}
	if len(p.Requests("s_a")) != 0 {
		t.Fatal("timed-out request still listed")
	}

	// R8: sharing turned off is not a denial; nor is the share paused.
	r = p.Ask("s_a", "x", "", "")
	p.Reset()
	if err := p.Wait(ctx, r, time.Second); !errors.Is(err, ErrSharingOff) {
		t.Fatalf("reset: %v", err)
	}
	r = p.Ask("s_a", "x", "", "")
	p.DropShare("s_a")
	if err := p.Wait(ctx, r, time.Second); !errors.Is(err, ErrShareDropped) {
		t.Fatalf("drop share: %v", err)
	}
}
