package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"picmover/internal/backup"
)

// addTarget attaches a fresh empty folder as a target and returns it.
func (f *fixture) addTarget(name string) targetJSON {
	f.t.Helper()
	f.consistentTrash()
	dir := filepath.Join(f.base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	w := f.post("/api/l/lib/backups/targets", `{"path":`+jsonString(dir)+`,"label":"Red WD"}`)
	wantStatus(f.t, w, http.StatusCreated)
	return decode[targetJSON](f.t, w)
}

// consistentTrash moves the fixture's trashed file (its row is trashed,
// its file was left at the live path) into the trash, so a backup finds
// every catalog file (a missing one makes the run exit 1).
func (f *fixture) consistentTrash() {
	f.t.Helper()
	live := f.lib.Abs("image/2024/07/del.jpg")
	if _, err := os.Stat(live); err != nil {
		return
	}
	dst := f.lib.TrashPath("image/2024/07/del.jpg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(live, dst); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) startBackupJob(body string) jobJSON {
	f.t.Helper()
	w := f.post("/api/l/lib/jobs", body)
	wantStatus(f.t, w, http.StatusCreated)
	return f.pollJob(decode[jobJSON](f.t, w).ID)
}

func resultMap(t *testing.T, j jobJSON) map[string]any {
	t.Helper()
	b, _ := json.Marshal(j.Result)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		t.Fatalf("job result %s: %v", b, err)
	}
	return m
}

func TestBackupEndpointsAndJob(t *testing.T) {
	f := newFixture(t)
	w := f.get("/api/l/lib/backups")
	wantStatus(t, w, http.StatusOK)
	if got := strings.TrimSpace(w.Body.String()); got != `{"targets":[]}` {
		t.Fatalf("no targets: %s", got)
	}
	if l, _ := libByID(f.libraries(), "lib"); l.Backup != nil {
		t.Fatalf("backup status without targets: %+v", l.Backup)
	}
	tg := f.addTarget("drive")
	if !backup.ValidID(tg.ID) || tg.Label != "Red WD" || !tg.Reachable || tg.LastBackupAt != nil || tg.Set == nil ||
		tg.Set.Behind == nil || tg.Set.Behind.New == 0 || tg.Set.Files != 0 || tg.Set.Name != "lib" {
		t.Fatalf("added target %+v %+v", tg, tg.Set)
	}
	if _, err := os.Stat(filepath.Join(f.base, "drive", backup.MarkerName)); err != nil {
		t.Fatal("no marker created")
	}

	w = f.get("/api/l/lib/backups/targets/" + tg.ID + "/plan")
	wantStatus(t, w, http.StatusOK)
	var plan map[string]map[string]any
	json.Unmarshal(w.Body.Bytes(), &plan)
	for _, c := range []string{"new", "changed", "moved", "current", "extra"} {
		cl, ok := plan[c]
		if !ok || cl["count"] == nil || cl["bytes"] == nil || cl["paths"] == nil {
			t.Fatalf("plan class %s: %s", c, w.Body)
		}
	}
	if plan["new"]["count"].(float64) < 6 {
		t.Fatalf("plan %s", w.Body)
	}
	for _, p := range plan["new"]["paths"].([]any) {
		if strings.HasPrefix(p.(string), "/") || strings.Contains(p.(string), "..") {
			t.Fatalf("plan path %v", p)
		}
	}

	j := f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	if j.State != stateDone || j.ExitCode != 0 || j.Target != tg.ID || j.Library != "lib" || j.Error != nil {
		t.Fatalf("backup job %+v", j)
	}
	res := resultMap(t, j)
	if res["copied"].(float64) < 6 || res["bytes"].(float64) == 0 || res["failed"] == nil {
		t.Fatalf("result %v", res)
	}
	// Times are Unix seconds; behind is 0 after the run.
	w = f.get("/api/l/lib/backups?plan=1")
	var all map[string][]map[string]any
	json.Unmarshal(w.Body.Bytes(), &all)
	got := all["targets"][0]
	if _, ok := got["lastBackupAt"].(float64); !ok {
		t.Fatalf("lastBackupAt %v", got["lastBackupAt"])
	}
	set := got["set"].(map[string]any)
	behind := set["behind"].(map[string]any)
	if behind["new"].(float64) != 0 || set["files"].(float64) < 6 || set["unverified"].(float64) < 6 {
		t.Fatalf("after the run %s", w.Body)
	}
	w = f.get("/api/l/lib/backups/targets/" + tg.ID + "/runs")
	wantStatus(t, w, http.StatusOK)
	var runs map[string][]map[string]any
	json.Unmarshal(w.Body.Bytes(), &runs)
	if len(runs["runs"]) != 1 || runs["runs"][0]["kind"] != "backup" || runs["runs"][0]["finishedAt"] == nil {
		t.Fatalf("runs %s", w.Body)
	}
	if _, ok := runs["runs"][0]["startedAt"].(float64); !ok {
		t.Fatalf("startedAt %s", w.Body)
	}
	// The libraries listing carries the one-liner.
	l, _ := libByID(f.libraries(), "lib")
	if l.Backup == nil || l.Backup.Targets != 1 || l.Backup.LastBackupAt == nil || l.Backup.Behind == nil || *l.Backup.Behind != 0 {
		t.Fatalf("library backup status %+v", l.Backup)
	}

	// Scrub, with repair.
	j = f.startBackupJob(`{"kind":"scrub","target":"` + tg.ID + `","budget":60,"repair":true}`)
	if j.State != stateDone || j.ExitCode != 0 {
		t.Fatalf("scrub job %+v", j)
	}
	if r := resultMap(t, j); r["checked"].(float64) < 6 || r["damaged"] == nil || r["unknown"] == nil || r["missing"] == nil {
		t.Fatalf("scrub result %v", r)
	}

	// Restore missing brings back a file deleted from the library.
	victim := f.lib.Abs("image/undated/u.jpg")
	orig, _ := os.ReadFile(victim)
	os.Remove(victim)
	j = f.startBackupJob(`{"kind":"restore","target":"` + tg.ID + `","mode":"missing"}`)
	if j.State != stateDone || j.Mode != "missing" {
		t.Fatalf("restore job %+v", j)
	}
	if b, _ := os.ReadFile(victim); string(b) != string(orig) {
		t.Fatal("file not restored")
	}
	if r := resultMap(t, j); r["restored"].(float64) != 1 {
		t.Fatalf("restore result %v", r)
	}
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"restore","target":"`+tg.ID+`","mode":"user-data"}`),
		http.StatusBadRequest, "user-data")

	// Rename and forget.
	w = f.post("/api/l/lib/backups/targets/"+tg.ID, `{"label":"Blue"}`)
	wantStatus(t, w, http.StatusOK)
	if decode[targetJSON](t, w).Label != "Blue" {
		t.Fatal("label not changed")
	}
	wantStatus(t, f.post("/api/l/lib/backups/targets/"+tg.ID+"/forget", `{}`), http.StatusOK)
	wantStatus(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan"), http.StatusNotFound)
	if _, err := os.Stat(filepath.Join(f.base, "drive", "lib", "user.db")); err != nil {
		t.Fatal("forget touched the target")
	}
}

func TestBackupPrune(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	// A file the library no longer has.
	if err := os.WriteFile(filepath.Join(f.base, "drive", "lib", "image", "gone.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := backup.OpenSet(t.Context(), &backup.Target{Root: filepath.Join(f.base, "drive")}, "lib", false)
	if err != nil {
		t.Fatal(err)
	}
	set.Close()
	// Make it an extra by planting a row through a second library-less run:
	// simplest is a previous/ file older than the keep window.
	old := filepath.Join(f.base, "drive", "lib", ".picmover", "previous", "image", "x.jpg.1000")
	writeFile(t, old, []byte("old version"))

	w := f.get("/api/l/lib/backups/targets/" + tg.ID + "/plan")
	var plan map[string]map[string]any
	json.Unmarshal(w.Body.Bytes(), &plan)
	n := int(plan["extra"]["count"].(float64))
	if n != 1 || plan["extra"]["paths"].([]any)[0] != ".picmover/previous/image/x.jpg.1000" {
		t.Fatalf("extra %v", plan["extra"])
	}
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"prune","target":"`+tg.ID+`","expected":1}`), http.StatusBadRequest, "confirm")
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"prune","target":"`+tg.ID+`","confirm":true}`), http.StatusBadRequest, "expected")
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"prune","target":"`+tg.ID+`","confirm":true,"expected":2}`),
		http.StatusConflict, "now lists 1")
	if _, err := os.Stat(old); err != nil {
		t.Fatal("removed on a wrong count")
	}
	j := f.startBackupJob(fmt.Sprintf(`{"kind":"prune","target":"%s","confirm":true,"expected":%d}`, tg.ID, n))
	if j.State != stateDone || resultMap(t, j)["removed"].(float64) != 1 {
		t.Fatalf("prune job %+v %v", j, j.Result)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("not removed")
	}
	// The unknown file is never pruned (it has no row).
	if _, err := os.Stat(filepath.Join(f.base, "drive", "lib", "image", "gone.jpg")); err != nil {
		t.Fatal("an unknown file was removed")
	}
}

func TestBackupTargetRules(t *testing.T) {
	f := newFixture(t)
	post := func(body string) *httptest.ResponseRecorder { return f.post("/api/l/lib/backups/targets", body) }
	empty := filepath.Join(f.base, "empty")
	os.Mkdir(empty, 0o755)
	full := filepath.Join(f.base, "full")
	writeFile(t, filepath.Join(full, "x.txt"), []byte("x"))
	for _, c := range []struct{ body, msg string }{
		{`{"path":"relative/dir"}`, "absolute"},
		{`{"path":""}`, "absolute"},
		{`{"path":"/x\u0000y"}`, "NUL"},
		{`{"path":"\\\\?\\C:\\backup"}`, "device"},
		{`{"path":"//./PhysicalDrive0"}`, "device"},
		{`{"path":` + jsonString(f.lib.Root()) + `}`, "outside every library"},
		{`{"path":` + jsonString(filepath.Join(f.lib.Root(), "image")) + `}`, "outside every library"},
		{`{"path":` + jsonString(f.base) + `}`, "outside every library"},
		{`{"path":` + jsonString(full) + `}`, "not empty"},
		{`{"path":` + jsonString(filepath.Join(f.base, "nope")) + `}`, "does not exist"},
		{`{"path":` + jsonString(empty) + `,"label":"a\nb"}`, "label"},
		{`{"path":` + jsonString(empty) + `,"set":"../x"}`, "set"},
	} {
		w := post(c.body)
		wantError(t, w, http.StatusBadRequest, c.msg)
		if strings.Contains(w.Body.String(), f.base) && !strings.Contains(c.body, "nope") {
			t.Errorf("%s: answer names a path: %s", c.body, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(empty, backup.MarkerName)); err == nil {
		t.Fatal("marker created by a refused request")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(f.base, "into-lib")
		os.Symlink(filepath.Join(f.lib.Root(), "image"), link)
		wantError(t, post(`{"path":`+jsonString(link)+`}`), http.StatusBadRequest, "outside every library")
	}
	// A set of another library under the same name: 409.
	other := filepath.Join(f.base, "other")
	os.Mkdir(other, 0o755)
	tg, _, err := backup.InitTarget(other)
	if err != nil {
		t.Fatal(err)
	}
	s, err := backup.OpenSet(t.Context(), tg, "lib", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Claim(t.Context(), backup.Identity{LibraryID: "ffffffffffffffff", Instance: "ffffffffffffffff"}, false, false)
	s.Close()
	wantError(t, post(`{"path":`+jsonString(other)+`}`), http.StatusConflict, "another library")
	// ... unless another set name is chosen.
	w := post(`{"path":` + jsonString(other) + `,"set":"lib-2"}`)
	wantStatus(t, w, http.StatusCreated)
	if got := decode[targetJSON](t, w); got.ID != tg.ID() || got.Set == nil || got.Set.Name != "lib-2" {
		t.Fatalf("other set %+v", got)
	}
	// Re-pointing needs the same marker id.
	empty2 := filepath.Join(f.base, "empty2")
	os.Mkdir(empty2, 0o755)
	tg2, _, _ := backup.InitTarget(empty2)
	wantError(t, f.post("/api/l/lib/backups/targets/"+tg.ID(), `{"path":`+jsonString(empty2)+`}`), http.StatusConflict, "another backup target")
	_ = tg2
	moved := filepath.Join(f.base, "other-moved")
	if err := os.Rename(other, moved); err != nil {
		t.Fatal(err)
	}
	w = f.get("/api/l/lib/backups")
	var all backupsJSON
	json.Unmarshal(w.Body.Bytes(), &all)
	if len(all.Targets) != 1 || all.Targets[0].Reachable || all.Targets[0].Set != nil {
		t.Fatalf("moved target %+v", all.Targets)
	}
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"backup","target":"`+tg.ID()+`"}`), http.StatusConflict, "not reachable")
	w = f.post("/api/l/lib/backups/targets/"+tg.ID(), `{"path":`+jsonString(moved)+`}`)
	wantStatus(t, w, http.StatusOK)
	if got := decode[targetJSON](t, w); !got.Reachable || got.Path != moved {
		t.Fatalf("re-pointed %+v", got)
	}
}

func TestBackupIDValidation(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"xyz", "0123456789ABCDEF", "0123456789abcde", "%2e%2e"} {
		wantStatus(t, f.get("/api/l/lib/backups/targets/"+id+"/plan"), http.StatusBadRequest)
		wantStatus(t, f.get("/api/l/lib/backups/targets/"+id+"/runs"), http.StatusBadRequest)
		wantStatus(t, f.post("/api/l/lib/backups/targets/"+id+"/forget", `{}`), http.StatusBadRequest)
		wantStatus(t, f.post("/api/l/lib/backups/targets/"+id, `{}`), http.StatusBadRequest)
	}
	wantStatus(t, f.get("/api/l/lib/backups/targets/0123456789abcdef/plan"), http.StatusNotFound)
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"backup","target":"nope"}`), http.StatusBadRequest, "target")
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"backup"}`), http.StatusBadRequest, "target")
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"backup","target":"0123456789abcdef"}`), http.StatusNotFound, "no such target")
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"scrub","target":"0123456789abcdef","budget":-1}`), http.StatusBadRequest, "budget")
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"backup","target":"0123456789abcdef","workers":99}`), http.StatusBadRequest, "workers")
	// Mutations need the step 3 headers.
	w := f.request(http.MethodPost, "/api/l/lib/backups/targets", strings.NewReader(`{"path":"/tmp"}`),
		map[string]string{"Content-Type": "application/json"})
	wantStatus(t, w, http.StatusForbidden)
}

func TestValidTargetPath(t *testing.T) {
	ok := []string{"//server/share/backup"}
	if runtime.GOOS == "windows" {
		ok = append(ok, `\\server\share\backup`, `C:\backup`, `\\nas\photos`)
	} else {
		ok = append(ok, "/media/x/backup")
	}
	for _, p := range ok {
		if err := ValidTargetPath(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
		// UNC targets are POST-only; import sources and /api/fs refuse them.
		if strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`) {
			if ValidSourcePath(p) == nil {
				t.Errorf("ValidSourcePath accepted %q", p)
			}
		}
	}
	for _, p := range []string{"", "rel", `\\?\C:\x`, `//?/C:/x`, `\\.\pipe\x`, `\??\C:\x`, "/a\x00b"} {
		if ValidTargetPath(p) == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestBackupJobConflictsWithImport(t *testing.T) {
	b := newBlocker()
	f := newFixtureWith(t, b.adjust)
	tg := f.addTarget("drive")
	src := importSource(t, f.base, 3)
	w := f.post("/api/l/lib/jobs", `{"kind":"import","source":`+jsonString(src)+`}`)
	wantStatus(t, w, http.StatusCreated)
	b.wait(t)
	wantError(t, f.post("/api/l/lib/jobs", `{"kind":"backup","target":"`+tg.ID+`"}`), http.StatusConflict, "already running")
	close(b.release)
	f.pollJob(decode[jobJSON](t, w).ID)
	j := f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	if j.State != stateDone {
		t.Fatalf("backup after the import %+v", j)
	}
}

func TestBackupProgressEvent(t *testing.T) {
	j := &job{params: jobParams{Kind: kindBackup, Target: "0123456789abcdef"}, state: stateRunning}
	_, _, sub := j.subscribe()
	j.onBackupProgress(backup.Progress{Phase: "copy", Files: 2, Bytes: 10, TotalFiles: 5, TotalBytes: 50, Current: "image/a.jpg", Failed: 1})
	data := <-sub.progress
	var m map[string]any
	json.Unmarshal(data, &m)
	for _, k := range []string{"phase", "files", "bytes", "totalFiles", "totalBytes", "current", "failed"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("progress %s lacks %s", data, k)
		}
	}
	if m["phase"] != "copy" || m["current"] != "image/a.jpg" || m["failed"] != 1.0 {
		t.Fatalf("progress %s", data)
	}
	b, _ := json.Marshal(j.snapshot())
	var obj map[string]any
	json.Unmarshal(b, &obj)
	if obj["target"] != "0123456789abcdef" || obj["progress"].(map[string]any)["totalFiles"] != 5.0 {
		t.Fatalf("job object %s", b)
	}
}
