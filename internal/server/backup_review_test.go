package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/backup"
	"picmover/internal/userdata"
)

// Tests for the step 5 file-safety review (H2, M1, M2, L2, B1-B3) and
// the job object's scrub budget.

func (f *fixture) userDB() *userdata.DB {
	f.t.Helper()
	u, err := userdata.Open(f.t.Context(), filepath.Join(f.lib.Root(), userdata.FileName))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { u.Close() })
	return u
}

// relOf is the library path of an imported file, by source name.
func (f *fixture) relOf(name string) string {
	f.t.Helper()
	m, err := f.db.GetByHash(f.t.Context(), f.hashes[name])
	if err != nil {
		f.t.Fatal(err)
	}
	return m.RelPath
}

func (f *fixture) listing() backupsJSON {
	f.t.Helper()
	w := f.get("/api/l/lib/backups")
	wantStatus(f.t, w, http.StatusOK)
	return decode[backupsJSON](f.t, w)
}

// H2 / B2: a remembered set name that another library claimed later is
// that library's: the listing shows no set and the error, and every job
// and the plan are refused; nothing is pruned or restored from it.
func TestBackupOtherLibrarySetStoredName(t *testing.T) {
	f := newFixture(t)
	f.consistentTrash()
	dir := filepath.Join(f.base, "nas")
	os.Mkdir(dir, 0o755)
	w := f.post("/api/l/lib/backups/targets", `{"path":`+jsonString(dir)+`,"set":"shared"}`)
	wantStatus(t, w, http.StatusCreated)
	tg := decode[targetJSON](t, w)
	// Penny's library claims "shared" and backs up a diary.
	target, err := backup.OpenTarget(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := backup.OpenSet(t.Context(), target, "shared", true)
	if err != nil {
		t.Fatal(err)
	}
	penny := backup.Identity{LibraryID: "aaaaaaaaaaaaaaaa", Instance: "bbbbbbbbbbbbbbbb", Root: "/w/penny", Host: "h"}
	if err := s.Claim(t.Context(), penny, false, false); err != nil {
		t.Fatal(err)
	}
	s.Close()
	diary := filepath.Join(dir, "shared", "image", "diary.txt")
	writeFile(t, diary, []byte("penny's diary"))

	all := f.listing()
	want := "the folder shared on this target holds another library's backup"
	if len(all.Targets) != 1 || all.Targets[0].Set != nil || all.Targets[0].Error != want || all.Targets[0].Foreign {
		t.Fatalf("listing %+v", all.Targets)
	}
	wantError(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan"), http.StatusConflict, want)
	for _, body := range []string{
		`{"kind":"backup","target":"%s"}`,
		`{"kind":"scrub","target":"%s"}`,
		`{"kind":"restore","target":"%s"}`,
		`{"kind":"prune","target":"%s","confirm":true,"expected":1}`,
		`{"kind":"backup","target":"%s","takeOver":true}`,
	} {
		wantError(t, f.post("/api/l/lib/jobs", fmt.Sprintf(body, tg.ID)), http.StatusConflict, want)
	}
	if _, err := os.Stat(diary); err != nil {
		t.Fatal("penny's file removed")
	}
	if _, err := os.Stat(f.lib.Abs("image/diary.txt")); err == nil {
		t.Fatal("penny's file restored into this library")
	}
}

// H2: a GET never stores a set name in user.db.
func TestBackupGetStoresNothing(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.base, "drive")
	os.Mkdir(dir, 0o755)
	tgt, _, err := backup.InitTarget(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := f.userDB()
	if _, _, err := u.AddBackupTarget(t.Context(), userdata.BackupTarget{ID: tgt.ID(), Label: "d", LastPath: tgt.Root}); err != nil {
		t.Fatal(err)
	}
	f.get("/api/l/lib/backups?plan=1")
	f.get("/api/l/lib/backups/targets/" + tgt.ID() + "/plan")
	f.get("/api/l/lib/backups/targets/" + tgt.ID() + "/runs")
	if bt, _ := u.BackupTarget(t.Context(), tgt.ID()); bt.SetName != "" {
		t.Fatalf("a GET stored the set name %q", bt.SetName)
	}
	f.consistentTrash()
	f.startBackupJob(`{"kind":"backup","target":"` + tgt.ID() + `"}`)
	if bt, _ := u.BackupTarget(t.Context(), tgt.ID()); bt.SetName != "lib" {
		t.Fatalf("the backup did not store the set name: %q", bt.SetName)
	}
}

// M1: a set last written by another copy of this library is shown with
// foreign: true and no set; jobs are refused with the folder and host of
// that copy; takeOver adopts it.
func TestBackupForeignInstance(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	target, _ := backup.OpenTarget(filepath.Join(f.base, "drive"))
	s, err := backup.OpenSet(t.Context(), target, "lib", false)
	if err != nil {
		t.Fatal(err)
	}
	me, _ := f.userDB().Identity(t.Context())
	laptop := me
	laptop.Instance, laptop.Root, laptop.Host = "cccccccccccccccc", "/w2/laptop", "laptop"
	if err := s.Claim(t.Context(), laptop, true, true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	want := "last written by the library at /w2/laptop on laptop"
	all := f.listing()
	if got := all.Targets[0]; got.Set != nil || !got.Foreign || !strings.Contains(got.Error, want) {
		t.Fatalf("listing %+v", got)
	}
	w := f.get("/api/l/lib/backups")
	if !strings.Contains(w.Body.String(), `"foreign":true`) || !strings.Contains(w.Body.String(), `"set":null`) {
		t.Fatalf("listing JSON %s", w.Body)
	}
	for _, body := range []string{`{"kind":"backup","target":"%s"}`, `{"kind":"scrub","target":"%s"}`,
		`{"kind":"restore","target":"%s"}`, `{"kind":"prune","target":"%s","confirm":true,"expected":0}`} {
		wantError(t, f.post("/api/l/lib/jobs", fmt.Sprintf(body, tg.ID)), http.StatusConflict, want)
	}
	wantError(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan"), http.StatusConflict, want)
	wantStatus(t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/runs"), http.StatusOK)
	j := f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `","takeOver":true}`)
	if j.State != stateDone {
		t.Fatalf("take over %+v %v", j, j.Error)
	}
	if got := f.listing().Targets[0]; got.Set == nil || got.Foreign || got.Error != "" {
		t.Fatalf("after take over %+v", got)
	}
}

// M2: a prune job is refused while the library scan is incomplete
// (here: a catalog file missing), unless forced.
func TestBackupPruneIncompleteScan(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	os.Remove(f.lib.Abs(f.relOf("a.jpg")))
	w := f.get("/api/l/lib/backups/targets/" + tg.ID + "/plan")
	plan := decode[planJSON](t, w)
	if plan.Missing != 1 || plan.Errors == nil || plan.Collisions == nil {
		t.Fatalf("plan %s", w.Body)
	}
	body := fmt.Sprintf(`{"kind":"prune","target":"%s","confirm":true,"expected":%d}`, tg.ID, plan.Extra.Count)
	wantError(t, f.post("/api/l/lib/jobs", body), http.StatusConflict, "could not be read completely")
	j := f.startBackupJob(strings.TrimSuffix(body, "}") + `,"force":true}`)
	if j.State != stateDone {
		t.Fatalf("forced prune %+v", j)
	}
	// The backup job now ends with exit 1 and says why.
	j = f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	if j.ExitCode != 1 || j.Error == nil || !strings.Contains(*j.Error, "missing in the library") {
		t.Fatalf("backup with a missing file %+v", j)
	}
}

// L2: keepDays 0 keeps no earlier versions (absent keeps 30 days).
func TestBackupKeepDaysZero(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`) // the first user.db snapshot goes to previous/
	p30 := decode[planJSON](t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan"))
	p0 := decode[planJSON](t, f.get("/api/l/lib/backups/targets/"+tg.ID+"/plan?keepDays=0"))
	if p30.Extra.Count != 0 || p0.Extra.Count < 1 || p0.KeepDays != 0 {
		t.Fatalf("keep 30: %d, keep 0: %d", p30.Extra.Count, p0.Extra.Count)
	}
	j := f.startBackupJob(fmt.Sprintf(`{"kind":"prune","target":"%s","confirm":true,"expected":%d,"keepDays":0}`, tg.ID, p0.Extra.Count))
	if j.State != stateDone || resultMap(t, j)["removed"].(float64) != float64(p0.Extra.Count) {
		t.Fatalf("prune keep 0 %+v", j)
	}
}

// B1: targets are listed in the order they were added.
func TestBackupTargetsListedInOrderAdded(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for _, n := range []string{"d1", "d2", "d3", "d4"} {
		ids = append(ids, f.addTarget(n).ID)
	}
	all := f.listing()
	for i, tg := range all.Targets {
		if tg.ID != ids[i] {
			t.Fatalf("order %v, added %v", all.Targets, ids)
		}
	}
}

// B3: the plan's restore class and set.restorable; scrub jobs carry
// their budget.
func TestBackupRestoreClassAndBudget(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	rel := f.relOf("a.jpg")
	os.Remove(f.lib.Abs(rel))
	w := f.get("/api/l/lib/backups/targets/" + tg.ID + "/plan")
	var raw map[string]json.RawMessage
	json.Unmarshal(w.Body.Bytes(), &raw)
	plan := decode[planJSON](t, w)
	if _, ok := raw["restore"]; !ok || plan.Restore.Count != 1 || plan.Restore.Paths.([]any)[0] != rel {
		t.Fatalf("restore class %s", w.Body)
	}
	all := decode[backupsJSON](t, f.get("/api/l/lib/backups?plan=1"))
	if all.Targets[0].Set == nil || all.Targets[0].Set.Restorable != 1 {
		t.Fatalf("restorable %+v", all.Targets[0].Set)
	}
	j := f.startBackupJob(`{"kind":"scrub","target":"` + tg.ID + `","budget":900}`)
	if j.Budget != 900 {
		t.Fatalf("scrub job budget %+v", j)
	}
	b, _ := json.Marshal(f.startBackupJob(`{"kind":"scrub","target":"` + tg.ID + `"}`))
	if strings.Contains(string(b), `"budget"`) {
		t.Fatalf("budget on a scrub without one: %s", b)
	}
}
