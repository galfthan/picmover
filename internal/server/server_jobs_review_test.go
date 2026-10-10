package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"picmover/internal/importer"
)

// Error results are kept apart from the result ring, so a late subscriber
// still receives early errors after many successes.
func TestErrorResultsSurviveRing(t *testing.T) {
	j := &job{params: jobParams{Kind: kindImport}, state: stateRunning, created: time.Now(), done: make(chan struct{})}
	errSeqs := map[int64]bool{}
	for i := 1; i <= 600; i++ {
		st := importer.StatusImported
		if i%10 == 0 && i <= 50 {
			st = importer.StatusError
			errSeqs[int64(i)] = true
		}
		j.onResult(importer.Result{Status: st, Source: fmt.Sprint(i)})
	}
	if len(errSeqs) != 5 {
		t.Fatalf("%d errors", len(errSeqs))
	}
	_, ring, sub := j.subscribe()
	if sub == nil {
		t.Fatal("no subscriber for a running job")
	}
	j.unsubscribe(sub)
	if len(ring) != 5+ringSize {
		t.Fatalf("replay has %d results, want %d", len(ring), 5+ringSize)
	}
	got := 0
	for i, r := range ring {
		if i > 0 && r.Seq <= ring[i-1].Seq {
			t.Fatalf("replay not in seq order at %d: %d after %d", i, r.Seq, ring[i-1].Seq)
		}
		if r.Status == string(importer.StatusError) {
			if !errSeqs[r.Seq] {
				t.Fatalf("unexpected error result %+v", r)
			}
			got++
		}
	}
	if got != 5 || ring[len(ring)-1].Seq != 600 {
		t.Fatalf("errors replayed %d, last seq %d", got, ring[len(ring)-1].Seq)
	}

	// An error still inside the general ring is replayed once.
	j.onResult(importer.Result{Status: importer.StatusError, Source: "late"})
	_, ring, _ = j.subscribe()
	n := 0
	for _, r := range ring {
		if r.Seq == 601 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("recent error replayed %d times", n)
	}
}

// The error ring is capped.
func TestErrorRingCapped(t *testing.T) {
	j := &job{params: jobParams{Kind: kindImport}, state: stateRunning, created: time.Now(), done: make(chan struct{})}
	for range errorRingSize + 50 {
		j.onResult(importer.Result{Status: importer.StatusError})
	}
	for range ringSize {
		j.onResult(importer.Result{Status: importer.StatusImported})
	}
	_, ring, _ := j.subscribe()
	if len(ring) != errorRingSize+ringSize || ring[0].Seq != 51 {
		t.Fatalf("replay %d results, first seq %d", len(ring), ring[0].Seq)
	}
}

// At most maxSubscribers streams per running job; the next gets a 503 JSON
// error. A finished job is not limited (its replay needs no subscriber).
func TestSubscriberCap(t *testing.T) {
	l := newLive(t, nil)
	j := &job{id: 999, params: jobParams{Kind: kindThumbs}, state: stateRunning, created: time.Now(), done: make(chan struct{})}
	l.srv.jobs.mu.Lock()
	l.srv.jobs.jobs = append(l.srv.jobs.jobs, j)
	l.srv.jobs.mu.Unlock()
	for i := range maxSubscribers {
		if _, _, sub, err := j.trySubscribe(); err != nil || sub == nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
	}
	if _, _, _, err := j.trySubscribe(); err != errTooManySubscribers {
		t.Fatalf("subscriber beyond the cap: %v", err)
	}
	code, b := l.do(http.MethodGet, "/api/jobs/999/events", "")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(b), `"error"`) || !strings.Contains(string(b), "too many") {
		t.Fatalf("events beyond the cap: %d %s", code, b)
	}
	j.finish(stateDone, 0, importer.Stats{}, 0, "")
	if _, _, sub, err := j.trySubscribe(); err != nil || sub != nil {
		t.Fatalf("finished job: sub %v err %v", sub, err)
	}
}

// Network (UNC) and device sources are refused lexically.
func TestJobSourceUNCRefused(t *testing.T) {
	l := newLive(t, nil)
	for _, src := range []string{`\\\\host\\share\\photos`, `//host/share/photos`, `\\\\?\\C:\\photos`, `\\\\.\\PhysicalDrive0`, "/tmp/a\\u0000b"} {
		body := `{"kind":"import","source":"` + src + `"}`
		code, b := l.do(http.MethodPost, "/api/l/lib/jobs", body)
		if code != http.StatusBadRequest || !strings.Contains(string(b), "source") {
			t.Errorf("%s: %d %s, want 400", body, code, b)
		}
	}
}

// The thumbnail-stage message quotes the library root like the CLI.
func TestOutcomeQuotesRoot(t *testing.T) {
	_, _, msg := outcome(kindImport, importer.Stats{}, &importer.ThumbStageError{Err: fmt.Errorf("bad")}, "L", "/my photos")
	if !strings.Contains(msg, `'picmover thumbs "/my photos"'`) {
		t.Fatalf("message %q", msg)
	}
}

// A zero-width box matches points at exactly that longitude; on the
// antimeridian it becomes a zero-width crossing box matching 180 and -180.
func TestParseBBoxZeroWidth(t *testing.T) {
	for in, want := range map[string]bbox{
		"25,0,25,10":     {25, 0, 25, 10},
		"190,0,190,10":   {-170, 0, -170, 10},
		"180,0,180,10":   {180, 0, -180, 10},
		"-180,0,-180,10": {180, 0, -180, 10},
	} {
		got, err := parseBBox(in)
		if err != nil || got != want {
			t.Errorf("parseBBox(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}
