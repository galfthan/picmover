package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/library"
)

// holdEntry counts a request on the open library id (as a streaming
// video would) until the returned release is called.
func holdEntry(t *testing.T, s *Server, id string) (*libHandles, func()) {
	t.Helper()
	e := s.ws.entry(id)
	if e == nil {
		t.Fatalf("no entry %s", id)
	}
	h, state, ok := e.acquire()
	if !ok {
		t.Fatalf("%s not open: %s", id, state)
	}
	return h, h.entry.release
}

// Switching to the single library this process serves while a request
// still uses it hands the library over: no 409, the lock never drops,
// the request's handles keep working, and releasing them closes nothing.
func TestSwitchToHeldSingleLibrary(t *testing.T) {
	lib := newCatalog(t, filepath.Join(t.TempDir(), "lib"))
	a := startApp(t, lib.Root())
	h, release := holdEntry(t, a.run.srv, "lib")
	start := time.Now()
	ls := decodeJSON[librariesJSON](t, a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(lib.Root())+`}`, http.StatusOK))
	if ls.Mode != modeSingle || ls.Root != lib.Root() || len(ls.Libraries) != 1 || ls.Libraries[0].State != libOpen {
		t.Fatalf("listing %+v", ls)
	}
	if time.Since(start) > retireWait/2 {
		t.Fatalf("the switch waited for a handed-over library: %v", time.Since(start))
	}
	if lockFree(t, lib.Root()) {
		t.Fatal("lock dropped during the switch")
	}
	if _, err := h.db.CountSimilarPairs(context.Background()); err != nil {
		t.Fatalf("the request's catalog was closed: %v", err)
	}
	release()
	if lockFree(t, lib.Root()) {
		t.Fatal("releasing the old request released the lock")
	}
	a.want(http.MethodGet, "/api/l/lib/library", "", http.StatusOK)
	a.mu.Lock()
	if len(a.changed) != 1 || a.changed[0] != lib.Root() {
		t.Fatalf("OnWorkspaceChanged %v", a.changed)
	}
	a.mu.Unlock()
	// Shutdown releases the handed-over lock.
	a.run.Shutdown(context.Background())
	if !lockFree(t, lib.Root()) {
		t.Fatal("lock held after Shutdown")
	}
}

// Switching from a workspace to one of its libraries that is open and in
// use hands it over too; the other open library is released once its
// requests end.
func TestSwitchToHeldLibraryInWorkspace(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "media")
	newCatalog(t, filepath.Join(ws, "peter"))
	newCatalog(t, filepath.Join(ws, "penny"))
	a := startApp(t, ws)
	a.want(http.MethodPost, "/api/libraries/peter/open", `{}`, http.StatusOK)
	a.want(http.MethodPost, "/api/libraries/penny/open", `{}`, http.StatusOK)
	h, release := holdEntry(t, a.run.srv, "peter")
	defer release()
	peter := filepath.Join(ws, "peter")
	ls := decodeJSON[librariesJSON](t, a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(peter)+`}`, http.StatusOK))
	if ls.Mode != modeSingle || ls.Root != peter || ls.Libraries[0].State != libOpen {
		t.Fatalf("listing %+v", ls)
	}
	if lockFree(t, peter) {
		t.Fatal("peter's lock dropped")
	}
	if !lockFree(t, filepath.Join(ws, "penny")) {
		t.Fatal("penny still locked after the switch")
	}
	if _, err := h.db.CountSimilarPairs(context.Background()); err != nil {
		t.Fatalf("the request's catalog was closed: %v", err)
	}
	a.want(http.MethodGet, "/api/l/peter/library", "", http.StatusOK)
}

// A switch whose library cannot be opened publishes nothing: the
// previous single library stays served, locked and open, and
// OnWorkspaceChanged is not called.
func TestSwitchFailureKeepsPrevious(t *testing.T) {
	base := t.TempDir()
	lib := newCatalog(t, filepath.Join(base, "lib"))
	bad := filepath.Join(base, "bad")
	writeFile(t, filepath.Join(bad, library.DBName), []byte("this is not a catalog, just some bytes that are long enough"))
	a := startApp(t, lib.Root())
	b := a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(bad)+`}`, http.StatusConflict)
	if !strings.Contains(string(b), "cannot be opened") {
		t.Fatalf("answer %s", b)
	}
	ls := decodeJSON[librariesJSON](t, a.want(http.MethodGet, "/api/libraries", "", http.StatusOK))
	if ls.Mode != modeSingle || ls.Root != lib.Root() || ls.Libraries[0].State != libOpen {
		t.Fatalf("listing after a failed switch %+v", ls)
	}
	if lockFree(t, lib.Root()) {
		t.Fatal("previous library unlocked")
	}
	a.want(http.MethodGet, "/api/l/lib/library", "", http.StatusOK)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.changed) != 0 {
		t.Fatalf("OnWorkspaceChanged %v", a.changed)
	}
}

// An entry captured before a switch is retired: opening it, rebuilding
// it or starting a job on it is refused, and a listing of the previous
// workspace registers nothing.
func TestSwitchRetiredEntryRefused(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "media")
	newCatalog(t, filepath.Join(ws, "peter"))
	other := filepath.Join(base, "other")
	os.MkdirAll(other, 0o755)
	a := startApp(t, ws)
	s := a.run.srv
	l, err := s.ws.find("peter")
	if err != nil {
		t.Fatal(err)
	}
	e := s.ws.entryFor(l)
	a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(other)+`}`, http.StatusOK)

	if err := s.ws.open(context.Background(), e); !errors.Is(err, errRetired) {
		t.Fatalf("open: %v", err)
	}
	if !lockFree(t, filepath.Join(ws, "peter")) {
		t.Fatal("a retired entry took the lock")
	}
	if _, _, err := s.ws.beginRebuild(context.Background(), e); !errors.Is(err, errRetired) {
		t.Fatalf("beginRebuild: %v", err)
	}
	if _, err := s.jobs.start(e, jobParams{Kind: kindRebuild}, time.UTC); !errors.Is(err, errRetired) {
		t.Fatalf("job start: %v", err)
	}
	if s.jobs.count() != 0 {
		t.Fatal("a job started")
	}
	// The listing taken before the switch: a retired entry, not
	// registered in the new workspace.
	if e2 := s.ws.entryFor(l); !e2.retired.Load() || s.ws.entry("peter") != nil {
		t.Fatal("an old listing registered an entry")
	}
	// The open route answers 409 with the reason.
	rec := httptest.NewRecorder()
	s.openFailed(rec, nil, l, e, errRetired)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workspace was changed") {
		t.Fatalf("openFailed %d %s", rec.Code, rec.Body.String())
	}
	// A library cannot be created in the previous workspace.
	if _, err := s.ws.createLibrary(context.Background(), &wsConf{}, "x"); err == nil {
		t.Fatal("created a library without a workspace")
	}
}

// A folder inside a library (any parent has media.db) is refused as a
// workspace, naming the library.
func TestSwitchInsideLibraryRefused(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "media")
	peter := filepath.Join(ws, "peter")
	newCatalog(t, peter)
	deep := filepath.Join(peter, "2024", "06")
	os.MkdirAll(deep, 0o755)
	a := startApp(t, "")
	for _, p := range []string{filepath.Join(peter, "2024"), deep} {
		b := a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(p)+`}`, http.StatusBadRequest)
		if !strings.Contains(string(b), "inside the library "+jsonEscape(peter)) {
			t.Fatalf("%s: %s", p, b)
		}
	}
	// The library itself and the workspace are fine.
	a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(peter)+`}`, http.StatusOK)
	a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(ws)+`}`, http.StatusOK)
}

// jsonEscape is s as it appears inside a JSON string.
func jsonEscape(s string) string {
	q := jsonString(s)
	return q[1 : len(q)-1]
}
