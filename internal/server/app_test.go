package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/library"
)

// appRun is a server started with Start and App hooks that record calls.
type appRun struct {
	t   *testing.T
	run *Running

	mu      sync.Mutex
	opened  int
	quits   []bool
	changed []string
}

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func startApp(t *testing.T, path string) *appRun {
	t.Helper()
	a := &appRun{t: t}
	hooks := &AppHooks{
		Token: testToken, Version: "v-test", Platform: "linux", Window: false,
		CloseToTray: func() bool { return true },
		OnOpen:      func() { a.mu.Lock(); a.opened++; a.mu.Unlock() },
		OnQuit: func(force bool) error {
			a.mu.Lock()
			a.quits = append(a.quits, force)
			a.mu.Unlock()
			return nil
		},
		OnWorkspaceChanged: func(p string) { a.mu.Lock(); a.changed = append(a.changed, p); a.mu.Unlock() },
	}
	run, err := Start(context.Background(), StartOptions{Path: path, Addr: "127.0.0.1:0", AddrGiven: true,
		Version: "v-test", App: hooks, static: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	a.run = run
	t.Cleanup(func() { run.Shutdown(context.Background()) })
	return a
}

// do sends a request the way the second instance does: a plain client,
// no Origin, X-Picmover: 1 and a JSON content type on POSTs.
func (a *appRun) do(method, path, body string) (int, []byte) {
	a.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(a.run.URL(), "/")+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Picmover", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (a *appRun) want(method, path, body string, code int) []byte {
	a.t.Helper()
	got, b := a.do(method, path, body)
	if got != code {
		a.t.Fatalf("%s %s: %d %s, want %d", method, path, got, b, code)
	}
	return b
}

func decodeJSON[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

// lockFree reports whether the library lock at dir can be taken (and
// releases it again).
func lockFree(t *testing.T, dir string) bool {
	t.Helper()
	unlock, err := library.LockFile(filepath.Join(dir, library.MetaDir, library.LockName), "test")
	if err != nil {
		return false
	}
	unlock()
	return true
}

// Start serves a new library like serve, and Shutdown closes it and
// releases its lock.
func TestStartSingleShutdown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lib")
	run, err := Start(context.Background(), StartOptions{Path: dir, Addr: "127.0.0.1:0", AddrGiven: true,
		Version: "v1", static: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(run.URL(), "http://127.0.0.1:") || !strings.Contains(run.Banner(), "(new)") {
		t.Fatalf("url %s banner %s", run.URL(), run.Banner())
	}
	resp, err := http.Get(run.URL() + "api/l/lib/library")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("library %d", resp.StatusCode)
	}
	if lockFree(t, dir) {
		t.Fatal("library not locked while serving")
	}
	if run.JobCount() != 0 || run.Workspace() == "" {
		t.Fatalf("jobs %d workspace %q", run.JobCount(), run.Workspace())
	}
	if err := run.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.Done():
	default:
		t.Fatal("Done not closed after Shutdown")
	}
	if !lockFree(t, dir) {
		t.Fatal("lock held after Shutdown")
	}
	if err := run.Shutdown(context.Background()); err != nil {
		t.Fatal("second Shutdown:", err)
	}
	// /api/app is 404 under serve (no App).
	run, err = Start(context.Background(), StartOptions{Path: dir, Addr: "127.0.0.1:0", AddrGiven: true, static: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Shutdown(context.Background())
	a := &appRun{t: t, run: run}
	a.want(http.MethodGet, "/api/app", "", http.StatusNotFound)
	a.want(http.MethodPost, "/api/app/show", `{"token":"`+testToken+`"}`, http.StatusNotFound)
	a.want(http.MethodPost, "/api/app/quit", `{"token":"`+testToken+`"}`, http.StatusNotFound)
	a.want(http.MethodPost, "/api/app/open", `{}`, http.StatusNotFound)
	a.want(http.MethodPost, "/api/app/workspace", `{"path":"/"}`, http.StatusNotFound)

	// A busy address that was given is an error; one that was not falls
	// back to a free port.
	if _, err := Start(context.Background(), StartOptions{Path: dir, Addr: strings.TrimPrefix(strings.TrimSuffix(run.URL(), "/"), "http://"),
		AddrGiven: true, static: fstest.MapFS{}}); err == nil {
		t.Fatal("busy given address accepted")
	}
}

// --workspace on a library is a ModeError (serve's usage error).
func TestStartModeError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lib")
	newCatalog(t, dir)
	_, err := Start(context.Background(), StartOptions{Path: dir, Workspace: true, Addr: "127.0.0.1:0", static: fstest.MapFS{}})
	var me *ModeError
	if err == nil || !strings.Contains(err.Error(), "is a library") || !errors.As(err, &me) {
		t.Fatalf("err %v", err)
	}
}

// Unset mode: nothing listed, every library route 409 {state: unset},
// the folder browser works.
func TestAppUnsetMode(t *testing.T) {
	a := startApp(t, "")
	ls := decodeJSON[librariesJSON](t, a.want(http.MethodGet, "/api/libraries", "", http.StatusOK))
	if ls.Mode != "unset" || ls.Root != "" || ls.Libraries == nil || len(ls.Libraries) != 0 || ls.Folders == nil || len(ls.Folders) != 0 {
		t.Fatalf("listing %+v", ls)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/l/x/library", ""},
		{http.MethodGet, "/api/l/x/media", ""},
		{http.MethodPost, "/api/l/x/jobs", `{"kind":"thumbs"}`},
		{http.MethodPost, "/api/libraries", `{"name":"x"}`},
		{http.MethodPost, "/api/libraries/x/open", `{}`},
		{http.MethodGet, "/api/libraries/x/cover", ""},
		{http.MethodPost, "/api/libraries/x/jobs", `{"kind":"rebuild"}`},
	} {
		b := a.want(c.method, c.path, c.body, http.StatusConflict)
		if e := decodeJSON[stateError](t, b); e.State != "unset" || e.Error != "no workspace" {
			t.Fatalf("%s: %s", c.path, b)
		}
	}
	a.want(http.MethodGet, "/api/fs?path="+t.TempDir(), "", http.StatusOK)
	a.want(http.MethodGet, "/api/jobs", "", http.StatusOK)
	app := decodeJSON[appJSON](t, a.want(http.MethodGet, "/api/app", "", http.StatusOK))
	if app != (appJSON{App: true, Version: "v-test", Workspace: "", CloseToTray: true, Platform: "linux", Window: false}) {
		t.Fatalf("app %+v", app)
	}
}

// show and quit need the token; quit refuses while a job runs unless
// forced; open needs none. A plain client without Origin passes.
func TestAppTokenEndpoints(t *testing.T) {
	a := startApp(t, "")
	if b := a.want(http.MethodPost, "/api/app/open", `{}`, http.StatusNoContent); len(b) != 0 {
		t.Fatalf("open body %q", b)
	}
	a.want(http.MethodPost, "/api/app/show", `{"token":"wrong"}`, http.StatusForbidden)
	a.want(http.MethodPost, "/api/app/show", `{}`, http.StatusForbidden)
	a.want(http.MethodPost, "/api/app/show", `{"token":"`+testToken+`"}`, http.StatusNoContent)
	a.mu.Lock()
	if a.opened != 2 {
		t.Fatalf("opened %d", a.opened)
	}
	a.mu.Unlock()

	// The usual POST rules apply.
	req, _ := http.NewRequest(http.MethodPost, a.run.URL()+"api/app/show", strings.NewReader(`{"token":"`+testToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no X-Picmover: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, a.run.URL()+"api/app/show", strings.NewReader(`{"token":"`+testToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Picmover", "1")
	req.Header.Set("Origin", "http://evil.example")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}

	a.want(http.MethodPost, "/api/app/quit", `{"token":"wrong"}`, http.StatusForbidden)
	release := fakeRunningJob(a.run.srv)
	b := a.want(http.MethodPost, "/api/app/quit", `{"token":"`+testToken+`"}`, http.StatusConflict)
	if e := decodeJSON[jobsBusyJSON](t, b); e.Jobs != 1 || e.Error == "" {
		t.Fatalf("quit busy %s", b)
	}
	if a.run.JobCount() != 1 {
		t.Fatal("JobCount")
	}
	a.want(http.MethodPost, "/api/app/quit", `{"token":"`+testToken+`","force":true}`, http.StatusNoContent)
	release()
	a.want(http.MethodPost, "/api/app/quit", `{"token":"`+testToken+`"}`, http.StatusNoContent)
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		n := len(a.quits)
		a.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("OnQuit calls %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.quits[0] && !a.quits[1] {
		t.Fatalf("quits %v", a.quits)
	}
}

// fakeRunningJob makes the job manager report a running job until
// release is called.
func fakeRunningJob(s *Server) (release func()) {
	s.jobs.mu.Lock()
	s.jobs.running = &job{id: 999}
	s.jobs.mu.Unlock()
	return func() {
		s.jobs.mu.Lock()
		s.jobs.running = nil
		s.jobs.mu.Unlock()
	}
}

// The workspace switch: validation, 409 during a job, libraries closed
// (locks released) and the new folder served, OnWorkspaceChanged called.
func TestAppWorkspaceSwitch(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "media")
	newCatalog(t, filepath.Join(ws, "peter"))
	newCatalog(t, filepath.Join(ws, "penny"))
	file := filepath.Join(base, "file.txt")
	writeFile(t, file, []byte("x"))
	a := startApp(t, "")

	for path, msg := range map[string]string{
		"":                                      "Choose a folder",
		"relative/dir":                          "absolute path",
		filepath.Join(base, "missing"):          "does not exist",
		file:                                    "not a folder",
		filepath.Join(ws, "peter", ".picmover"): ".picmover",
		"//server/share":                        "Network",
	} {
		b := a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(path)+`}`, http.StatusBadRequest)
		if !strings.Contains(string(b), msg) {
			t.Errorf("%q: %s, want %q", path, b, msg)
		}
	}
	a.want(http.MethodPost, "/api/app/workspace", `{"path":`, http.StatusBadRequest)

	release := fakeRunningJob(a.run.srv)
	b := a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(ws)+`}`, http.StatusConflict)
	if e := decodeJSON[jobsBusyJSON](t, b); e.Jobs != 1 || e.Error != "a job is running" {
		t.Fatalf("busy %s", b)
	}
	release()

	ls := decodeJSON[librariesJSON](t, a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(ws)+`}`, http.StatusOK))
	if ls.Mode != modeWorkspace || ls.Root != ws || len(ls.Libraries) != 2 {
		t.Fatalf("listing %+v", ls)
	}
	app := decodeJSON[appJSON](t, a.want(http.MethodGet, "/api/app", "", http.StatusOK))
	if app.Workspace != ls.Root {
		t.Fatalf("app workspace %q, listing root %q", app.Workspace, ls.Root)
	}
	a.want(http.MethodPost, "/api/libraries/peter/open", `{}`, http.StatusOK)
	a.want(http.MethodGet, "/api/l/peter/library", "", http.StatusOK)
	if lockFree(t, filepath.Join(ws, "peter")) {
		t.Fatal("peter not locked when open")
	}

	// Switch to one library: peter is closed and unlocked, penny served
	// and locked.
	penny := filepath.Join(ws, "penny")
	ls = decodeJSON[librariesJSON](t, a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(penny)+`}`, http.StatusOK))
	if ls.Mode != modeSingle || ls.Root != penny || len(ls.Libraries) != 1 || ls.Libraries[0].State != libOpen {
		t.Fatalf("single listing %+v", ls)
	}
	if !lockFree(t, filepath.Join(ws, "peter")) {
		t.Fatal("peter still locked after the switch")
	}
	if lockFree(t, penny) {
		t.Fatal("penny not locked")
	}
	a.want(http.MethodGet, "/api/l/peter/library", "", http.StatusNotFound)
	a.want(http.MethodGet, "/api/l/penny/library", "", http.StatusOK)

	// Back to the workspace: penny's lock is released.
	a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(ws)+`}`, http.StatusOK)
	if !lockFree(t, penny) {
		t.Fatal("penny still locked")
	}
	a.want(http.MethodGet, "/api/l/penny/library", "", http.StatusConflict) // listed, not open

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.changed) != 3 || a.changed[0] != ws || a.changed[1] != penny || a.changed[2] != ws {
		t.Fatalf("OnWorkspaceChanged %v", a.changed)
	}
}

// A library busy in another process is refused with 409 and the
// previous workspace stays served.
func TestAppWorkspaceSwitchBusyLibrary(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "media")
	newCatalog(t, filepath.Join(ws, "peter"))
	other := newCatalog(t, filepath.Join(base, "other"))
	unlock, err := other.Lock(context.Background(), "import")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	a := startApp(t, ws)
	b := a.want(http.MethodPost, "/api/app/workspace", `{"path":`+jsonString(other.Root())+`}`, http.StatusConflict)
	if !strings.Contains(string(b), "in use") {
		t.Fatalf("busy %s", b)
	}
	ls := decodeJSON[librariesJSON](t, a.want(http.MethodGet, "/api/libraries", "", http.StatusOK))
	if ls.Root != ws || ls.Mode != modeWorkspace {
		t.Fatalf("listing after a failed switch %+v", ls)
	}
	// The app serves an existing folder without media.db as a workspace
	// even when it holds other files.
	photos := filepath.Join(base, "photos")
	writeFile(t, filepath.Join(photos, "a.jpg"), []byte("x"))
	run, err := Start(context.Background(), StartOptions{Path: photos, Addr: "127.0.0.1:0", App: &AppHooks{}, static: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Shutdown(context.Background())
	if run.srv.Mode() != modeWorkspace {
		t.Fatalf("mode %s", run.srv.Mode())
	}
	if _, err := os.Stat(filepath.Join(photos, "media.db")); err == nil {
		t.Fatal("a library was created")
	}
}
