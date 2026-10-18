package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/library"
	"picmover/internal/testimg"
	"picmover/internal/userdata"
)

// noPaths fails when a response body carries an absolute path of the
// test's temp directory.
func noPaths(t *testing.T, base, body string) {
	t.Helper()
	if strings.Contains(body, base) || strings.Contains(body, filepath.ToSlash(base)) {
		t.Errorf("response names an absolute path: %s", body)
	}
}

func setUserDBVersion(t *testing.T, dir string, v int) {
	t.Helper()
	p := filepath.Join(dir, userdata.FileName)
	u, err := userdata.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatal(err)
	}
}

// Review A#1: a failed rebuild must not leave the library locked by the
// server itself; once the cause is fixed a second rebuild succeeds.
func TestRebuildRetryAfterFailure(t *testing.T) {
	f, root := newWorkspace(t)
	bad := filepath.Join(root, "bad")
	if err := os.MkdirAll(filepath.Join(bad, "media.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bad, "pics", "p.jpg"), testimg.JPEG(testimg.Options{Width: 32, Height: 24, Seed: 11}))
	w := f.post("/api/libraries/bad/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateFailed || j.Error == nil || *j.Error != errNotRegularCatalog.Error() {
		t.Fatalf("first rebuild %+v", j)
	}
	noPaths(t, f.base, *j.Error)
	if err := os.Remove(filepath.Join(bad, "media.db")); err != nil {
		t.Fatal(err)
	}
	w = f.post("/api/libraries/bad/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateDone || j.Stats.Existing != 1 {
		t.Fatalf("second rebuild %+v", j)
	}
	if l, _ := libByID(f.libraries(), "bad"); l.State != libOpen || l.Summary.Total != 1 {
		t.Fatalf("bad after rebuild %+v", l)
	}
	// A third one (on the open library, lock held) works too.
	w = f.post("/api/l/bad/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateDone {
		t.Fatalf("third rebuild %+v", j)
	}
}

// Review A#1, single mode: the lock belongs to cmd/serve; an open after a
// failed rebuild reuses it instead of colliding with it.
func TestSingleModeOpenAfterErrorReusesLock(t *testing.T) {
	var unlock func()
	f := newFixtureWith(t, func(o *Options) {
		u, err := o.Library.Lock(context.Background(), "serve")
		if err != nil {
			t.Fatal(err)
		}
		unlock = u
	})
	t.Cleanup(func() { unlock() })
	e := f.srv.ws.entry("lib")
	e.mu.Lock()
	e.closeCatalogLocked()
	e.state, e.errMsg = libError, "rebuild failed"
	e.mu.Unlock()
	w := f.post("/api/libraries/lib/open", ``)
	wantStatus(t, w, http.StatusOK)
	if l := decode[libJSON](t, w); l.State != libOpen {
		t.Fatalf("open %+v", l)
	}
}

// Review A#2: a library folder that links out of the library is refused
// by trash as it is by /file; the row stays live.
func TestTrashRefusesLinkedFolder(t *testing.T) {
	f := newFixture(t)
	m, _ := f.db.GetByHash(context.Background(), f.hashes["c.jpg"])
	dir := filepath.Dir(f.lib.Abs(m.RelPath))
	ext := filepath.Join(f.base, "ext")
	if err := os.Rename(dir, ext); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ext, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	wantStatus(t, f.get(lp+"file/"+itoa64(m.ID)), http.StatusNotFound)
	w := f.post(lp+"trash", `{"hashes":["`+hexHash(m.Hash)+`"]}`)
	d := decode[trashResultJSON](t, w)
	if d.Trashed != 0 || len(d.Failed) != 1 || d.Failed[0].Error != "the file's catalog path is not a library path" {
		t.Fatalf("trash %s", w.Body)
	}
	if _, err := os.Stat(filepath.Join(ext, filepath.Base(m.RelPath))); err != nil {
		t.Fatal("outside file moved")
	}
	// A row whose path is now a directory is refused too.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.lib.Abs(m.RelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	w = f.post(lp+"trash", `{"hashes":["`+hexHash(m.Hash)+`"]}`)
	if d := decode[trashResultJSON](t, w); d.Trashed != 0 || len(d.Failed) != 1 ||
		d.Failed[0].Error != "the path is a folder or a link, not a file" {
		t.Fatalf("trash of a directory %s", w.Body)
	}
}

// Review A#3: an import source that is, contains or lies inside a
// library of the workspace (or is the workspace root) is refused.
func TestImportSourceOverlappingLibraries(t *testing.T) {
	f, root := newWorkspace(t)
	wantStatus(t, f.post("/api/libraries/peter/open", ``), http.StatusOK)
	writeFile(t, filepath.Join(root, "penny", "image", "x.jpg"), testimg.JPEG(testimg.Options{Width: 32, Height: 24, Seed: 12}))
	for _, src := range []string{
		root,
		filepath.Join(root, "penny"),
		filepath.Join(root, "penny", "image"),
		filepath.Join(root, "peter"),
		f.base,
	} {
		w := f.post("/api/l/peter/jobs", `{"kind":"import","source":`+jsonString(src)+`,"move":true,"noThumbs":true}`)
		wantError(t, w, http.StatusBadRequest, "library")
	}
	w := f.post("/api/l/peter/jobs", `{"kind":"import","source":`+jsonString(filepath.Join(root, "plain"))+`,"noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateDone || j.Stats.Imported != 1 {
		t.Fatalf("import from a plain folder %+v", j)
	}
	if _, err := os.Stat(filepath.Join(root, "penny", "image", "x.jpg")); err != nil {
		t.Fatal("penny's file touched")
	}
}

// Review A#4: a library folder replaced while open is detected before a
// mutation: 409 state error, and opening again locks the new folder.
func TestReplacedFolderDetected(t *testing.T) {
	f, root := newWorkspace(t)
	wantStatus(t, f.post("/api/libraries/peter/open", ``), http.StatusOK)
	if err := os.Rename(filepath.Join(root, "peter"), filepath.Join(root, "peter-old")); err != nil {
		t.Fatal(err)
	}
	newCatalog(t, filepath.Join(root, "peter"))
	w := f.post("/api/l/peter/trash", `{"hashes":["0000000000c0ffee"]}`)
	if v := decodeAny(t, w); w.Code != http.StatusConflict || v["state"] != libError {
		t.Fatalf("trash on a replaced folder: %d %v", w.Code, v)
	}
	if w := f.post("/api/l/peter/jobs", `{"kind":"thumbs"}`); w.Code != http.StatusConflict {
		t.Fatalf("job on a replaced folder: %d %s", w.Code, w.Body)
	}
	// The old folder's lock is released; the new folder opens and locks.
	old, _ := library.Attach(filepath.Join(root, "peter-old"))
	if u, err := old.Lock(context.Background(), "test"); err != nil {
		t.Fatalf("old lock still held: %v", err)
	} else {
		u()
	}
	wantStatus(t, f.post("/api/libraries/peter/open", ``), http.StatusOK)
	nl, _ := library.Attach(filepath.Join(root, "peter"))
	var locked *library.ErrLocked
	if _, err := nl.Lock(context.Background(), "test"); !errors.As(err, &locked) {
		t.Fatalf("new folder not locked: %v", err)
	}
}

// Review A#4: creating a library whose old folder was deleted while open
// gets a fresh entry, never the stale handles.
func TestCreateAfterDeleteFreshEntry(t *testing.T) {
	f, root := newWorkspace(t)
	wantStatus(t, f.post("/api/libraries/penny/open", ``), http.StatusOK)
	old := f.srv.ws.entry("penny")
	if err := os.RemoveAll(filepath.Join(root, "penny")); err != nil {
		t.Fatal(err)
	}
	w := f.post("/api/libraries", `{"name":"penny"}`)
	wantStatus(t, w, http.StatusCreated)
	if e := f.srv.ws.entry("penny"); e == old || e.state != libOpen {
		t.Fatal("old entry reused")
	}
	old.mu.Lock()
	defer old.mu.Unlock()
	if old.db != nil || old.unlock != nil {
		t.Fatal("old entry's handles still open")
	}
}

// Review A#5: a rebuild waits for a running trash operation (the trash
// lock) before it closes the catalog.
func TestRebuildWaitsForTrashOps(t *testing.T) {
	f := newFixture(t)
	e := f.srv.ws.entry("lib")
	e.trashMu.Lock()
	w := f.post(lp+"jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	id := decode[jobJSON](t, w).ID
	time.Sleep(200 * time.Millisecond)
	e.mu.Lock()
	dbOpen := e.db != nil
	e.mu.Unlock()
	if !dbOpen {
		t.Fatal("the catalog was closed while a trash operation held the lock")
	}
	e.trashMu.Unlock()
	if j := f.pollJob(id); j.State != stateDone {
		t.Fatalf("rebuild %+v", j)
	}
}

// Review A#6: a failed move answers a fixed text, without paths or OS
// error text.
func TestTrashErrorHasNoPaths(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions")
	}
	f := newFixture(t)
	m, _ := f.db.GetByHash(context.Background(), f.hashes["e.jpg"])
	dir := filepath.Dir(f.lib.Abs(m.RelPath))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	w := f.post(lp+"trash", `{"hashes":["`+hexHash(m.Hash)+`"]}`)
	d := decode[trashResultJSON](t, w)
	if len(d.Failed) != 1 || d.Failed[0].Error != "permission denied" {
		t.Fatalf("trash %s", w.Body)
	}
	noPaths(t, f.base, w.Body.String())
}

// Review A#7: links into another library or its .picmover are not listed,
// and nothing is created through them.
func TestWorkspaceLinksIntoLibrariesSkipped(t *testing.T) {
	f, root := newWorkspace(t)
	if err := os.Symlink(filepath.Join(root, "penny", ".picmover"), filepath.Join(root, "cachelink")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "peter"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	ls := f.libraries()
	for _, id := range []string{"cachelink", "alias"} {
		if _, ok := libByID(ls, id); ok {
			t.Errorf("%s listed as a library", id)
		}
		for _, fo := range ls.Folders {
			if fo.ID == id {
				t.Errorf("%s listed as a folder", id)
			}
		}
		wantStatus(t, f.post("/api/libraries/"+id+"/jobs", `{"kind":"rebuild","noThumbs":true}`), http.StatusNotFound)
	}
	if _, err := os.Stat(filepath.Join(root, "penny", ".picmover", "media.db")); err == nil {
		t.Fatal("catalog created inside penny's .picmover")
	}
	// A link to a plain folder is listed, but a rebuild through it is
	// refused: a library is never created through a link.
	w := f.post("/api/libraries/inner/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateFailed || j.Error == nil || *j.Error != errViaSymlink.Error() {
		t.Fatalf("rebuild through a link %+v", j)
	}
	if _, err := os.Stat(filepath.Join(root, "plain", "media.db")); err == nil {
		t.Fatal("library created through the link")
	}
}

// Review A#8: failed peeks (a cancelled request) are never cached.
func TestPeekErrorsNotCached(t *testing.T) {
	f, root := newWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dbPath := filepath.Join(root, "peter", library.DBName)
	if _, err := f.srv.ws.peek(ctx, dbPath); err == nil {
		t.Skip("the peek did not see the cancellation")
	}
	if l, _ := libByID(f.libraries(), "peter"); l.State != libClosed || l.Summary == nil {
		t.Fatalf("peter after a cancelled peek %+v", l)
	}
}

// Review A#10: a user.db this picmover cannot open stops a rebuild before
// the backup; the catalog stays.
func TestRebuildJobUnreadableUserDB(t *testing.T) {
	f, root := newWorkspace(t)
	setUserDBVersion(t, filepath.Join(root, "peter"), 5)
	w := f.post("/api/libraries/peter/jobs", `{"kind":"rebuild","noThumbs":true}`)
	if w.Code == http.StatusCreated {
		j := f.pollJob(decode[jobJSON](t, w).ID)
		if j.State != stateFailed || j.Error == nil || !strings.Contains(*j.Error, "schema version 5") {
			t.Fatalf("rebuild %+v", j)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "peter", library.DBName)); err != nil {
		t.Fatal("catalog renamed away")
	}
	if baks, _ := filepath.Glob(filepath.Join(root, "peter", "media.db.bak-*")); len(baks) != 0 {
		t.Fatalf("backup made: %v", baks)
	}
}

// Review A#24: a user.db with another schema version lists the library
// as error, with the open's message.
func TestListingUserDBVersion(t *testing.T) {
	f, root := newWorkspace(t)
	setUserDBVersion(t, filepath.Join(root, "penny"), 5)
	l, _ := libByID(f.libraries(), "penny")
	want := "user.db has schema version 5; this picmover supports 4: upgrade picmover"
	if l.State != libError || l.Error != want {
		t.Fatalf("penny %+v", l)
	}
	w := f.post("/api/libraries/penny/open", ``)
	if v := decodeAny(t, w); w.Code != http.StatusConflict || v["error"] != want || v["state"] != libError {
		t.Fatalf("open %d %v", w.Code, v)
	}
}

// Review B#5/#16: a rebuild cancelled during the record stage restores
// the previous catalog instead of making the partial one live.
func TestRebuildCancelledRestoresCatalog(t *testing.T) {
	b := newBlocker()
	f := newFixtureWith(t, b.adjust)
	before := f.countAll("")
	w := f.post(lp+"jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	id := decode[jobJSON](t, w).ID
	b.wait(t)
	wantStatus(t, f.post(fmt.Sprintf("/api/jobs/%d/cancel", id), ``), http.StatusAccepted)
	close(b.release)
	j := f.pollJob(id)
	if j.State != stateCancelled || j.Error == nil || !strings.Contains(*j.Error, "previous catalog was restored") {
		t.Fatalf("cancelled rebuild %+v", j)
	}
	wantStatus(t, f.get(lp+"library"), http.StatusOK)
	if n := f.countAll(""); n != before {
		t.Fatalf("rows %d after the rollback, want %d", n, before)
	}
	if baks, _ := filepath.Glob(filepath.Join(f.lib.Root(), "media.db.bak-*")); len(baks) != 0 {
		t.Fatalf("backup left: %v", baks)
	}
}

// Review B#16: a rebuild that fails before its new catalog is complete
// restores the backup; on a folder it removes the incomplete catalog.
func TestRebuildFailureRestoresOrRemoves(t *testing.T) {
	f, root := newWorkspaceWith(t, func(o *Options) {
		o.jobOptions = func(io *importer.Options) {
			if io.RecordInPlace {
				io.Source = filepath.Join(io.Source, "no-such-dir") // fatal before recording
			}
		}
	})
	wantStatus(t, f.post("/api/libraries/peter/open", ``), http.StatusOK)
	w := f.post("/api/libraries/peter/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateFailed || j.Error == nil || !strings.Contains(*j.Error, "previous catalog was restored") {
		t.Fatalf("failed rebuild %+v", j)
	}
	noPaths(t, f.base, *j.Error)
	if l, _ := libByID(f.libraries(), "peter"); l.State != libOpen || l.Summary == nil || l.Summary.Total != 1 {
		t.Fatalf("peter after the rollback %+v", l)
	}
	w = f.post("/api/libraries/plain/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateFailed || !strings.Contains(*j.Error, "incomplete catalog was removed") {
		t.Fatalf("failed folder rebuild %+v", j)
	}
	if _, err := os.Lstat(filepath.Join(root, "plain", library.DBName)); !os.IsNotExist(err) {
		t.Fatalf("media.db left in the folder: %v", err)
	}
	ls := f.libraries()
	found := false
	for _, fo := range ls.Folders {
		found = found || fo.ID == "plain"
	}
	if !found {
		t.Fatalf("plain is not a folder again: %+v", ls)
	}
}

// Review A#11: an import of content that is in the trash restores it.
func TestImportJobRestoresTrashed(t *testing.T) {
	f := newFixture(t)
	a := hexHash(f.hashes["a.jpg"])
	f.post(lp+"trash", `{"hashes":["`+a+`"]}`)
	w := f.post(lp+"jobs", `{"kind":"import","source":`+jsonString(filepath.Join(f.base, "src"))+`,"noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateDone || j.Stats.Imported != 1 {
		t.Fatalf("import %+v", j)
	}
	m, _ := f.srv.ws.entry("lib").db.GetByHash(context.Background(), f.hashes["a.jpg"])
	if !m.DeletedAt.IsZero() {
		t.Fatal("row still trashed")
	}
	if _, err := os.Stat(f.lib.Abs(m.RelPath)); err != nil {
		t.Fatal("file not restored")
	}
	if at, _ := f.srv.ws.entry("lib").user.TrashedAt(context.Background(), []uint64{f.hashes["a.jpg"]}); len(at) != 0 {
		t.Fatal("user.db trash row kept")
	}
}

// Review A#12: user.db and catalog names are refused case-insensitively.
func TestValidRelPathDatabases(t *testing.T) {
	for _, bad := range []string{"user.db", "USER.DB", "User.db-wal", "MEDIA.DB", ".PicMover/x.jpg"} {
		if validRelPath(bad) {
			t.Errorf("validRelPath(%q) = true", bad)
		}
	}
}

// Review B#3/#15: a request in flight when the server shuts down
// finishes against open handles, under the lock; the libraries close
// only after the HTTP server drained.
func TestShutdownDrainsInFlightRequests(t *testing.T) {
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	f := newFixtureWith(t, func(o *Options) { o.Addr = addr })
	ts.Config.Handler = f.srv.Handler()
	ts.Start()
	defer ts.Close()

	e := f.srv.ws.entry("lib")
	e.trashMu.Lock() // the trash request blocks inside its handler
	type result struct {
		code int
		body string
	}
	done := make(chan result, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+lp+"trash",
			strings.NewReader(`{"hashes":["`+hexHash(f.hashes["b.jpg"])+`"]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Picmover", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- result{0, err.Error()}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- result{resp.StatusCode, string(b)}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		n := e.inflight
		e.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The cmd/serve order: jobs, HTTP drain, libraries.
	shut := make(chan struct{})
	go func() {
		defer close(shut)
		if err := f.srv.StopJobs(context.Background()); err != nil {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ts.Config.Shutdown(ctx)
		f.srv.Close()
	}()
	time.Sleep(100 * time.Millisecond)
	e.mu.Lock()
	open := e.db != nil
	e.mu.Unlock()
	if !open {
		t.Fatal("libraries closed before the HTTP drain")
	}
	e.trashMu.Unlock()
	r := <-done
	if r.code != http.StatusOK || !strings.Contains(r.body, `"trashed":1`) {
		t.Fatalf("in-flight trash: %d %s", r.code, r.body)
	}
	<-shut
}

// Review B#18: a warm listing re-stats and does not peek again.
func TestListingWarmIsCached(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	for i := range 10 {
		rows := make([]catalog.Media, 0, 200)
		for k := range 200 {
			rows = append(rows, catalog.Media{Hash: uint64(i*1000 + k + 1), Size: 1, FileType: "image", Ext: "jpg",
				RelPath: fmt.Sprintf("image/2020/01/%d.jpg", k), TakenAt: wall(2020, 1, 1, 0, 0, k%60)})
		}
		newCatalog(t, filepath.Join(root, fmt.Sprintf("lib%02d", i)), rows...)
	}
	srv, err := New(Options{Workspace: root, Addr: testAddr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	f := &fixture{t: t, base: base, srv: srv, addr: testAddr}
	if ls := f.libraries(); len(ls.Libraries) != 10 || ls.Libraries[3].Summary.Total != 200 {
		t.Fatalf("listing %+v", ls)
	}
	cached := len(srv.ws.peeks)
	start := time.Now()
	f.libraries()
	el := time.Since(start)
	t.Logf("warm listing of 10 libraries: %v", el)
	if cached != 10 || len(srv.ws.peeks) != 10 {
		t.Fatalf("peek cache has %d entries", len(srv.ws.peeks))
	}
	if el > 500*time.Millisecond {
		t.Errorf("warm listing took %v", el)
	}
}

// Review B#19: a library in a read-only folder is listed with its summary
// (immutable peek) and opening it answers 409 "the folder is read-only".
func TestReadOnlyLibraryFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions")
	}
	f, root := newWorkspace(t)
	dir := filepath.Join(root, "peter")
	for _, d := range []string{filepath.Join(dir, library.MetaDir), dir} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		os.Chmod(dir, 0o755)
		os.Chmod(filepath.Join(dir, library.MetaDir), 0o755)
	})
	if l, _ := libByID(f.libraries(), "peter"); l.State != libClosed || l.Summary == nil || l.Summary.Total != 1 {
		t.Fatalf("peter listed as %+v", l)
	}
	w := f.post("/api/libraries/peter/open", ``)
	if v := decodeAny(t, w); w.Code != http.StatusConflict || v["state"] != libError ||
		!strings.HasPrefix(fmt.Sprint(v["error"]), "the folder is read-only") {
		t.Fatalf("open %d %v", w.Code, v)
	}
	noPaths(t, f.base, w.Body.String())
}

// Review B#21: "Empty trash" also removes trash files without a row.
func TestEmptyTrashRemovesOrphans(t *testing.T) {
	f := newFixture(t)
	orphan := f.lib.TrashPath("image/2020/01/orphan.jpg")
	writeFile(t, orphan, []byte("orphan"))
	a := hexHash(f.hashes["a.jpg"])
	f.post(lp+"trash", `{"hashes":["`+a+`"]}`)
	trashed := f.countAll("trashed=1")
	w := f.post(lp+"trash/delete", `{"all":true,"confirm":true}`)
	d := decode[deleteResultJSON](t, w)
	if d.Deleted != int(trashed)+1 || len(d.Failed) != 0 {
		t.Fatalf("empty trash %s (trashed rows %d)", w.Body, trashed)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan kept")
	}
}
