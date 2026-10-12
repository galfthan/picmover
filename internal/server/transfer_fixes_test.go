package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/transfer"
	"picmover/internal/userdata"
)

// interruptedRename leaves a move of m from peter into penny interrupted
// after its rename (the file in penny, no row there yet) and returns the
// record id. sourceID and sourceRoot are the journal's source ("" for
// peter's own; another source leaves the file where it is).
func interruptedRename(t *testing.T, f *fixture, root string, m catalog.Media, sourceID, sourceRoot string) int64 {
	t.Helper()
	ctx := context.Background()
	own := sourceID == ""
	if own {
		_, hp := f.srv.ws.entry("peter").snapshot()
		id, err := hp.user.LibraryID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sourceID = id
		sourceRoot, _ = filepath.EvalSymlinks(filepath.Join(root, "peter"))
	}
	wantStatus(t, f.post("/api/libraries/penny/open", "{}"), http.StatusOK)
	_, h := f.srv.ws.entry("penny").snapshot()
	id, err := h.user.BeginImport(ctx, userdata.ImportRecord{Source: sourceRoot, SourceName: "peter",
		Mode: userdata.ImportModeTransfer})
	if err != nil {
		t.Fatal(err)
	}
	journal := fmt.Sprintf(`{"v":1,"id":%d,"source":{"id":%q,"root":%q,"name":"peter"},"target":{"id":"x","root":%q,"name":"penny"},"startedAt":1}
{"op":"begin","hash":%q,"kind":"move","rel":%q,"to":%q,"mode":"rename"}
`, id, sourceID, sourceRoot, filepath.Join(root, "penny"), hexHash(m.Hash), m.RelPath, m.RelPath)
	writeFile(t, filepath.Join(root, "penny", ".picmover", "transfers", fmt.Sprint(id)+".journal"), []byte(journal))
	if !own {
		return id
	}
	src := filepath.Join(root, "peter", filepath.FromSlash(m.RelPath))
	dst := filepath.Join(root, "penny", filepath.FromSlash(m.RelPath))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	return id
}

// An interrupted move involving either library refuses a new move (409,
// no state); the pair's own interrupted move does not (Run finishes it).
func TestTransferRefusedWhileOtherMoveInterrupted(t *testing.T) {
	f, root, ph := transferWorkspace(t)
	interruptedRename(t, f, root, ph["two"], "", "")

	for _, req := range []struct{ lib, to string }{{"peter", "paul"}, {"paul", "penny"}} {
		wantStatus(t, f.post("/api/libraries/"+req.lib+"/open", "{}"), http.StatusOK)
		w := f.post("/api/l/"+req.lib+"/jobs", `{"kind":"transfer","to":"`+req.to+`","hashes":`+hashesJSON(ph["one"])+`}`)
		wantStatus(t, w, http.StatusConflict)
		if !strings.Contains(w.Body.String(), "the move from peter to penny was interrupted; finish it first") ||
			strings.Contains(w.Body.String(), `"state"`) {
			t.Fatalf("%s -> %s: %s", req.lib, req.to, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "paul", "image", "2024", "06", "one.jpg")); err == nil {
		t.Fatal("moved to paul")
	}

	// Between the two libraries of the interrupted move: it is finished first.
	w := f.post("/api/l/peter/jobs", `{"kind":"transfer","to":"penny","hashes":`+hashesJSON(ph["one"])+`}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if res := j.Result.(map[string]any); j.State != stateDone || res["recovered"] != 1.0 || res["moved"] != 1.0 {
		t.Fatalf("job %+v", j)
	}
	if n := f.countAllIn("penny"); n != 2 {
		t.Fatalf("penny has %d photos", n)
	}
}

// An unreadable journal and a move from a library not in the workspace
// cannot be finished: they are listed with the reason, the resume
// answers 409 with it, and they do not block other moves.
func TestTransferInterruptedProblems(t *testing.T) {
	f, root, ph := transferWorkspace(t)
	wantStatus(t, f.post("/api/libraries/penny/open", "{}"), http.StatusOK)
	writeFile(t, filepath.Join(root, "penny", ".picmover", "transfers", "7.journal"), []byte("garbage\n"))

	ls := f.libraries()
	l, _ := libByID(ls, "penny")
	if len(l.InterruptedMoves) != 1 || l.InterruptedMoves[0].ID != 7 || l.InterruptedMoves[0].From != "" ||
		!strings.Contains(l.InterruptedMoves[0].Problem, "cannot be read") {
		t.Fatalf("penny %+v", l.InterruptedMoves)
	}
	w := f.post("/api/libraries/penny/transfer/resume", "{}")
	wantStatus(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "the interrupted move into penny could not be finished: its journal (.picmover/transfers/7.journal) cannot be read") {
		t.Fatalf("resume: %s", w.Body)
	}
	if err := os.Remove(filepath.Join(root, "penny", ".picmover", "transfers", "7.journal")); err != nil {
		t.Fatal(err)
	}

	gone := filepath.Join(t.TempDir(), "elsewhere")
	id := interruptedRename(t, f, root, ph["two"], "0123456789abcdef", gone)
	l, _ = libByID(f.libraries(), "penny")
	if len(l.InterruptedMoves) != 1 || l.InterruptedMoves[0] != (interruptedJSON{From: "peter", To: "penny", ID: id,
		Problem: "the library it came from (peter) is not in this workspace; put it back to finish the move"}) {
		t.Fatalf("penny %+v", l.InterruptedMoves)
	}
	if p, _ := libByID(f.libraries(), "peter"); len(p.InterruptedMoves) != 1 {
		t.Fatalf("peter (named in the journal) %+v", p.InterruptedMoves)
	}
	w = f.post("/api/libraries/penny/transfer/resume", "{}")
	wantStatus(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "not in this workspace") {
		t.Fatalf("resume: %s", w.Body)
	}
	w = f.post("/api/l/peter/transfer/plan", `{"to":"penny","hashes":`+hashesJSON(ph["one"])+`}`)
	if plan := decode[transferPlanJSON](t, w); len(plan.Interrupted) != 1 || plan.Interrupted[0].Problem == "" {
		t.Fatalf("plan %+v", plan)
	}
	// Not blocking.
	w = f.post("/api/l/peter/jobs", `{"kind":"transfer","to":"penny","hashes":`+hashesJSON(ph["one"])+`}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateDone || j.ExitCode != 0 {
		t.Fatalf("job %+v", j)
	}
}

// A resume that finishes one move and cannot finish another ends with
// exit 2 and says which and why; nothing is counted as a failed file.
func TestTransferResumeReportsProblems(t *testing.T) {
	f, root, ph := transferWorkspace(t)
	interruptedRename(t, f, root, ph["two"], "", "")
	writeFile(t, filepath.Join(root, "penny", ".picmover", "transfers", "999999.journal"), []byte("garbage\n"))

	w := f.post("/api/libraries/penny/transfer/resume", "{}")
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	res := j.Result.(map[string]any)
	if j.State != stateDone || j.ExitCode != 2 || res["recovered"] != 1.0 || res["failedCount"] != 0.0 ||
		!strings.Contains(deref(j.Error), "the interrupted move into penny could not be finished: its journal") {
		t.Fatalf("job %+v error %q", j, deref(j.Error))
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// While a move holds a library, trash requests and user data changes on
// it are refused at once (409, no state) instead of waiting for the move
// or being lost; reads go on.
func TestTransferRefusesEditsWhileMoving(t *testing.T) {
	f, _, ph := transferWorkspace(t)
	wantStatus(t, f.post("/api/libraries/penny/open", "{}"), http.StatusOK)
	unlock := lockPair(f.srv.ws.entry("peter"), f.srv.ws.entry("penny"))
	one := hashesJSON(ph["one"])
	edits := []struct{ path, body string }{
		{"/api/l/peter/ratings", `{"rating":5,"hashes":` + one + `}`},
		{"/api/l/penny/ratings", `{"rating":5,"hashes":` + one + `}`},
		{"/api/l/peter/tags", `{"name":"x"}`},
		{"/api/l/peter/media/tags", `{"hashes":` + one + `,"create":["x"]}`},
		{"/api/l/peter/albums", `{"name":"a","hashes":` + one + `}`},
		{"/api/l/peter/similar/ignore", `{"hashes":` + hashesJSON(ph["one"], ph["two"]) + `}`},
		{"/api/l/peter/trash", `{"hashes":` + one + `}`},
		{"/api/l/peter/trash/restore", `{"hashes":` + one + `}`},
		{"/api/l/peter/trash/delete", `{"confirm":true,"hashes":` + one + `}`},
		{"/api/l/penny/trash", `{"hashes":` + one + `}`},
	}
	for _, e := range edits {
		done := make(chan *http.Response, 1)
		var body string
		go func() {
			w := f.post(e.path, e.body)
			body = w.Body.String()
			done <- w.Result()
		}()
		select {
		case r := <-done:
			if r.StatusCode != http.StatusConflict || !strings.Contains(body, "a move is running") || strings.Contains(body, `"state"`) {
				t.Errorf("%s: %d %s", e.path, r.StatusCode, body)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waits for the move", e.path)
		}
	}
	wantStatus(t, f.get("/api/l/peter/tags"), http.StatusOK)
	unlock()
	wantStatus(t, f.post("/api/l/peter/ratings", `{"rating":5,"hashes":`+one+`}`), http.StatusOK)
	wantStatus(t, f.post("/api/l/peter/trash", `{"hashes":`+one+`}`), http.StatusOK)
}

// The outcome of a transfer job: a cancel counts only when it cut the
// work short; the source-pending message says where to finish.
func TestTransferOutcome(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	j := func(ctx context.Context) *job {
		return &job{ctx: ctx, entry: &libEntry{id: "peter"}, params: jobParams{To: "penny", target: &libEntry{id: "penny"}}}
	}
	for _, c := range []struct {
		name   string
		ctx    context.Context
		err    error
		res    transferResultJSON
		state  string
		code   int
		substr string
	}{
		{"cancelled before the run", ctx, fmt.Errorf("read: %w", context.Canceled), transferResultJSON{}, stateCancelled, 130, ""},
		{"error after a cancel", ctx, errors.New("interrupted"), transferResultJSON{}, stateCancelled, 130, ""},
		{"cut short", ctx, nil, transferResultJSON{Moved: 1, cancelled: true}, stateCancelled, 130, ""},
		{"cancel after the last file", ctx, nil, transferResultJSON{Moved: 2}, stateDone, 0, ""},
		{"failure", live, errors.New("boom"), transferResultJSON{}, stateFailed, 1, "the move failed"},
		{"source pending", live, nil, transferResultJSON{Moved: 2, SourcePending: 2}, stateDone, 2,
			"2 photos were moved to penny, but their originals could not be moved to the trash in peter; finish the move from the Libraries page"},
		{"no space", live, &transfer.NoSpaceError{Need: 2 << 30, Free: 1 << 30}, transferResultJSON{}, stateFailed, 1, "not enough free space in penny"},
	} {
		state, code, msg := transferOutcome(j(c.ctx), c.err, c.res, logger)
		if state != c.state || code != c.code || !strings.Contains(msg, c.substr) {
			t.Errorf("%s: %s %d %q", c.name, state, code, msg)
		}
	}
	r := &job{ctx: live, entry: &libEntry{id: "penny"}, params: jobParams{resume: true}}
	if state, code, msg := transferOutcome(r, nil, transferResultJSON{problems: []string{"x could not be finished"}}, logger); state != stateFailed || code != 1 || msg != "x could not be finished" {
		t.Errorf("resume of nothing: %s %d %q", state, code, msg)
	}
}

// A job cancelled at once ends cancelled (never failed), or done when it
// finished before the cancel arrived.
func TestTransferCancelEarly(t *testing.T) {
	for range 3 {
		f, _, ph := transferWorkspace(t)
		w := f.post("/api/l/peter/jobs", `{"kind":"transfer","to":"penny","hashes":`+hashesJSON(ph["one"], ph["two"])+`}`)
		wantStatus(t, w, http.StatusCreated)
		id := decode[jobJSON](t, w).ID
		f.post(fmt.Sprintf("/api/jobs/%d/cancel", id), "{}")
		j := f.pollJob(id)
		switch {
		case j.State == stateCancelled && j.ExitCode == 130:
		case j.State == stateDone && j.ExitCode == 0 && j.Result.(map[string]any)["moved"] == 2.0:
		default:
			t.Fatalf("job %+v error %q", j, deref(j.Error))
		}
	}
}
