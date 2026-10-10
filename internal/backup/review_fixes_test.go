package backup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/library"
	"picmover/internal/userdata"
)

// Tests for the file-safety review of step 5 (H1, H2, M1-M4, L1-L4, B3).

func previousFiles(t *testing.T, s *Set) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(s.PreviousDir(), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(s.PreviousDir(), p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func skipIfRootOrWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions are not enforced here")
	}
}

// H1: an existing folder named like the set that is not a set is never
// taken over: another picmover library copied there, or any other
// folder; a folder holding only file system bookkeeping is fine.
func TestH1ExistingFolderRefused(t *testing.T) {
	f := newFx(t)
	other := filepath.Join(f.target.Root, "photos")
	write(t, filepath.Join(other, "image", "2018", "06", "tiny.jpg"), "B's photo")
	write(t, filepath.Join(other, library.DBName), "B's catalog")
	write(t, filepath.Join(other, library.UserDBName), "B's user data")
	if _, err := OpenSet(f.ctx, f.target, "photos", true); !errors.Is(err, ErrLibraryFolder) {
		t.Fatalf("library folder: %v", err)
	}
	// A library with a lock only.
	os.Remove(filepath.Join(other, library.DBName))
	os.Remove(filepath.Join(other, library.UserDBName))
	write(t, filepath.Join(other, library.MetaDir, library.LockName), "")
	if _, err := OpenSet(f.ctx, f.target, "photos", true); !errors.Is(err, ErrLibraryFolder) {
		t.Fatalf("locked library folder: %v", err)
	}
	os.RemoveAll(filepath.Join(other, library.MetaDir))
	// An ordinary folder.
	if _, err := OpenSet(f.ctx, f.target, "photos", true); !errors.Is(err, ErrNotSetFolder) {
		t.Fatalf("ordinary folder: %v", err)
	}
	if readFile(t, filepath.Join(other, "image", "2018", "06", "tiny.jpg")) != "B's photo" {
		t.Fatal("B's photo touched")
	}
	if _, err := os.Lstat(filepath.Join(other, library.MetaDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused folder got a .picmover")
	}
	// Only bookkeeping: accepted.
	os.RemoveAll(other)
	write(t, filepath.Join(other, ".DS_Store"), "x")
	os.Mkdir(filepath.Join(other, library.MetaDir), 0o755) // a killed first run
	s, err := OpenSet(f.ctx, f.target, "photos", true)
	if err != nil {
		t.Fatalf("bookkeeping only: %v", err)
	}
	s.Close()
	// Now it is a set: opened again, even with files in it.
	f.run(Options{})
	if _, err := OpenSet(f.ctx, f.target, "photos", true); err != nil {
		t.Fatalf("existing set: %v", err)
	}
}

// H1: a file without a row at a destination is a conflict for that file:
// it is never moved into previous/ (where prune would delete it).
func TestH1RowlessFileIsConflict(t *testing.T) {
	f := newFx(t)
	set := f.set(true)
	write(t, set.Abs("notes.txt"), "not picmover's")
	var failures []Failure
	st := f.run(Options{OnFailure: func(x Failure) { failures = append(failures, x) }})
	if st.FailedCount != 1 || len(failures) != 1 || failures[0].Path != "notes.txt" ||
		!strings.Contains(failures[0].Error, "does not know") || st.ExitCode() != 1 {
		t.Fatalf("stats %+v failures %+v", st, failures)
	}
	if readFile(t, set.Abs("notes.txt")) != "not picmover's" {
		t.Fatal("the unknown file was replaced")
	}
	if p := previousFiles(t, set); len(p) != 0 {
		t.Fatalf("moved into previous/: %v", p)
	}
	if _, ok := rowsOf(t, set)["notes.txt"]; ok {
		t.Fatal("row for a conflicting file")
	}
	// A file with the right content is adopted (an interrupted run).
	write(t, set.Abs("notes.txt"), "hello")
	if st := f.run(Options{}); st.Adopted != 1 || st.FailedCount != 0 {
		t.Fatalf("adopt %+v", st)
	}
	// A move onto a row-less file is a conflict too.
	os.MkdirAll(f.lib.Abs("moved"), 0o755)
	os.Rename(f.lib.Abs("notes.txt"), f.lib.Abs("moved/notes.txt"))
	write(t, set.Abs("moved/notes.txt"), "someone else's")
	st = f.run(Options{})
	if st.FailedCount != 1 || st.Failures[0].Path != "moved/notes.txt" {
		t.Fatalf("move conflict %+v", st)
	}
	if readFile(t, set.Abs("moved/notes.txt")) != "someone else's" || readFile(t, set.Abs("notes.txt")) != "hello" {
		t.Fatal("move conflict changed files")
	}
	for _, p := range previousFiles(t, set) {
		if !strings.HasPrefix(p, "user.db.") {
			t.Fatalf("previous/ %v", p)
		}
	}
}

// H1: the media.db snapshot is put in place without replacing: the old
// one goes aside first and is removed once the new one is in.
func TestH1MediaSnapshotNoReplace(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	write(t, set.Abs(MediaSnapshot), "stale")
	st := f.run(Options{})
	if !st.MediaSnapshot || st.SnapshotError != "" {
		t.Fatalf("%+v", st)
	}
	if readFile(t, set.Abs(MediaSnapshot)) == "stale" {
		t.Fatal("media.db snapshot not renewed")
	}
	for _, p := range previousFiles(t, set) {
		if strings.HasPrefix(p, MediaSnapshot) {
			t.Fatalf("old media.db snapshot kept: %s", p)
		}
	}
	// A folder where the snapshot goes is refused, not replaced.
	os.Remove(set.Abs(MediaSnapshot))
	os.Mkdir(set.Abs(MediaSnapshot), 0o755)
	if st := f.run(Options{}); st.SnapshotError == "" {
		t.Fatal("a folder at media.db was accepted")
	}
}

// H2: prune, restore and a repairing scrub refuse a set of another
// library inside the package, and a set nobody owns yet.
func TestH2OwnershipInsidePackage(t *testing.T) {
	f := newFx(t)
	set := f.set(true)
	src := f.src()
	if _, err := Prune(f.ctx, src, set, PruneOptions{Keep: DefaultKeep, Owner: f.id}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("prune unowned: %v", err)
	}
	f.run(Options{})
	penny := Identity{LibraryID: "aaaaaaaaaaaaaaaa", Instance: "bbbbbbbbbbbbbbbb", Root: "/w/penny", Host: "h"}
	os.Remove(f.lib.Abs("notes.txt")) // an extra prune would remove
	_, err := Prune(f.ctx, src, set, PruneOptions{Keep: DefaultKeep, Owner: penny, Expected: 1})
	var ole *OtherLibraryError
	if !errors.As(err, &ole) || ole.Name != "photos" || !errors.Is(err, ErrOtherLibrary) {
		t.Fatalf("prune of another library's set: %v", err)
	}
	if _, err := os.Stat(set.Abs("notes.txt")); err != nil {
		t.Fatal("pruned another library's file")
	}
	empty, _ := library.Open(filepath.Join(t.TempDir(), "penny"))
	if _, err := Restore(f.ctx, set, empty, RestoreOptions{Owner: &penny}); !errors.Is(err, ErrOtherLibrary) {
		t.Fatalf("restore: %v", err)
	}
	if ents, _ := os.ReadDir(empty.Root()); len(ents) > 1 {
		t.Fatalf("restore wrote %v", ents)
	}
	if _, err := Scrub(f.ctx, set, ScrubOptions{Repair: true, Source: &src, Owner: penny}); !errors.Is(err, ErrOtherLibrary) {
		t.Fatalf("scrub repair: %v", err)
	}
	// Without repair a scrub reads any set.
	if _, err := Scrub(f.ctx, set, ScrubOptions{}); err != nil {
		t.Fatal(err)
	}
	// The stored-name path of ResolveSetName checks the owner too.
	if _, err := ResolveSetName(f.ctx, f.target, "photos", penny.LibraryID, "/x/photos"); !errors.Is(err, ErrOtherLibrary) {
		t.Fatalf("stored name: %v", err)
	}
}

// H2: Claim is atomic: of two libraries claiming a new set at once,
// exactly one wins.
func TestH2ClaimAtomic(t *testing.T) {
	for range 5 {
		f := newFx(t)
		s1 := f.set(true)
		s2 := f.set(false)
		ids := []Identity{
			{LibraryID: "1111111111111111", Instance: "1111111111111111"},
			{LibraryID: "2222222222222222", Instance: "2222222222222222"},
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, s := range []*Set{s1, s2} {
			wg.Go(func() { errs[i] = s.Claim(f.ctx, ids[i], false, false) })
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("both or neither claimed: %v %v", errs[0], errs[1])
		}
		owner, _ := s1.Owner(f.ctx)
		winner := 0
		if errs[0] != nil {
			winner = 1
		}
		if owner.LibraryID != ids[winner].LibraryID || owner.Instance != ids[winner].Instance {
			t.Fatalf("owner %+v, winner %d", owner, winner)
		}
	}
}

// M1: a copy of the library (same library id, another instance) is
// refused with the folder and host of the last writer, and may take the
// set over; the original is then refused in turn.
func TestM1ForeignInstance(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	if o, _ := set.Owner(f.ctx); o.Instance != f.id.Instance || o.Root != f.id.Root || o.Host != f.id.Host {
		t.Fatalf("owner %+v, want %+v", o, f.id)
	}
	laptop := f.id
	laptop.Instance, laptop.Root, laptop.Host = "cccccccccccccccc", "/w2/laptop", "otherhost"
	_, err := Run(f.ctx, f.src(), set, Options{Owner: laptop})
	var fe *ForeignError
	if !errors.As(err, &fe) || fe.Root != f.id.Root || !errors.Is(err, ErrForeignInstance) ||
		!strings.Contains(err.Error(), "last written by the library at "+f.id.Root+" on ") {
		t.Fatalf("copy: %v", err)
	}
	if err := set.CheckOwner(f.ctx, laptop, true); !errors.Is(err, ErrForeignInstance) {
		t.Fatalf("check: %v", err)
	}
	if err := CheckSetOwner(f.ctx, f.target, "photos", laptop); !errors.Is(err, ErrForeignInstance) {
		t.Fatalf("check set: %v", err)
	}
	src := f.src()
	if _, err := Prune(f.ctx, src, set, PruneOptions{Keep: DefaultKeep, Owner: laptop}); !errors.Is(err, ErrForeignInstance) {
		t.Fatalf("prune: %v", err)
	}
	if _, err := Scrub(f.ctx, set, ScrubOptions{Repair: true, Source: &src, Owner: laptop}); !errors.Is(err, ErrForeignInstance) {
		t.Fatalf("repair: %v", err)
	}
	empty, _ := library.Open(filepath.Join(t.TempDir(), "x"))
	if _, err := Restore(f.ctx, set, empty, RestoreOptions{Owner: &laptop}); !errors.Is(err, ErrForeignInstance) {
		t.Fatalf("restore: %v", err)
	}
	// Take over.
	if _, err := Run(f.ctx, f.src(), set, Options{Owner: laptop, TakeOver: true}); err != nil {
		t.Fatalf("take over: %v", err)
	}
	if o, _ := set.Owner(f.ctx); o.Instance != laptop.Instance || o.Root != "/w2/laptop" || o.Host != "otherhost" {
		t.Fatalf("after take over %+v", o)
	}
	_, err = Run(f.ctx, f.src(), set, Options{Owner: f.id})
	if !errors.As(err, &fe) || fe.Root != "/w2/laptop" || fe.Host != "otherhost" {
		t.Fatalf("original after take over: %v", err)
	}
	// A set written before instances existed (no instance recorded) is
	// adopted silently.
	set.SetMeta(f.ctx, metaInstance, "")
	if _, err := Run(f.ctx, f.src(), set, Options{Owner: f.id}); err != nil {
		t.Fatalf("legacy set: %v", err)
	}
}

// M1 end to end: a copied library folder gets its own instance from
// userdata and is refused.
func TestM1CopiedLibrary(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	cp := filepath.Join(t.TempDir(), "laptop", userdata.FileName)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	if err := f.user.Snapshot(f.ctx, cp); err != nil {
		t.Fatal(err)
	}
	u, err := userdata.Open(f.ctx, cp)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	id, err := u.Identity(f.ctx)
	if err != nil || id.LibraryID != f.id.LibraryID || id.Instance == f.id.Instance {
		t.Fatalf("copy identity %+v %v", id, err)
	}
	if _, err := Run(f.ctx, f.src(), f.set(false), Options{Owner: id}); !errors.Is(err, ErrForeignInstance) {
		t.Fatalf("copy backed up into the original's set: %v", err)
	}
}

// M2: an unreadable folder makes the scan incomplete: the backup exits
// 1 and lists it, prune refuses (unless forced), and catalog files
// missing in the library refuse prune too.
func TestM2IncompleteScan(t *testing.T) {
	skipIfRootOrWindows(t)
	f := newFx(t)
	f.file("image/2018/notes.xmp", "sidecar 2018")
	f.run(Options{})
	set := f.set(false)
	dir := f.lib.Abs("image/2018")
	os.Chmod(dir, 0)
	defer os.Chmod(dir, 0o755)
	plan, err := PlanFor(f.ctx, f.src(), set)
	if err != nil || len(plan.Errors) != 1 || plan.Errors[0].Path != "image/2018" || plan.Extra.Files != 1 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if strings.Contains(plan.Errors[0].Error, f.lib.Root()) {
		t.Fatal("absolute path in the error")
	}
	st := f.run(Options{})
	if st.ScanErrorCount != 1 || st.ExitCode() != 1 || st.ScanErrors[0].Path != "image/2018" {
		t.Fatalf("backup %+v", st)
	}
	_, err = Prune(f.ctx, f.src(), set, PruneOptions{Keep: DefaultKeep, Owner: f.id, Expected: 1})
	var ie *IncompleteScanError
	if !errors.As(err, &ie) || ie.Unreadable != 1 {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(set.Abs("image/2018/notes.xmp")); err != nil {
		t.Fatal("pruned the backup of a file that still exists")
	}
	if st, err := Prune(f.ctx, f.src(), set, PruneOptions{Keep: DefaultKeep, Owner: f.id, Expected: 1, Force: true}); err != nil || st.Removed != 1 {
		t.Fatalf("forced prune %+v %v", st, err)
	}
	os.Chmod(dir, 0o755)
	// A catalog file missing in the library refuses prune too.
	os.Remove(f.lib.Abs("image/2024/07/a.jpg"))
	if _, err := Prune(f.ctx, f.src(), set, PruneOptions{Keep: DefaultKeep, Owner: f.id}); !errors.Is(err, ErrIncompleteScan) {
		t.Fatalf("missing: %v", err)
	}
	if st := f.run(Options{}); st.MissingCount != 1 || st.ExitCode() != 1 {
		t.Fatalf("missing backup %+v", st)
	}
}

// M3: names that differ only in case or Unicode form: one is backed up,
// the other is a failure; runs are stable and nothing goes to previous/.
func TestM3NameCollisions(t *testing.T) {
	f := newFx(t)
	f.file("image/NOTE.txt", "upper")
	f.file("image/note.txt", "lower")
	if _, err := os.Stat(f.lib.Abs("image/NOTE.txt")); err != nil || readFile(t, f.lib.Abs("image/note.txt")) != "lower" ||
		readFile(t, f.lib.Abs("image/NOTE.txt")) != "upper" {
		t.Skip("the file system ignores case")
	}
	f.file("café.txt", "nfc")
	f.file("café.txt", "nfd")
	twoNorm := readFile(t, f.lib.Abs("café.txt")) == "nfc"
	st := f.run(Options{})
	set := f.set(false)
	wantFailed := int64(1)
	if twoNorm {
		wantFailed = 2
	}
	if st.CollisionCount != wantFailed || st.FailedCount != wantFailed || st.ExitCode() != 1 {
		t.Fatalf("stats %+v", st)
	}
	if f := st.Failures[len(st.Failures)-1]; f.Path != "image/note.txt" || !strings.Contains(f.Error, "image/NOTE.txt") {
		t.Fatalf("failures %+v", st.Failures)
	}
	rows := rowsOf(t, set)
	if r, ok := rows["image/NOTE.txt"]; !ok || readFile(t, set.Abs(r.RelPath)) != "upper" {
		t.Fatalf("rows %v", rows)
	}
	for range 2 {
		st = f.run(Options{})
		if st.Copied != 0 || st.CollisionCount != wantFailed {
			t.Fatalf("rerun %+v", st)
		}
	}
	for _, p := range previousFiles(t, set) {
		if !strings.HasPrefix(p, "user.db.") {
			t.Fatalf("a colliding file went to previous/: %s", p)
		}
	}
	// The left-out file is still the library's: its backups are never
	// extras.
	plan, _ := PlanFor(f.ctx, f.src(), set)
	if plan.Extra.Files != 0 || len(plan.Collisions) != int(wantFailed) {
		t.Fatalf("plan %+v", plan)
	}
}

// M4: a user-data restore that cannot copy the snapshot, or whose
// snapshot is damaged, leaves the library's user.db in place.
func TestM4RestoreUserDBSafe(t *testing.T) {
	f := newFx(t)
	if _, err := f.user.SetRating(f.ctx, []uint64{f.hashes["image/2024/07/a.jpg"]}, 4); err != nil {
		t.Fatal(err)
	}
	f.run(Options{})
	set := f.set(false)
	if v, _ := set.Meta(f.ctx, metaUserSnapshotHash); len(v) != 16 {
		t.Fatalf("snapshot hash %q", v)
	}
	target, _ := library.Open(filepath.Join(t.TempDir(), "restored"))
	userPath := filepath.Join(target.Root(), library.UserDBName)
	write(t, userPath, "")
	u, err := userdata.Open(f.ctx, userPath)
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	before := readFile(t, userPath)
	check := func(what string) {
		t.Helper()
		if readFile(t, userPath) != before {
			t.Fatalf("%s: user.db changed", what)
		}
		if baks, _ := filepath.Glob(userPath + ".bak-*"); len(baks) != 0 {
			t.Fatalf("%s: backup left %v", what, baks)
		}
		if ents, _ := os.ReadDir(target.TempDir()); len(ents) != 0 {
			t.Fatalf("%s: temp files %v", what, ents)
		}
	}
	// Unreadable snapshot.
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		os.Chmod(set.Abs(UserSnapshot), 0)
		if _, err := Restore(f.ctx, set, target, RestoreOptions{Mode: ModeUserData, Force: true}); err == nil {
			t.Fatal("unreadable snapshot restored")
		}
		os.Chmod(set.Abs(UserSnapshot), 0o644)
		check("unreadable")
	}
	// Damaged snapshot (does not match the recorded hash).
	good := readFile(t, set.Abs(UserSnapshot))
	write(t, set.Abs(UserSnapshot), good[:len(good)/2])
	if _, err := Restore(f.ctx, set, target, RestoreOptions{Mode: ModeUserData, Force: true}); !errors.Is(err, ErrBadUserSnapshot) {
		t.Fatalf("damaged: %v", err)
	}
	check("damaged")
	// Not a database at all, with no recorded hash.
	set.SetMeta(f.ctx, metaUserSnapshotHash, "")
	write(t, set.Abs(UserSnapshot), "garbage garbage garbage")
	if _, err := Restore(f.ctx, set, target, RestoreOptions{Mode: ModeUserData, Force: true}); !errors.Is(err, ErrBadUserSnapshot) {
		t.Fatalf("garbage: %v", err)
	}
	check("garbage")
	// A good one replaces it, keeping the old one.
	write(t, set.Abs(UserSnapshot), good)
	st, err := Restore(f.ctx, set, target, RestoreOptions{Mode: ModeUserData, Force: true})
	if err != nil || st.UserDB != "replaced" {
		t.Fatalf("good %+v %v", st, err)
	}
	if baks, _ := filepath.Glob(userPath + ".bak-*"); len(baks) == 0 {
		t.Fatal("the replaced user.db was not kept")
	}
	u, _ = userdata.Open(f.ctx, userPath)
	if r, _ := u.Rating(f.ctx, f.hashes["image/2024/07/a.jpg"]); r != 4 {
		t.Fatal("rating not restored")
	}
	u.Close()
}

// L4/H1: a run killed between moving the old user.db snapshot to
// previous/ and putting the new one in place: restore uses the newest
// snapshot in previous/.
func TestUserSnapshotFallback(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	set.now = func() time.Time { return time.Now().Add(-time.Hour) }
	if _, err := set.keepPrevious(UserSnapshot); err != nil {
		t.Fatal(err)
	}
	empty, _ := library.Open(filepath.Join(t.TempDir(), "r"))
	st, err := Restore(f.ctx, set, empty, RestoreOptions{})
	if err != nil || st.UserDB != "restored" {
		t.Fatalf("fallback %+v %v", st, err)
	}
	if err := userdata.IntegrityCheck(f.ctx, filepath.Join(empty.Root(), library.UserDBName)); err != nil {
		t.Fatal(err)
	}
}

// L1: a tmp or logs folder that is a link is never followed (the temp
// cleanup would delete files outside the set).
func TestL1LinkedTmpRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges")
	}
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "copy-precious.part"), "keep")
	os.RemoveAll(set.TmpDir())
	os.Symlink(outside, set.TmpDir())
	_, err := Run(f.ctx, f.src(), set, Options{Owner: f.id})
	if !errors.Is(err, errNotRealDir) {
		t.Fatalf("linked tmp: %v", err)
	}
	if readFile(t, filepath.Join(outside, "copy-precious.part")) != "keep" {
		t.Fatal("followed the link")
	}
	os.Remove(set.TmpDir())
	logs := t.TempDir()
	os.RemoveAll(set.LogDir())
	os.Symlink(logs, set.LogDir())
	f.run(Options{})
	if ents, _ := os.ReadDir(logs); len(ents) != 0 {
		t.Fatalf("logged through a link: %v", ents)
	}
}

// L2: keep 0 keeps no earlier versions, in the plan and in the prune.
func TestL2KeepZero(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	write(t, set.previousPath("x.txt", time.Now().Add(-time.Hour)), "recent")
	plan, _ := PlanFor(f.ctx, f.src(), set)
	pp, _ := PlanPrune(set, plan, 0, time.Now())
	// x.txt and the user.db snapshot of the first run? Only x.txt is old
	// enough to be listed by second-resolution names; count what is there.
	st, err := Prune(f.ctx, f.src(), set, PruneOptions{Keep: 0, Owner: f.id, Expected: pp.Count})
	if err != nil || st.Removed != int64(pp.Count) || pp.Count < 1 {
		t.Fatalf("keep 0: %+v %v (plan %d)", st, err, pp.Count)
	}
	if _, err := Prune(f.ctx, f.src(), set, PruneOptions{Keep: -1, Owner: f.id}); err == nil {
		t.Fatal("negative keep accepted")
	}
}

// L4: restore never creates folders outside the library through a link.
func TestL4RestoreNoMkdirOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges")
	}
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	target, _ := library.Open(filepath.Join(t.TempDir(), "r"))
	outside := t.TempDir()
	os.Symlink(outside, target.Abs("image"))
	st, err := Restore(f.ctx, set, target, RestoreOptions{})
	if err != nil || st.FailedCount != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("created outside the library: %v", ents)
	}
}

// L4: a moved file whose old copy is gone from the target is copied
// again and the row at its old path is dropped.
func TestL4MoveFallbackDropsOldRow(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	os.Remove(set.Abs("image/2024/07/a.jpg"))
	os.MkdirAll(f.lib.Abs("moved"), 0o755)
	os.Rename(f.lib.Abs("image/2024/07/a.jpg"), f.lib.Abs("moved/a.jpg"))
	f.db.UpdateRelPath(f.ctx, f.hashes["image/2024/07/a.jpg"], "moved/a.jpg")
	st := f.run(Options{})
	if st.Copied != 1 || st.FailedCount != 0 {
		t.Fatalf("%+v", st)
	}
	rows := rowsOf(t, set)
	if _, ok := rows["image/2024/07/a.jpg"]; ok {
		t.Fatal("old row kept")
	}
	if _, ok := rows["moved/a.jpg"]; !ok {
		t.Fatal("new row missing")
	}
}

// L4: a target that disappears is reported as such, not as a link.
func TestL4TargetGone(t *testing.T) {
	f := newFx(t)
	set := f.set(true)
	os.RemoveAll(f.target.Root)
	if err := set.within(set.Abs("a/b.jpg")); !errors.Is(err, errTargetGone) || strings.Contains(PublicError(err), "link") {
		t.Fatalf("gone: %v", err)
	}
}

// L4: restoring into a library whose user.db belongs to another library
// says so.
func TestL4OtherUserDBWarned(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	other, _ := library.Open(filepath.Join(t.TempDir(), "o"))
	u, err := userdata.Open(f.ctx, filepath.Join(other.Root(), library.UserDBName))
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
	st, err := Restore(f.ctx, f.set(false), other, RestoreOptions{})
	if err != nil || st.UserDB != "present" || st.UserDBWarning == "" {
		t.Fatalf("%+v %v", st, err)
	}
}

// B3: the plan's restore class is what restore --mode missing copies.
func TestB3RestoreClass(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	os.Remove(f.lib.Abs("notes.txt"))                                             // gone: restorable (and extra)
	os.Remove(f.lib.Abs("image/2023/01/b.jpg"))                                   // catalog row still there: restorable, not extra
	os.MkdirAll(f.lib.Abs("elsewhere"), 0o755)                                    //
	os.Rename(f.lib.Abs("video/2022/05/c.mp4"), f.lib.Abs("elsewhere/c.mp4"))     // moved: not restorable
	f.db.UpdateRelPath(f.ctx, f.hashes["video/2022/05/c.mp4"], "elsewhere/c.mp4") //
	plan, err := PlanFor(f.ctx, f.src(), set)
	if err != nil || plan.Restore.Files != 2 || len(plan.RestorePaths) != 2 ||
		plan.RestorePaths[0] != "image/2023/01/b.jpg" || plan.RestorePaths[1] != "notes.txt" {
		t.Fatalf("restore class %+v %v", plan.RestorePaths, err)
	}
	st, err := Restore(context.Background(), set, f.lib, RestoreOptions{DryRun: true})
	if err != nil || st.Restored != plan.Restore.Files {
		t.Fatalf("dry run %+v %v", st, err)
	}
}
