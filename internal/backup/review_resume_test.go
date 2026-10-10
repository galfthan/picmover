package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"picmover/internal/library"
)

// Tests for the resumability and performance review of step 5 (H1,
// M1-M6, L1-L7).

func nonUserPrevious(t *testing.T, s *Set) int {
	t.Helper()
	n := 0
	for _, p := range previousFiles(t, s) {
		if !strings.HasPrefix(p, "user.db.") {
			n++
		}
	}
	return n
}

// H1: backup, scrub, prune and restore take the set lock; a second one
// is refused naming the holder, and a reader is never blocked by a
// writer.
func TestSetLock(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	unlock, held, err := set.Lock("backup")
	if err != nil || !held {
		t.Fatalf("lock: %v %v", held, err)
	}
	check := func(what string, err error) {
		t.Helper()
		var le *library.ErrLocked
		if !errors.As(err, &le) || !strings.Contains(err.Error(), "the backup set is in use by another picmover process") ||
			le.PID != os.Getpid() || le.Cmd != "backup" {
			t.Fatalf("%s while locked: %v", what, err)
		}
	}
	_, err = Run(f.ctx, f.src(), set, Options{Owner: f.id})
	check("backup", err)
	_, err = Scrub(f.ctx, set, ScrubOptions{})
	check("scrub", err)
	_, err = Prune(f.ctx, f.src(), set, PruneOptions{Keep: DefaultKeep, Owner: f.id})
	check("prune", err)
	empty, _ := library.Open(filepath.Join(t.TempDir(), "r"))
	_, err = Restore(f.ctx, set, empty, RestoreOptions{})
	check("restore", err)
	check("busy", SetBusy(f.target, "photos"))
	// A dry run takes no lock.
	if _, err := Restore(f.ctx, set, empty, RestoreOptions{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	unlock()
	if err := SetBusy(f.target, "photos"); err != nil {
		t.Fatalf("busy after unlock: %v", err)
	}
	if _, err := Scrub(f.ctx, set, ScrubOptions{}); err != nil {
		t.Fatalf("scrub after unlock: %v", err)
	}
	// A writer holding backup.db's write lock does not block a reader
	// that opens the set read-only (the listing).
	w := f.set(false)
	tx, err := w.db.BeginTx(f.ctx, nil) // BEGIN IMMEDIATE
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE meta SET value = value WHERE key = 'name'`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	ro, err := OpenSetReadOnly(f.ctx, f.target, "photos")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := ro.Summary(f.ctx)
	ro.Close()
	tx.Rollback()
	if err != nil || sum.Files != 5 || time.Since(started) > 2*time.Second {
		t.Fatalf("read-only open during a write: %+v %v after %v", sum, err, time.Since(started))
	}
	// A read-only set refuses writes.
	ro, _ = OpenSetReadOnly(f.ctx, f.target, "photos")
	defer ro.Close()
	if err := ro.SetMeta(f.ctx, "x", "y"); err == nil {
		t.Fatal("a read-only set wrote")
	}
}

// M1: a run killed after renaming a moved file, before its row: the next
// run adopts the file and drops the row at the old path; and a row whose
// content another row at a library path already holds is an extra.
func TestKillAfterMove(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	os.MkdirAll(f.lib.Abs("image/2024/08"), 0o755)
	os.Rename(f.lib.Abs("image/2024/07/a.jpg"), f.lib.Abs("image/2024/08/a.jpg"))
	f.db.UpdateRelPath(f.ctx, f.hashes["image/2024/07/a.jpg"], "image/2024/08/a.jpg")
	crashAfter = func(done int) bool { return done == 1 }
	_, err := Run(f.ctx, f.src(), f.set(false), Options{Owner: f.id, Workers: 1})
	crashAfter = nil
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatal(err)
	}
	st := f.run(Options{})
	if st.Adopted != 1 || st.FailedCount != 0 {
		t.Fatalf("resume %+v", st)
	}
	set := f.set(false)
	rows := rowsOf(t, set)
	if _, ok := rows["image/2024/07/a.jpg"]; ok {
		t.Fatal("the row at the old path survived")
	}
	if sst, err := Scrub(f.ctx, set, ScrubOptions{}); err != nil || sst.ExitCode() != 0 {
		t.Fatalf("scrub after resume %+v %v", sst, err)
	}
	// A leftover duplicate (a repaired phantom) is an extra.
	write(t, set.Abs("image/2024/07/a.jpg"), "AAAA")
	set.apply(f.ctx, []rowOp{{upsert: &FileRow{RelPath: "image/2024/07/a.jpg", Hash: f.hashes["image/2024/07/a.jpg"], Size: 4}}})
	plan, _ := PlanFor(f.ctx, f.src(), set)
	if len(plan.Extras) != 1 || plan.Extras[0].RelPath != "image/2024/07/a.jpg" {
		t.Fatalf("extras %+v", plan.Extras)
	}
}

// M2: a run killed after putting a changed file in place: the next run
// adopts it and never moves the correct copy to previous/.
func TestKillAfterChanged(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	write(t, f.lib.Abs("notes.txt"), "hello, changed")
	crashAfter = func(done int) bool { return done == 1 }
	Run(f.ctx, f.src(), f.set(false), Options{Owner: f.id, Workers: 1})
	crashAfter = nil
	set := f.set(false)
	before := nonUserPrevious(t, set)
	st := f.run(Options{})
	if st.Copied != 0 || st.Adopted != 1 || nonUserPrevious(t, set) != before {
		t.Fatalf("resume %+v, previous %d -> %d", st, before, nonUserPrevious(t, set))
	}
	if readFile(t, set.Abs("notes.txt")) != "hello, changed" {
		t.Fatal("content")
	}
}

// M3: scrub results are written in batches with the cursor; every row is
// verified and the counts follow.
func TestScrubBatches(t *testing.T) {
	f := newFx(t)
	for i := range 150 {
		f.file(filepath.Join("n", strings.Repeat("x", 1+i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt"), "x")
	}
	f.run(Options{})
	set := f.set(false)
	st, err := Scrub(f.ctx, set, ScrubOptions{})
	if err != nil || st.Checked != 155 || !st.PassComplete {
		t.Fatalf("%+v %v", st, err)
	}
	for _, r := range rowsOf(t, set) {
		if r.VerifiedAt == 0 {
			t.Fatalf("%s not verified", r.RelPath)
		}
	}
	if sum, _ := set.Summary(f.ctx); sum.Unverified != 0 || sum.Files != 155 {
		t.Fatalf("summary %+v", sum)
	}
	// A scrub stopped by its budget writes what it verified, with its
	// cursor.
	set.SetMeta(f.ctx, "scrub_cursor", "")
	set.db.Exec(`UPDATE files SET verified_at = NULL`)
	st, err = Scrub(f.ctx, set, ScrubOptions{Budget: time.Nanosecond})
	verified := 0
	for _, r := range rowsOf(t, set) {
		if r.VerifiedAt != 0 {
			verified++
		}
	}
	if err != nil || st.OK != 1 || verified != 1 {
		t.Fatalf("budgeted scrub %+v %v, %d verified", st, err, verified)
	}
	if c, _ := readCursor(f.ctx, set); c.Pass == 0 || c.Path == "" {
		t.Fatalf("cursor not stored: %+v", c)
	}
}

// M5: a restore works from a write-protected target.
func TestRestoreReadOnlyTarget(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions are not enforced here")
	}
	f := newFx(t)
	f.run(Options{})
	var dirs []string
	filepath.WalkDir(f.target.Root, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			os.Chmod(p, 0o555)
			if d.IsDir() {
				dirs = append(dirs, p)
			} else {
				os.Chmod(p, 0o444)
			}
		}
		return nil
	})
	defer func() {
		for _, d := range dirs {
			os.Chmod(d, 0o755)
		}
	}()
	set, err := OpenSetReadOnly(f.ctx, f.target, "photos")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	empty, _ := library.Open(filepath.Join(t.TempDir(), "r"))
	st, err := Restore(f.ctx, set, empty, RestoreOptions{})
	if err != nil || st.Restored != 5 || st.UserDB != "restored" || st.FailedCount != 0 {
		t.Fatalf("restore from a read-only target %+v %v", st, err)
	}
}

// L1: a verified_at in the future counts as never verified. L2: a file
// verified in the first second of a pass is not read again in it.
func TestScrubClock(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	set.db.Exec(`UPDATE files SET verified_at = ?`, time.Now().Add(48*time.Hour).Unix())
	write(t, set.Abs("notes.txt"), "rotten")
	st, err := Scrub(f.ctx, set, ScrubOptions{})
	if err != nil || st.Checked != 5 || st.DamagedCount != 1 {
		t.Fatalf("future verified_at %+v %v", st, err)
	}
	write(t, set.Abs("notes.txt"), "hello")
	// A frozen clock: every budgeted scrub runs in the same second.
	frozen := time.Now().Add(time.Hour)
	set.now = func() time.Time { return frozen }
	set.SetMeta(f.ctx, "scrub_cursor", "")
	set.db.Exec(`UPDATE files SET verified_at = NULL`)
	for range 5 {
		st, err := Scrub(f.ctx, set, ScrubOptions{Budget: time.Nanosecond})
		if err != nil || st.Checked != 1 {
			t.Fatalf("%+v %v", st, err)
		}
	}
	verified := 0
	for _, r := range rowsOf(t, set) {
		if r.VerifiedAt != 0 {
			verified++
		}
	}
	if verified != 5 {
		t.Fatalf("after 5 one-file scrubs %d rows verified (a row was read twice)", verified)
	}
}

// L4: runs left unfinished by a killed run are closed as interrupted by
// the next one.
func TestInterruptedRunsClosed(t *testing.T) {
	f := newFx(t)
	crashAfter = func(done int) bool { return done == 2 }
	Run(f.ctx, f.src(), f.set(true), Options{Owner: f.id, Workers: 1})
	crashAfter = nil
	f.run(Options{})
	runs, _ := f.set(false).Runs(f.ctx, 10)
	if len(runs) != 2 || runs[1].FinishedAt.IsZero() {
		t.Fatalf("runs %+v", runs)
	}
	var stats map[string]any
	json.Unmarshal(runs[1].Stats, &stats)
	if stats["interrupted"] != true {
		t.Fatalf("stats %s", runs[1].Stats)
	}
}

// L5: the counts the listing shows are kept by triggers.
func TestCountsKept(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	cmp := func(what string) {
		t.Helper()
		var want SetSummary
		set.db.QueryRow(`SELECT COUNT(*), coalesce(SUM(size), 0), coalesce(SUM(verified_at IS NULL), 0) FROM files`).
			Scan(&want.Files, &want.Bytes, &want.Unverified)
		if got, err := set.Summary(f.ctx); err != nil || got != want {
			t.Fatalf("%s: counts %+v, want %+v (%v)", what, got, want, err)
		}
	}
	cmp("after a backup")
	Scrub(f.ctx, set, ScrubOptions{})
	cmp("after a scrub")
	write(t, f.lib.Abs("notes.txt"), "changed content")
	os.Remove(f.lib.Abs("image/2024/07/a.xmp"))
	f.run(Options{})
	cmp("after a change")
	plan, _ := PlanFor(f.ctx, f.src(), set)
	pp, _ := PlanPrune(set, plan, DefaultKeep, time.Now())
	Prune(f.ctx, f.src(), set, PruneOptions{Keep: DefaultKeep, Owner: f.id, Expected: pp.Count})
	cmp("after a prune")
}

// L7: non-media files are matched by nanosecond mtime: an edit of the
// same size within the same second is noticed.
func TestMtimeNanoseconds(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	p := f.lib.Abs("notes.txt")
	fi, _ := os.Stat(p)
	mt := fi.ModTime().Truncate(time.Second).Add(100 * time.Millisecond)
	os.Chtimes(p, mt, mt)
	f.run(Options{}) // refreshed with this mtime
	write(t, p, "HELLO")
	mt2 := mt.Add(200 * time.Millisecond) // same second, same size
	os.Chtimes(p, mt2, mt2)
	st := f.run(Options{})
	if st.Changed != 1 || st.Copied != 1 {
		t.Fatalf("same-second edit %+v", st)
	}
	if readFile(t, f.set(false).Abs("notes.txt")) != "HELLO" {
		t.Fatal("not copied")
	}
}
