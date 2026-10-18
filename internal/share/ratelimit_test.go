package share

import (
	"testing"
	"time"
)

func TestLimiterPerKeyAndRefill(t *testing.T) {
	clk := newClock()
	l := PerMinute(5, clk.now)
	for i := 0; i < 5; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("6th request in a minute allowed")
	}
	if !l.Allow("b") {
		t.Fatal("another key was limited")
	}
	clk.add(12 * time.Second) // one token back
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("refill")
	}
	// A global limiter is the same with one key.
	g := PerMinute(20, clk.now)
	n := 0
	for g.Allow("") {
		n++
	}
	if n != 20 {
		t.Fatalf("global burst %d", n)
	}
}

func TestLockout(t *testing.T) {
	clk := newClock()
	l := NewLockout(30, 5*time.Minute, clk.now)
	for i := 0; i < 30; i++ {
		l.Fail("10.0.0.9")
	}
	if l.Locked("10.0.0.9") {
		t.Fatal("locked within the limit")
	}
	l.Fail("10.0.0.9")
	if !l.Locked("10.0.0.9") || l.Locked("10.0.0.8") {
		t.Fatal("lockout per IP")
	}
	clk.add(5*time.Minute - time.Second)
	if !l.Locked("10.0.0.9") {
		t.Fatal("lockout ended early")
	}
	clk.add(2 * time.Second)
	if l.Locked("10.0.0.9") {
		t.Fatal("lockout did not end after 5 minutes")
	}
}
