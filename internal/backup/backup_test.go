package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// fx is a library with a catalog and a user.db, and a target.
type fx struct {
	t      *testing.T
	ctx    context.Context
	lib    *library.Library
	db     *catalog.DB
	user   *userdata.DB
	target *Target
	libID  string
	hashes map[string]uint64 // by rel
}

func newFx(t *testing.T) *fx {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	lib, err := library.Open(filepath.Join(base, "photos"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { user.Close() })
	id, err := user.LibraryID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tdir := filepath.Join(base, "drive")
	os.Mkdir(tdir, 0o755)
	tg, created, err := InitTarget(tdir)
	if err != nil || !created {
		t.Fatalf("init target %v %v", created, err)
	}
	f := &fx{t: t, ctx: ctx, lib: lib, db: db, user: user, target: tg, libID: id, hashes: map[string]uint64{}}
	f.media("image/2024/07/a.jpg", "AAAA", 2024)
	f.media("image/2023/01/b.jpg", "BBBBBB", 2023)
	f.media("video/2022/05/c.mp4", "CCCCCCCCCC", 2022)
	f.file("image/2024/07/a.xmp", "sidecar")
	f.file("notes.txt", "hello")
	return f
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// media writes a file and records it in the catalog.
func (f *fx) media(rel, content string, year int) {
	f.t.Helper()
	write(f.t, f.lib.Abs(rel), content)
	h, n, _ := library.HashFile(f.lib.Abs(rel))
	m := catalog.Media{Hash: h, Size: n, RelPath: rel, FileType: strings.Split(rel, "/")[0], Ext: "jpg",
		TakenAt: time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)}
	if ok, err := f.db.InsertMedia(f.ctx, nil, &m); err != nil || !ok {
		f.t.Fatalf("insert %s: %v", rel, err)
	}
	f.hashes[rel] = h
}

// file writes a non-media file.
func (f *fx) file(rel, content string) {
	f.t.Helper()
	write(f.t, f.lib.Abs(rel), content)
}

func (f *fx) src() Source { return Source{Lib: f.lib, Catalog: f.db, User: f.user} }

func (f *fx) set(create bool) *Set {
	f.t.Helper()
	s, err := OpenSet(f.ctx, f.target, "photos", create)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { s.Close() })
	return s
}

func (f *fx) run(opts Options) Stats {
	f.t.Helper()
	opts.LibraryID = f.libID
	st, err := Run(f.ctx, f.src(), f.set(true), opts)
	if err != nil {
		f.t.Fatalf("run: %v (%+v)", err, st)
	}
	return st
}

func rowsOf(t *testing.T, s *Set) map[string]FileRow {
	t.Helper()
	rows, err := s.Files(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]FileRow{}
	for _, r := range rows {
		out[r.RelPath] = r
	}
	return out
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMarker(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenTarget(dir); !errors.Is(err, ErrNoMarker) {
		t.Fatalf("no marker: %v", err)
	}
	os.Mkdir(filepath.Join(dir, "lost+found"), 0o755)
	os.Mkdir(filepath.Join(dir, "System Volume Information"), 0o755)
	tg, created, err := InitTarget(dir)
	if err != nil || !created || !ValidID(tg.ID()) {
		t.Fatalf("init %v %v %v", tg, created, err)
	}
	tg2, created, err := InitTarget(dir)
	if err != nil || created || tg2.ID() != tg.ID() {
		t.Fatalf("second init %v %v", created, err)
	}
	var m map[string]any
	json.Unmarshal([]byte(readFile(t, filepath.Join(dir, MarkerName))), &m)
	if m["id"] != tg.ID() || m["format"] != 1.0 || m["created"] == "" {
		t.Fatalf("marker %v", m)
	}
	other := t.TempDir()
	write(t, filepath.Join(other, "x.txt"), "x")
	if _, _, err := InitTarget(other); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("non-empty: %v", err)
	}
	if _, _, err := InitTarget(filepath.Join(other, "x.txt")); !errors.Is(err, ErrNotDir) {
		t.Fatalf("file: %v", err)
	}
	for content, want := range map[string]error{
		`{"id":"XYZ","format":1}`:                ErrBadMarker,
		`not json`:                               ErrBadMarker,
		`{"id":"0123456789abcdef","format":2}`:   ErrNewerMarker,
		`{"id":"0123456789ABCDEF","format":1}`:   ErrBadMarker,
		`{"id":"0123456789abcdef","format":1}`:   nil,
		`{"id":"0123456789abcdef"}`:              ErrBadMarker,
		strings.Repeat(" ", 5000) + `{"id":"x"}`: ErrBadMarker,
	} {
		d := t.TempDir()
		write(t, filepath.Join(d, MarkerName), content)
		_, err := ReadMarker(d)
		if (want == nil && err != nil) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("marker %.40q: %v, want %v", content, err, want)
		}
	}
	for id, ok := range map[string]bool{"0123456789abcdef": true, "0123456789abcde": false, "0123456789abcdeg": false,
		"../../etc/passw": false, "": false} {
		if ValidID(id) != ok {
			t.Errorf("ValidID(%q) != %v", id, ok)
		}
	}
}

func TestCheckSeparate(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	os.MkdirAll(filepath.Join(lib, "sub"), 0o755)
	tg := filepath.Join(base, "tg")
	os.Mkdir(tg, 0o755)
	if err := CheckSeparate(tg, lib); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{lib, filepath.Join(lib, "sub"), base} {
		if err := CheckSeparate(bad, lib); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(base, "link")
		os.Symlink(filepath.Join(lib, "sub"), link)
		if err := CheckSeparate(link, lib); err == nil {
			t.Error("a link into the library accepted")
		}
	}
}

func TestMakePlanClasses(t *testing.T) {
	sc := &Scan{
		Entries: []Entry{
			{Rel: "a.jpg", Hash: 1, HashKnown: true, Size: 10, Media: true},       // current
			{Rel: "new/b.jpg", Hash: 2, HashKnown: true, Size: 20, Media: true},   // moved from old/b.jpg
			{Rel: "c.jpg", Hash: 3, HashKnown: true, Size: 30, Media: true},       // changed (row has 33)
			{Rel: "d.jpg", Hash: 4, HashKnown: true, Size: 40, Media: true},       // new
			{Rel: "e.xmp", Size: 5, MTime: 100},                                   // current non-media
			{Rel: "f.txt", Size: 6, MTime: 200},                                   // changed non-media (mtime)
			{Rel: "g.txt", Size: 7, MTime: 300},                                   // new non-media
			{Rel: "A.JPG2", Hash: 9, HashKnown: true, Size: 1, Media: true},       // current, NOCASE
			{Rel: "dup.jpg", Hash: 1, HashKnown: true, Size: 10, Media: true},     // new: hash 1's row is in use
			{Rel: ".picmover/trash/t.jpg", Hash: 7, HashKnown: true, Media: true}, // new
		},
		CatalogHashes: map[uint64]struct{}{1: {}, 2: {}, 3: {}, 4: {}, 9: {}, 7: {}, 50: {}},
	}
	rows := []FileRow{
		{RelPath: "a.jpg", Hash: 1, Size: 10},
		{RelPath: "old/b.jpg", Hash: 2, Size: 20},
		{RelPath: "c.jpg", Hash: 33, Size: 30},
		{RelPath: "e.xmp", Hash: 55, Size: 5, MTime: 100},
		{RelPath: "f.txt", Hash: 66, Size: 6, MTime: 199},
		{RelPath: "a.jpg2", Hash: 9, Size: 1},
		{RelPath: "gone.jpg", Hash: 99, Size: 1000},      // extra
		{RelPath: "trashed-elsewhere", Hash: 50, Size: 1}, // not extra: the catalog has the hash
		{RelPath: "e-copy.xmp", Hash: 55, Size: 5},        // not extra: e.xmp has the hash
	}
	p := MakePlan(sc, rows)
	check := func(name string, c Count, files, bytes int64) {
		if c.Files != files || c.Bytes != bytes {
			t.Errorf("%s = %+v, want %d files %d bytes", name, c, files, bytes)
		}
	}
	check("current", p.Current, 3, 16)
	check("moved", p.Moved, 1, 20)
	check("changed", p.Changed, 2, 36)
	check("new", p.New, 4, 57)
	check("extra", p.Extra, 1, 1000)
	if p.Items[0].Class != ClassMoved || p.Items[0].From != "old/b.jpg" || p.Items[0].Rel != "new/b.jpg" {
		t.Errorf("first item %+v", p.Items[0])
	}
	if len(p.Extras) != 1 || p.Extras[0].RelPath != "gone.jpg" {
		t.Errorf("extras %+v", p.Extras)
	}
	var order []string
	for _, it := range p.Items[1:] {
		order = append(order, it.Rel)
	}
	if want := []string{"c.jpg", "d.jpg", "f.txt", "g.txt", "dup.jpg", ".picmover/trash/t.jpg"}; !slices.Equal(order, want) {
		t.Errorf("copy order %v, want %v", order, want)
	}
}

func TestBackupRunAndIncremental(t *testing.T) {
	f := newFx(t)
	// A trashed file, a symlink and picmover's own files.
	write(t, f.lib.TrashPath("image/2020/01/t.jpg"), "TRASHED")
	h, n, _ := library.HashFile(f.lib.TrashPath("image/2020/01/t.jpg"))
	f.db.InsertMedia(f.ctx, nil, &catalog.Media{Hash: h, Size: n, RelPath: "image/2020/01/t.jpg", FileType: "image",
		Ext: "jpg", DeletedAt: time.Now()})
	write(t, filepath.Join(f.lib.Root(), library.MetaDir, "logs", "x.log"), "log")
	write(t, filepath.Join(f.lib.Root(), "media.db.bak-20240101-000000"), "bak")
	if runtime.GOOS != "windows" {
		os.Symlink(f.lib.Abs("notes.txt"), f.lib.Abs("link.txt"))
	}
	var progress []Progress
	st := f.run(Options{OnProgress: func(p Progress) { progress = append(progress, p) }, Library: f.lib.Root()})
	if st.Copied != 6 || st.FailedCount != 0 || st.New != 6 || !st.UserSnapshot || !st.MediaSnapshot || st.ExitCode() != 0 {
		t.Fatalf("first run %+v", st)
	}
	if runtime.GOOS != "windows" && (st.SymlinkCount != 1 || st.Symlinks[0] != "link.txt") {
		t.Fatalf("symlinks %+v", st.Symlinks)
	}
	if len(progress) == 0 || progress[len(progress)-1].Phase != PhaseSnapshot {
		t.Fatalf("progress %+v", progress)
	}
	set := f.set(false)
	rows := rowsOf(t, set)
	want := []string{"image/2024/07/a.jpg", "image/2023/01/b.jpg", "video/2022/05/c.mp4", "image/2024/07/a.xmp", "notes.txt",
		".picmover/trash/image/2020/01/t.jpg"}
	for _, rel := range want {
		r, ok := rows[rel]
		if !ok {
			t.Fatalf("no row for %s: %v", rel, rows)
		}
		if got := readFile(t, set.Abs(rel)); got != readFile(t, libPath(f.lib.Root(), rel)) {
			t.Fatalf("%s content %q", rel, got)
		}
		if r.VerifiedAt != 0 || r.BackedAt == 0 {
			t.Fatalf("row %+v", r)
		}
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %v", rows)
	}
	for _, p := range []string{"link.txt", ".picmover/logs/x.log", "media.db.bak-20240101-000000"} {
		if _, err := os.Lstat(set.Abs(p)); err == nil {
			t.Errorf("%s backed up", p)
		}
	}
	for _, p := range []string{UserSnapshot, MediaSnapshot} {
		if fi, err := os.Stat(set.Abs(p)); err != nil || fi.Size() == 0 {
			t.Errorf("snapshot %s: %v", p, err)
		}
	}
	// mtime preserved.
	lfi, _ := os.Stat(f.lib.Abs("notes.txt"))
	sfi, _ := os.Stat(set.Abs("notes.txt"))
	if !lfi.ModTime().Equal(sfi.ModTime()) {
		t.Error("mtime not preserved")
	}
	if runs, _ := set.Runs(f.ctx, 10); len(runs) != 1 || runs[0].Kind != "backup" || runs[0].FinishedAt.IsZero() {
		t.Fatalf("runs %+v", runs)
	}

	// Nothing changed: nothing copied; user.db snapshot kept in previous/.
	st = f.run(Options{})
	if st.Copied != 0 || st.Current != 6 || st.ExitCode() != 0 {
		t.Fatalf("second run %+v", st)
	}
	if ents, _ := os.ReadDir(set.PreviousDir()); len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "user.db.") {
		t.Fatalf("previous %v", ents)
	}

	// Move a photo, change a non-media file, delete one.
	os.MkdirAll(f.lib.Abs("image/2024/08"), 0o755)
	os.Rename(f.lib.Abs("image/2024/07/a.jpg"), f.lib.Abs("image/2024/08/a.jpg"))
	f.db.UpdateRelPath(f.ctx, f.hashes["image/2024/07/a.jpg"], "image/2024/08/a.jpg")
	write(t, f.lib.Abs("notes.txt"), "hello, changed")
	os.Remove(f.lib.Abs("video/2022/05/c.mp4"))
	f.db.Remove(f.ctx, f.hashes["video/2022/05/c.mp4"])
	plan, err := PlanFor(f.ctx, f.src(), set)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Moved.Files != 1 || plan.Changed.Files != 1 || plan.Extra.Files != 1 || plan.New.Files != 0 {
		t.Fatalf("plan %+v", plan)
	}
	st = f.run(Options{})
	if st.Renamed != 1 || st.Copied != 1 || st.Extra != 1 || st.ExitCode() != 0 {
		t.Fatalf("third run %+v", st)
	}
	rows = rowsOf(t, set)
	if _, ok := rows["image/2024/07/a.jpg"]; ok {
		t.Fatal("old path row kept")
	}
	if r := rows["image/2024/08/a.jpg"]; r.Hash != f.hashes["image/2024/07/a.jpg"] {
		t.Fatalf("moved row %+v", r)
	}
	if readFile(t, set.Abs("notes.txt")) != "hello, changed" {
		t.Fatal("changed file not copied")
	}
	prev, _ := filepath.Glob(filepath.Join(set.PreviousDir(), "notes.txt.*"))
	if len(prev) != 1 || readFile(t, prev[0]) != "hello" {
		t.Fatalf("previous version %v", prev)
	}
	if readFile(t, set.Abs("video/2022/05/c.mp4")) != "CCCCCCCCCC" {
		t.Fatal("a backup run removed a file")
	}

	// Touching a non-media file without changing it: refreshed, no copy.
	later := time.Now().Add(time.Hour)
	os.Chtimes(f.lib.Abs("image/2024/07/a.xmp"), later, later)
	st = f.run(Options{})
	if st.Refreshed != 1 || st.Copied != 0 {
		t.Fatalf("touch run %+v", st)
	}
}

func TestBackupOtherLibrary(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	_, err := Run(f.ctx, f.src(), f.set(false), Options{LibraryID: "ffffffffffffffff"})
	if !errors.Is(err, ErrOtherLibrary) {
		t.Fatalf("other library: %v", err)
	}
	if name, _ := FindSet(f.ctx, f.target, f.libID); name != "photos" {
		t.Fatalf("FindSet %q", name)
	}
	if owner, _ := SetOwner(f.ctx, f.target, "photos"); owner != f.libID {
		t.Fatalf("owner %q", owner)
	}
}

func TestBackupVerificationFailure(t *testing.T) {
	f := newFx(t)
	// The file no longer matches its catalog hash.
	write(t, f.lib.Abs("image/2023/01/b.jpg"), "bitrot")
	var failures []Failure
	st := f.run(Options{OnFailure: func(x Failure) { failures = append(failures, x) }})
	if st.FailedCount != 1 || st.Copied != 4 || st.ExitCode() != 1 || len(failures) != 1 ||
		failures[0].Path != "image/2023/01/b.jpg" || strings.Contains(failures[0].Error, f.lib.Root()) {
		t.Fatalf("stats %+v failures %+v", st, failures)
	}
	set := f.set(false)
	if _, ok := rowsOf(t, set)["image/2023/01/b.jpg"]; ok {
		t.Fatal("row for a failed copy")
	}
	if _, err := os.Lstat(set.Abs("image/2023/01/b.jpg")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a failed copy left a file")
	}
	if ents, _ := os.ReadDir(set.TmpDir()); len(ents) != 0 {
		t.Fatalf("temp files left %v", ents)
	}
	// A file missing from the library is reported.
	os.Remove(f.lib.Abs("notes.txt"))
	os.Remove(f.lib.Abs("image/2024/07/a.jpg"))
	st = f.run(Options{})
	if st.MissingCount != 1 || st.Missing[0] != "image/2024/07/a.jpg" {
		t.Fatalf("missing %+v", st)
	}
}

func TestBackupResumeAfterKill(t *testing.T) {
	f := newFx(t)
	for i := range 150 {
		f.media(fmt.Sprintf("image/2010/01/x%03d.jpg", i), fmt.Sprintf("content %d", i), 2010)
	}
	crashAfter = func(done int) bool { return done == 100 }
	_, err := Run(f.ctx, f.src(), f.set(true), Options{LibraryID: f.libID, Workers: 3})
	crashAfter = nil
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("crash: %v", err)
	}
	set := f.set(false)
	rows := rowsOf(t, set)
	// Rows are written in batches of 64: the first batch survived.
	if len(rows) != rowBatch {
		t.Fatalf("%d rows after the kill", len(rows))
	}
	// Every row's file is on the target with the right content.
	for rel, r := range rows {
		h, _, err := library.HashFile(set.Abs(rel))
		if err != nil || h != r.Hash {
			t.Fatalf("row %s without its file: %v", rel, err)
		}
	}
	if runs, _ := set.Runs(f.ctx, 5); len(runs) != 1 || !runs[0].FinishedAt.IsZero() {
		t.Fatalf("runs %+v", runs)
	}
	st := f.run(Options{})
	// The files copied but not recorded are verified by hash, not copied.
	if st.Adopted < 100-rowBatch || st.Copied+st.Adopted != 155-rowBatch || st.FailedCount != 0 {
		t.Fatalf("resume %+v", st)
	}
	if n := len(rowsOf(t, set)); n != 155 {
		t.Fatalf("%d rows after resuming", n)
	}
}

func TestBackupFreeSpace(t *testing.T) {
	f := newFx(t)
	defer func() { freeSpace = diskFree }()
	snap := snapshotBytes(f.src())
	freeSpace = func(string) int64 { return 10 }
	_, err := Run(f.ctx, f.src(), f.set(true), Options{LibraryID: f.libID})
	var ns *NoSpaceError
	if !errors.As(err, &ns) || ns.Free != 10 || ns.Need < 30 {
		t.Fatalf("no space: %v", err)
	}
	if !strings.Contains(err.Error(), "free") {
		t.Fatal(err)
	}
	// Partial: room for the newest file (a.jpg, 4 bytes) and the snapshots.
	freeSpace = func(string) int64 { return snap + spaceMargin + 5 }
	st := f.run(Options{Partial: true})
	if !st.Partial || st.Copied != 1 || st.LeftFiles != 4 || st.ExitCode() != 1 {
		t.Fatalf("partial %+v", st)
	}
	rows := rowsOf(t, f.set(false))
	if _, ok := rows["image/2024/07/a.jpg"]; !ok {
		t.Fatalf("the newest file was not copied first: %v", rows)
	}
}

func TestScrub(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	// A clock that moves a minute per reading, so passes are distinct.
	clock := time.Now()
	set.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	// Budget so small that one file is read per run.
	st, err := Scrub(f.ctx, set, ScrubOptions{Budget: time.Nanosecond})
	if err != nil || st.Checked != 1 || st.PassComplete || st.Remaining != 4 {
		t.Fatalf("budget scrub %+v %v", st, err)
	}
	seen := map[string]bool{}
	for range 4 {
		st, err = Scrub(f.ctx, set, ScrubOptions{Budget: time.Nanosecond})
		if err != nil || st.Checked != 1 {
			t.Fatalf("%+v %v", st, err)
		}
	}
	if !st.PassComplete {
		t.Fatalf("pass not complete after 5 runs %+v", st)
	}
	for _, r := range rowsOf(t, set) {
		if r.VerifiedAt == 0 {
			t.Fatalf("not verified: %s", r.RelPath)
		}
		seen[r.RelPath] = true
	}
	// Damage one file, delete one, plant an unknown one.
	write(t, set.Abs("image/2023/01/b.jpg"), "BBBBBX")
	os.Remove(set.Abs("notes.txt"))
	write(t, set.Abs("stray.txt"), "?")
	st, err = Scrub(f.ctx, set, ScrubOptions{})
	if err != nil || st.DamagedCount != 1 || st.Damaged[0].Path != "image/2023/01/b.jpg" || st.Damaged[0].Repaired ||
		st.MissingCount != 1 || st.Missing[0] != "notes.txt" || st.UnknownCount != 1 || st.Unknown[0] != "stray.txt" ||
		st.ExitCode() != 1 || !st.PassComplete {
		t.Fatalf("damage %+v %v", st, err)
	}
	if r := rowsOf(t, set)["image/2023/01/b.jpg"]; r.VerifiedAt != 0 {
		t.Fatal("damaged row still verified")
	}
	// Repair from the library.
	src := f.src()
	st, err = Scrub(f.ctx, set, ScrubOptions{Repair: true, Source: &src})
	if err != nil || st.DamagedCount != 1 || !st.Damaged[0].Repaired || st.Repaired != 2 {
		t.Fatalf("repair %+v %v", st, err)
	}
	if readFile(t, set.Abs("image/2023/01/b.jpg")) != "BBBBBB" || readFile(t, set.Abs("notes.txt")) != "hello" {
		t.Fatal("not repaired")
	}
	prev, _ := filepath.Glob(filepath.Join(set.PreviousDir(), "image", "2023", "01", "b.jpg.*"))
	if len(prev) != 1 || readFile(t, prev[0]) != "BBBBBX" {
		t.Fatalf("damaged copy not kept %v", prev)
	}
	st, err = Scrub(f.ctx, set, ScrubOptions{})
	if err != nil || st.DamagedCount != 0 || st.MissingCount != 0 || st.ExitCode() != 0 {
		t.Fatalf("after repair %+v %v", st, err)
	}
	if runs, _ := set.Runs(f.ctx, 50); runs[0].Kind != "scrub" {
		t.Fatalf("runs %+v", runs)
	}
}

func TestPrune(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	os.Remove(f.lib.Abs("video/2022/05/c.mp4"))
	f.db.Remove(f.ctx, f.hashes["video/2022/05/c.mp4"])
	os.Remove(f.lib.Abs("notes.txt"))
	// A trashed file is not pruned while the catalog has it.
	os.MkdirAll(filepath.Dir(f.lib.TrashPath("image/2023/01/b.jpg")), 0o755)
	os.Rename(f.lib.Abs("image/2023/01/b.jpg"), f.lib.TrashPath("image/2023/01/b.jpg"))
	f.db.SetDeleted(f.ctx, f.hashes["image/2023/01/b.jpg"], time.Now())
	// Old and recent previous versions.
	old := set.previousPath("x.txt", time.Now().Add(-40*24*time.Hour))
	recent := set.previousPath("y.txt", time.Now().Add(-time.Hour))
	write(t, old, "old")
	write(t, recent, "recent")
	plan, _ := PlanFor(f.ctx, f.src(), set)
	pp, err := PlanPrune(set, plan, DefaultKeep, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range pp.Items {
		paths = append(paths, it.Path)
	}
	sort.Strings(paths)
	want := []string{".picmover/previous/x.txt." + strings.TrimPrefix(filepath.Ext(old), "."), "notes.txt", "video/2022/05/c.mp4"}
	if !slices.Equal(paths, want) || pp.Count != 3 {
		t.Fatalf("prune plan %v, want %v", paths, want)
	}
	if _, err := Prune(f.ctx, f.src(), set, PruneOptions{Expected: 2}); !errors.Is(err, ErrPruneCount) {
		t.Fatalf("wrong count: %v", err)
	}
	if _, err := os.Stat(set.Abs("notes.txt")); err != nil {
		t.Fatal("removed with a wrong count")
	}
	st, err := Prune(f.ctx, f.src(), set, PruneOptions{Expected: 3})
	if err != nil || st.Removed != 3 {
		t.Fatalf("prune %+v %v", st, err)
	}
	for _, p := range []string{set.Abs("notes.txt"), set.Abs("video/2022/05/c.mp4"), old, set.Abs("video")} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s not removed", p)
		}
	}
	for _, p := range []string{recent, set.Abs("image/2023/01/b.jpg"), set.Abs(UserSnapshot), set.Abs(MediaSnapshot)} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s removed", p)
		}
	}
	rows := rowsOf(t, set)
	if _, ok := rows["notes.txt"]; ok {
		t.Fatal("row kept")
	}
	if _, err := os.Stat(f.lib.Abs("image/2024/07/a.jpg")); err != nil {
		t.Fatal("prune touched the library")
	}
	// A link planted on the target is never followed by prune.
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "victim.txt")
		write(t, outside, "keep me")
		os.MkdirAll(set.Abs("evil"), 0o755)
		os.Remove(set.Abs("evil"))
		os.Symlink(filepath.Dir(outside), set.Abs("evil"))
		set.apply(f.ctx, []rowOp{{upsert: &FileRow{RelPath: "evil/victim.txt", Hash: 12345, Size: 7}}})
		plan, _ := PlanFor(f.ctx, f.src(), set)
		pp, _ := PlanPrune(set, plan, DefaultKeep, time.Now())
		st, _ := Prune(f.ctx, f.src(), set, PruneOptions{Expected: pp.Count})
		if readFile(t, outside) != "keep me" || len(st.Failures) != 1 {
			t.Fatalf("prune followed a link: %+v", st)
		}
	}
}

func TestRestore(t *testing.T) {
	f := newFx(t)
	if _, err := f.user.SetRating(f.ctx, []uint64{f.hashes["image/2024/07/a.jpg"]}, 5); err != nil {
		t.Fatal(err)
	}
	f.run(Options{})
	set := f.set(false)

	// Into an empty library: everything comes back, user.db too, and the
	// caller is told to rebuild.
	empty, _ := library.Open(filepath.Join(t.TempDir(), "restored"))
	dry, err := Restore(f.ctx, set, empty, RestoreOptions{DryRun: true})
	if err != nil || dry.Restored != 5 || dry.UserDB != "restored" || len(dry.Paths) != 5 {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	if _, err := os.Stat(empty.Abs("notes.txt")); err == nil {
		t.Fatal("dry run copied")
	}
	st, err := Restore(f.ctx, set, empty, RestoreOptions{})
	if err != nil || st.Restored != 5 || st.UserDB != "restored" || !st.NeedsRebuild || st.ExitCode() != 0 {
		t.Fatalf("restore %+v %v", st, err)
	}
	if readFile(t, empty.Abs("image/2024/07/a.jpg")) != "AAAA" {
		t.Fatal("content")
	}
	u, err := userdata.Open(f.ctx, filepath.Join(empty.Root(), userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := u.Rating(f.ctx, f.hashes["image/2024/07/a.jpg"]); r != 5 {
		t.Fatal("rating not restored")
	}
	u.Close()

	// Missing mode never overwrites, and skips content present elsewhere.
	write(t, f.lib.Abs("notes.txt"), "local edit")
	os.Remove(f.lib.Abs("image/2023/01/b.jpg"))
	os.MkdirAll(f.lib.Abs("elsewhere"), 0o755)
	os.Rename(f.lib.Abs("video/2022/05/c.mp4"), f.lib.Abs("elsewhere/c.mp4"))
	st, err = Restore(f.ctx, set, f.lib, RestoreOptions{})
	if err != nil || st.Restored != 1 || st.Elsewhere != 1 || st.Present != 3 || st.UserDB != "present" || st.NeedsRebuild {
		t.Fatalf("partial restore %+v %v", st, err)
	}
	if readFile(t, f.lib.Abs("notes.txt")) != "local edit" || readFile(t, f.lib.Abs("image/2023/01/b.jpg")) != "BBBBBB" {
		t.Fatal("overwrote or did not restore")
	}

	// user-data mode refuses an existing user.db without force.
	if _, err := Restore(f.ctx, set, empty, RestoreOptions{Mode: ModeUserData}); !errors.Is(err, ErrUserDBExists) {
		t.Fatalf("user-data without force: %v", err)
	}
	st, err = Restore(f.ctx, set, empty, RestoreOptions{Mode: ModeUserData, Force: true})
	if err != nil || st.UserDB != "replaced" {
		t.Fatalf("user-data force %+v %v", st, err)
	}
	baks, _ := filepath.Glob(filepath.Join(empty.Root(), "user.db.bak-*"))
	if len(baks) == 0 {
		t.Fatal("the replaced user.db was not kept")
	}
	if _, err := Restore(f.ctx, set, empty, RestoreOptions{Mode: "bogus"}); err == nil {
		t.Fatal("bad mode accepted")
	}
}

func TestOpenSetRules(t *testing.T) {
	f := newFx(t)
	if _, err := OpenSet(f.ctx, f.target, "photos", false); !errors.Is(err, ErrNoSet) {
		t.Fatalf("missing set: %v", err)
	}
	for _, bad := range []string{"", ".hidden", "a/b", "..", "CON", "x:y", "trailing."} {
		if _, err := OpenSet(f.ctx, f.target, bad, true); !errors.Is(err, ErrInvalidSetName) {
			t.Errorf("set name %q: %v", bad, err)
		}
	}
	if DefaultSetName("/") != "library" || DefaultSetName("/x/photos") != "photos" {
		t.Error("DefaultSetName")
	}
	for rel, ok := range map[string]bool{"a.jpg": true, "user.db": false, "MEDIA.DB-wal": false, "x/user.db": true,
		".picmover/backup.db": false, ".picmover/trash/a/b.jpg": true, ".picmover/trash": false, "../x": false,
		"a//b": false, "a\\b": false, "c:x": false} {
		if validSetPath(rel) != ok {
			t.Errorf("validSetPath(%q) != %v", rel, ok)
		}
	}
}
