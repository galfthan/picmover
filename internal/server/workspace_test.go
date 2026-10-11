package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/testimg"
	"picmover/internal/thumbs"
)

// newCatalog creates a library at dir with an empty catalog and the given
// rows.
func newCatalog(t *testing.T, dir string, rows ...catalog.Media) *library.Library {
	t.Helper()
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(context.Background(), lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := range rows {
		if _, err := db.InsertMedia(context.Background(), nil, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	return lib
}

// newWorkspace builds a workspace:
//
//	peter/   library, one row with a thumbnail (the cover)
//	penny/   library, empty
//	broken/  media.db that is not a database
//	plain/   a folder with a photo
//	inner -> plain (symlink inside the root: listed)
//	ext   -> a library outside the root (skipped)
//	.hidden/ a library (skipped)
//	notes.txt (skipped)
func newWorkspace(t *testing.T) (*fixture, string) { return newWorkspaceWith(t, nil) }

// newWorkspaceWith is newWorkspace with adjusted server options.
func newWorkspaceWith(t *testing.T, adjust func(*Options)) (*fixture, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "media")
	thumbAt := time.Unix(1700000000, 0)
	peter := newCatalog(t, filepath.Join(root, "peter"), catalog.Media{Hash: 0xc0ffee, Size: 10, RelPath: "image/2024/01/a.jpg",
		FileType: "image", Ext: "jpg", TakenAt: wall(2024, 1, 2, 3, 4, 5), ThumbState: "ok", ThumbAt: thumbAt})
	st, err := thumbs.OpenStore(peter)
	if err != nil {
		t.Fatal(err)
	}
	thumb := testimg.JPEG(testimg.Options{Width: 32, Height: 24, Seed: 3})
	if err := st.Put(context.Background(), 0xc0ffee, thumb, 32, 24, nil, thumb); err != nil {
		t.Fatal(err)
	}
	st.Close()
	newCatalog(t, filepath.Join(root, "penny"))
	writeFile(t, filepath.Join(root, "broken", "media.db"), bytes.Repeat([]byte("not a database "), 100))
	writeFile(t, filepath.Join(root, "plain", "pic.jpg"), testimg.JPEG(testimg.Options{Width: 32, Height: 24, Seed: 4,
		DateTime: wall(2020, 5, 6, 7, 8, 9)}))
	newCatalog(t, filepath.Join(root, ".hidden"))
	newCatalog(t, filepath.Join(base, "outside"))
	writeFile(t, filepath.Join(root, "notes.txt"), []byte("x"))
	if err := os.Symlink(filepath.Join(root, "plain"), filepath.Join(root, "inner")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(root, "ext")); err != nil {
		t.Fatal(err)
	}
	opts := Options{Workspace: root, Addr: testAddr, Static: fstest.MapFS{}}
	if adjust != nil {
		adjust(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Shutdown(context.Background()) })
	return &fixture{t: t, base: base, srv: srv, addr: testAddr}, root
}

func (f *fixture) libraries() librariesJSON {
	f.t.Helper()
	w := f.get("/api/libraries")
	wantStatus(f.t, w, http.StatusOK)
	return decode[librariesJSON](f.t, w)
}

func libByID(ls librariesJSON, id string) (libJSON, bool) {
	for _, l := range ls.Libraries {
		if l.ID == id {
			return l, true
		}
	}
	return libJSON{}, false
}

func TestWorkspaceDiscovery(t *testing.T) {
	f, root := newWorkspace(t)
	ls := f.libraries()
	if ls.Mode != modeWorkspace || ls.Root != root {
		t.Fatalf("listing %+v", ls)
	}
	var ids []string
	for _, l := range ls.Libraries {
		ids = append(ids, l.ID+":"+l.State)
	}
	if got := strings.Join(ids, " "); got != "broken:error penny:closed peter:closed" {
		t.Fatalf("libraries %s", got)
	}
	if len(ls.Folders) != 2 || ls.Folders[0].Name != "inner" || ls.Folders[1].Name != "plain" ||
		ls.Folders[1].Path != filepath.Join(root, "plain") || ls.Folders[0].State != libFolder {
		t.Fatalf("folders %+v", ls.Folders)
	}
	peter, _ := libByID(ls, "peter")
	if peter.Summary == nil || peter.Summary.Total != 1 || peter.Cover == nil || *peter.Cover != hexHash(0xc0ffee) ||
		peter.Path != filepath.Join(root, "peter") || peter.Name != "peter" {
		t.Fatalf("peter %+v", peter)
	}
	broken, _ := libByID(ls, "broken")
	if broken.Error == "" || broken.Summary != nil {
		t.Fatalf("broken %+v", broken)
	}

	// Cover thumbnails for unopened libraries.
	w := f.get("/api/libraries/peter/cover")
	wantStatus(t, w, http.StatusOK)
	if w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Cache-Control") != "private, max-age=3600" ||
		w.Header().Get("ETag") != fmt.Sprintf(`"%016x-%d-1700000000"`, 0xc0ffee, thumbs.FormatVersion) {
		t.Fatalf("cover headers %v", w.Header())
	}
	for _, id := range []string{"penny", "broken", "plain", "nope", "%2e%2e", ".hidden", "ext"} {
		if w := f.get("/api/libraries/" + id + "/cover"); w.Code != http.StatusNotFound {
			t.Errorf("cover of %s: %d", id, w.Code)
		}
	}
}

func TestWorkspaceIDValidation(t *testing.T) {
	f, _ := newWorkspace(t)
	// A literal ".." is cleaned by the mux (a redirect to an unprefixed
	// path, which is 404); the encoded form reaches the id check.
	for _, id := range []string{"nope", "%2e%2e", ".hidden", "ext", "a%2Fb", "a%5Cb", "a:b", "notes.txt",
		"%00", strings.Repeat("x", 256)} {
		for _, p := range []string{"/api/l/" + id + "/library", "/api/l/" + id + "/timeline", "/api/l/" + id + "/media/1",
			"/api/l/" + id + "/thumb/0000000000000001", "/api/l/" + id + "/tags", "/api/l/" + id + "/similar"} {
			if w := f.get(p); w.Code != http.StatusNotFound {
				t.Errorf("GET %s: %d %s", p, w.Code, w.Body)
			}
		}
		for _, p := range []string{"/api/libraries/" + id + "/open", "/api/libraries/" + id + "/jobs", "/api/l/" + id + "/ratings",
			"/api/l/" + id + "/trash"} {
			if w := f.post(p, `{"kind":"rebuild"}`); w.Code != http.StatusNotFound {
				t.Errorf("POST %s: %d %s", p, w.Code, w.Body)
			}
		}
	}
	if validLibID("a/b") || validLibID(".x") || validLibID("") || !validLibID("Peter's photos") {
		t.Error("validLibID")
	}
}

func TestWorkspaceOpenStates(t *testing.T) {
	f, root := newWorkspace(t)

	// Not open: 409 with the state; the client opens first.
	w := f.get("/api/l/peter/library")
	if w.Code != http.StatusConflict {
		t.Fatalf("closed: %d", w.Code)
	}
	if v := decodeAny(t, w); v["state"] != libClosed || v["error"] != "library is not open" {
		t.Fatalf("closed body %v", v)
	}
	w = f.post("/api/libraries/peter/open", `{}`)
	wantStatus(t, w, http.StatusOK)
	if l := decode[libJSON](t, w); l.State != libOpen || l.Summary.Total != 1 {
		t.Fatalf("opened %+v", l)
	}
	wantStatus(t, f.post("/api/libraries/peter/open", `{}`), http.StatusOK) // idempotent
	wantStatus(t, f.get("/api/l/peter/library"), http.StatusOK)
	if _, err := os.Stat(filepath.Join(root, "peter", "user.db")); err != nil {
		t.Fatalf("user.db not created: %v", err)
	}
	// Opening takes the lock.
	lib, _ := library.Attach(filepath.Join(root, "peter"))
	if _, err := lib.Lock(context.Background(), "test"); err == nil {
		t.Fatal("opened library not locked")
	}

	// Busy: another holder.
	penny, _ := library.Attach(filepath.Join(root, "penny"))
	unlock, err := penny.Lock(context.Background(), "import")
	if err != nil {
		t.Fatal(err)
	}
	w = f.post("/api/libraries/penny/open", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("busy: %d %s", w.Code, w.Body)
	}
	busy := decode[openBusyJSON](t, w)
	if busy.State != libBusy || busy.Holder == nil || busy.Holder.PID != os.Getpid() || busy.Holder.Cmd != "import" ||
		busy.Holder.Since == nil {
		t.Fatalf("busy body %s", w.Body)
	}
	if _, err := time.Parse(wallClockLayout, *busy.Holder.Since); err != nil {
		t.Fatalf("since %q", *busy.Holder.Since)
	}
	if l, _ := libByID(f.libraries(), "penny"); l.State != libBusy || l.Holder == nil {
		t.Fatalf("busy listing %+v", l)
	}
	unlock()
	wantStatus(t, f.post("/api/libraries/penny/open", `{}`), http.StatusOK)

	// Error and folder.
	w = f.post("/api/libraries/broken/open", `{}`)
	if v := decodeAny(t, w); w.Code != http.StatusConflict || v["state"] != libError {
		t.Fatalf("broken: %d %v", w.Code, v)
	}
	if _, err := os.Stat(filepath.Join(root, "broken", ".picmover", "lock")); err == nil {
		lb, _ := library.Attach(filepath.Join(root, "broken"))
		if u, err := lb.Lock(context.Background(), "test"); err != nil {
			t.Fatalf("failed open kept the lock: %v", err)
		} else {
			u()
		}
	}
	w = f.post("/api/libraries/plain/open", `{}`)
	if v := decodeAny(t, w); w.Code != http.StatusConflict || v["state"] != libFolder {
		t.Fatalf("folder: %d %v", w.Code, v)
	}
	if v := decodeAny(t, f.get("/api/l/plain/library")); v["state"] != libFolder {
		t.Fatalf("scoped folder %v", v)
	}

	// Shutdown releases every lock.
	f.srv.Shutdown(context.Background())
	u, err := lib.Lock(context.Background(), "test")
	if err != nil {
		t.Fatalf("lock not released at shutdown: %v", err)
	}
	u()
	wantError(t, f.get("/api/l/peter/library"), http.StatusServiceUnavailable, "shutting down")
}

func TestWorkspaceCreate(t *testing.T) {
	f, root := newWorkspace(t)
	w := f.post("/api/libraries", `{"name":"carol"}`)
	wantStatus(t, w, http.StatusCreated)
	if l := decode[libJSON](t, w); l.ID != "carol" || l.State != libOpen || l.Summary.Total != 0 {
		t.Fatalf("created %+v", l)
	}
	if _, err := os.Stat(filepath.Join(root, "carol", "media.db")); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.get("/api/l/carol/timeline"), http.StatusOK)
	wantError(t, f.post("/api/libraries", `{"name":"carol"}`), http.StatusConflict, "exists")
	wantError(t, f.post("/api/libraries", `{"name":"plain"}`), http.StatusConflict, "exists")
	for _, name := range []string{"", ".x", "..", "a/b", `a\b`, "a:b", "CON", "nul.txt", "Com1", "lpt9.jpg", "x.", "x ", "a?b",
		"a\x01b", strings.Repeat("x", 256)} {
		body, _ := json.Marshal(map[string]string{"name": name})
		wantError(t, f.post("/api/libraries", string(body)), http.StatusBadRequest, "invalid name")
	}
	for _, ok := range []string{"COM10", "console", "Aux-photos"} {
		if reservedName(ok) {
			t.Errorf("%s reserved", ok)
		}
	}
	wantError(t, f.post("/api/libraries", `{"name":`), http.StatusBadRequest, "invalid JSON")
}

func TestWorkspacePeekCache(t *testing.T) {
	f, root := newWorkspace(t)
	dbPath := filepath.Join(root, "penny", "media.db")
	if l, _ := libByID(f.libraries(), "penny"); l.Summary.Total != 0 {
		t.Fatalf("penny %+v", l)
	}
	fi, _ := os.Stat(dbPath)
	db, err := catalog.OpenExisting(context.Background(), dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.InsertMedia(context.Background(), nil, &catalog.Media{Hash: 1, Size: 1, RelPath: "a.jpg", FileType: "image", Ext: "jpg"})
	db.Close()
	if fi2, _ := os.Stat(dbPath); fi2.Size() != fi.Size() {
		t.Skip("the insert changed the file size")
	}
	os.Chtimes(dbPath, fi.ModTime(), fi.ModTime())
	// Unchanged size and mtime: the cached peek, with no expiry.
	for range 2 {
		if l, _ := libByID(f.libraries(), "penny"); l.Summary.Total != 0 {
			t.Fatalf("not cached: %+v", l.Summary)
		}
	}
	// A changed mtime refreshes at once.
	db, _ = catalog.OpenExisting(context.Background(), dbPath, nil)
	db.InsertMedia(context.Background(), nil, &catalog.Media{Hash: 2, Size: 1, RelPath: "b.jpg", FileType: "image", Ext: "jpg"})
	db.Close()
	os.Chtimes(dbPath, fi.ModTime().Add(time.Hour), fi.ModTime().Add(time.Hour))
	if l, _ := libByID(f.libraries(), "penny"); l.Summary.Total != 2 {
		t.Fatalf("mtime change not seen: %+v", l.Summary)
	}
}

func TestWorkspaceRebuildFolder(t *testing.T) {
	f, root := newWorkspace(t)
	// Only a rebuild runs on a folder.
	w := f.post("/api/libraries/plain/jobs", `{"kind":"thumbs"}`)
	if v := decodeAny(t, w); w.Code != http.StatusConflict || v["state"] != libFolder {
		t.Fatalf("thumbs on a folder: %d %v", w.Code, v)
	}
	w = f.post("/api/libraries/plain/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	j := f.pollJob(decode[jobJSON](t, w).ID)
	if j.State != stateDone || j.Library != "plain" || j.Stats.Existing != 1 {
		t.Fatalf("rebuild %+v", j)
	}
	if l, ok := libByID(f.libraries(), "plain"); !ok || l.State != libOpen || l.Summary.Total != 1 {
		t.Fatalf("plain after rebuild %+v", l)
	}
	wantStatus(t, f.get("/api/l/plain/library"), http.StatusOK)
	if _, err := os.Stat(filepath.Join(root, "plain", "pic.jpg")); err != nil {
		t.Fatal("rebuild moved the file")
	}
	// The broken catalog is rebuilt too (backed up first).
	w = f.post("/api/libraries/broken/jobs", `{"kind":"rebuild","noThumbs":true}`)
	wantStatus(t, w, http.StatusCreated)
	if j := f.pollJob(decode[jobJSON](t, w).ID); j.State != stateDone {
		t.Fatalf("rebuild broken %+v", j)
	}
	if l, _ := libByID(f.libraries(), "broken"); l.State != libOpen {
		t.Fatalf("broken after rebuild %+v", l)
	}
	baks, _ := filepath.Glob(filepath.Join(root, "broken", "media.db.bak-*"))
	if len(baks) != 1 {
		t.Fatalf("backups %v", baks)
	}
	// GET /api/jobs lists the jobs of every library, with their library.
	jobs := decode[jobListJSON](t, f.get("/api/jobs")).Jobs
	if len(jobs) != 2 || jobs[0].Library != "broken" || jobs[1].Library != "plain" {
		t.Fatalf("jobs %+v", jobs)
	}
	if js := decode[jobListJSON](t, f.get("/api/l/plain/jobs")).Jobs; len(js) != 1 {
		t.Fatalf("plain jobs %+v", js)
	}
}

func TestSingleModeListing(t *testing.T) {
	f := newFixture(t)
	ls := f.libraries()
	if ls.Mode != modeSingle || ls.Root != f.lib.Root() || len(ls.Libraries) != 1 || len(ls.Folders) != 0 {
		t.Fatalf("listing %+v", ls)
	}
	if l := ls.Libraries[0]; l.ID != "lib" || l.State != libOpen || l.Summary == nil || l.Summary.Total != 8 {
		t.Fatalf("lib %+v", l)
	}
	wantError(t, f.post("/api/libraries", `{"name":"x"}`), http.StatusBadRequest, "workspace")
	wantStatus(t, f.post("/api/libraries/lib/open", `{}`), http.StatusOK)
	wantError(t, f.get("/api/l/other/library"), http.StatusNotFound, "no such library")
	// The step 3 paths are gone.
	for _, p := range []string{"/api/library", "/api/timeline", "/api/media", "/api/thumb/0000000000000001", "/api/geo"} {
		wantError(t, f.get(p), http.StatusNotFound, "no such endpoint")
	}
	if singleID("/") != "library" || singleID(`C:\`) == "" {
		t.Error("singleID of a root")
	}
}
