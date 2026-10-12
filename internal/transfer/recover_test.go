package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// crashed is a move from a to b that died after the steps a test
// performs by hand: the target's running record and its journal.
type crashed struct {
	a, b Side
	id   int64
	j    *journal
}

func startCrashed(t *testing.T, a, b Side) *crashed {
	t.Helper()
	ctx := context.Background()
	id, err := b.User.BeginImport(ctx, userdata.ImportRecord{Source: a.Lib.Root(), SourceName: a.Name,
		Mode: userdata.ImportModeTransfer})
	if err != nil {
		t.Fatal(err)
	}
	sa, _ := ref(ctx, a)
	sb, _ := ref(ctx, b)
	j, err := createJournal(b.Lib, Header{ID: id, Source: sa, Target: sb, StartedAt: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.close() })
	return &crashed{a: a, b: b, id: id, j: j}
}

func (c *crashed) begin(t *testing.T, m catalog.Media, kind, to, mode string) {
	t.Helper()
	if err := c.j.begin(m.Hash, kind, m.RelPath, to, mode); err != nil {
		t.Fatal(err)
	}
}

func (c *crashed) recover(t *testing.T) (Result, []Item) {
	t.Helper()
	c.j.close()
	list, err := ListInterrupted(c.b.Lib, time.Now())
	if err != nil || len(list) != 1 || list[0].ID != c.id {
		t.Fatalf("interrupted %+v %v", list, err)
	}
	var items []Item
	res, err := Recover(context.Background(), c.a, c.b, list[0].Path, Options{OnItem: func(it Item) { items = append(items, it) }})
	if err != nil {
		t.Fatal(err)
	}
	return res, items
}

func (c *crashed) record(t *testing.T) userdata.ImportRecord {
	t.Helper()
	rec, err := c.b.User.GetImport(context.Background(), c.id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// Crash after a same-volume rename, before the target row: forward.
func TestRecoverRenamedNotRecorded(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/r.jpg", content("r"), true)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeRename)
	if err := a.Lib.MoveFileTo(m.RelPath, b.Lib, m.RelPath); err != nil {
		t.Fatal(err)
	}
	res, items := c.recover(t)
	if res.Recovered != 1 || res.Moved != 1 || items[0].SourceErr != nil {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if _, ok := getRow(t, a.DB, m.Hash); ok {
		t.Error("source row should be forgotten")
	}
	if row, ok := getRow(t, b.DB, m.Hash); !ok || row.RelPath != m.RelPath {
		t.Errorf("target row %+v", row)
	}
	if rec := c.record(t); rec.Outcome != userdata.ImportFailed || rec.Imported != 1 {
		t.Errorf("record %+v", rec)
	}
	if !journalEnded(t, b, c.id) {
		t.Error("journal not ended")
	}
	if list, _ := ListInterrupted(b.Lib, time.Now()); len(list) != 0 {
		t.Errorf("still interrupted: %+v", list)
	}
}

// Crash after a verified copy, before the target row: forward, and the
// original to the source trash.
func TestRecoverCopiedNotRecorded(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/c.jpg", content("c"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeCopy)
	if _, err := library.CopyReadBack(context.Background(), a.Lib.Abs(m.RelPath), b.Lib.Abs(m.RelPath), b.Lib.TempDir(), &m.Hash); err != nil {
		t.Fatal(err)
	}
	res, _ := c.recover(t)
	if res.Recovered != 1 {
		t.Fatalf("result %+v", res)
	}
	if !exists(a.Lib.TrashPath(m.RelPath)) || exists(a.Lib.Abs(m.RelPath)) {
		t.Error("original should be in the source trash")
	}
	if row, ok := getRow(t, a.DB, m.Hash); !ok || row.DeletedAt.IsZero() {
		t.Errorf("source row %+v", row)
	}
	if _, ok := getRow(t, b.DB, m.Hash); !ok {
		t.Error("target row missing")
	}
}

// Crash after the target row committed, before the source step: the
// source step is done.
func TestRecoverRecordedSourceLeft(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/s.jpg", content("s"), false)
	if _, err := a.User.SetRating(ctx, []uint64{m.Hash}, 3); err != nil {
		t.Fatal(err)
	}
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeCopy)
	if _, err := library.CopyReadBack(ctx, a.Lib.Abs(m.RelPath), b.Lib.Abs(m.RelPath), b.Lib.TempDir(), &m.Hash); err != nil {
		t.Fatal(err)
	}
	nr := m
	nr.ID = 0
	if _, err := b.DB.InsertMedia(ctx, nil, &nr); err != nil {
		t.Fatal(err)
	}
	res, _ := c.recover(t)
	if res.Recovered != 1 || !exists(a.Lib.TrashPath(m.RelPath)) {
		t.Fatalf("result %+v", res)
	}
	if r, _ := b.User.Rating(ctx, m.Hash); r != 3 {
		t.Errorf("user data not merged: rating %d", r)
	}
}

// Crash after the journal line, before anything was placed: back.
func TestRecoverNothingPlaced(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/n.jpg", content("n"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeCopy)
	res, items := c.recover(t)
	if res.Recovered != 0 || res.Failed != 1 || !errors.Is(items[0].Err, ErrInterrupted) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if !exists(a.Lib.Abs(m.RelPath)) || exists(b.Lib.Abs(m.RelPath)) {
		t.Fatal("the file must stay in the source")
	}
	if row, ok := getRow(t, a.DB, m.Hash); !ok || !row.DeletedAt.IsZero() {
		t.Fatal("source row changed")
	}
}

// Another file at the target path (not the one moved) is left alone.
func TestRecoverWrongFileAtTarget(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/w.jpg", content("w"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeRename)
	p := b.Lib.Abs(m.RelPath)
	if err := os.MkdirAll(b.Lib.Abs("image/2024/06"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content("someone else"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, items := c.recover(t)
	if res.Failed != 1 || !errors.Is(items[0].Err, ErrInterrupted) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if got, _ := os.ReadFile(p); string(got) != string(content("someone else")) {
		t.Fatal("the other file was touched")
	}
}

// The case the protocol's order excludes: the file is nowhere.
func TestRecoverLostTrack(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/l.jpg", content("l"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeRename)
	if err := os.Remove(a.Lib.Abs(m.RelPath)); err != nil {
		t.Fatal(err)
	}
	res, items := c.recover(t)
	if res.Failed != 1 || !errors.Is(items[0].Err, ErrLostTrack) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if _, ok := getRow(t, a.DB, m.Hash); !ok {
		t.Fatal("source row must be kept")
	}
}

// A torn last line is ignored; done entries count in the record.
func TestRecoverTornLineAndDone(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m1 := photo(t, a, "image/2024/06/d1.jpg", content("d1"), false)
	m2 := photo(t, a, "image/2024/06/d2.jpg", content("d2"), false)
	c := startCrashed(t, a, b)
	// m1 finished before the crash.
	c.begin(t, m1, kindMove, m1.RelPath, modeRename)
	if err := a.Lib.MoveFileTo(m1.RelPath, b.Lib, m1.RelPath); err != nil {
		t.Fatal(err)
	}
	r1 := m1
	r1.ID = 0
	if _, err := b.DB.InsertMedia(ctx, nil, &r1); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Remove(ctx, m1.Hash); err != nil {
		t.Fatal(err)
	}
	if err := c.j.done(m1.Hash); err != nil {
		t.Fatal(err)
	}
	c.begin(t, m2, kindMove, m2.RelPath, modeRename)
	if err := c.j.raw([]byte(`{"op":"done","ha`)); err != nil {
		t.Fatal(err)
	}
	res, _ := c.recover(t)
	if res.Recovered != 0 || res.Failed != 1 {
		t.Fatalf("result %+v", res)
	}
	rec := c.record(t)
	if rec.Imported != 1 || rec.Items != 1 || rec.Errors != 1 {
		t.Errorf("record %+v", rec)
	}
}

// Run finishes an interrupted move between the same libraries first.
func TestRunRecoversFirst(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m1 := photo(t, a, "image/2024/06/p.jpg", content("p"), false)
	m2 := photo(t, a, "image/2024/06/q.jpg", content("q"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m1, kindMove, m1.RelPath, modeRename)
	if err := a.Lib.MoveFileTo(m1.RelPath, b.Lib, m1.RelPath); err != nil {
		t.Fatal(err)
	}
	c.j.close()
	res, _ := doMove(t, a, b, m2.Hash)
	if res.Recovered != 1 || res.Moved != 1 {
		t.Fatalf("result %+v", res)
	}
	if _, ok := getRow(t, b.DB, m1.Hash); !ok {
		t.Fatal("interrupted file not recorded")
	}
	// A move from a third library is not touched.
	if list, _ := ListInterrupted(b.Lib, time.Now()); len(list) != 0 {
		t.Fatalf("interrupted %+v", list)
	}
}

func TestIsSource(t *testing.T) {
	in := Interrupted{ID: 1, Source: LibraryRef{ID: "abc", Root: t.TempDir()}}
	if !IsSource(in, "abc", in.Source.Root) || IsSource(in, "xyz", in.Source.Root) {
		t.Fatal("id match")
	}
	// Same id, another root that still holds a library: a copy.
	if err := os.WriteFile(in.Source.Root+"/media.db", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSource(in, "abc", t.TempDir()) {
		t.Fatal("a copy of the library must not match while the original exists")
	}
	in.Source.Root = t.TempDir() + "/moved-away"
	if !IsSource(in, "abc", "/new/place") {
		t.Fatal("a moved library should match by id")
	}
}

type crashSig struct{ at string }

// crashOn makes the protocol panic at the crashAt point named.
func crashOn(t *testing.T, point string) {
	t.Helper()
	old := crashAt
	crashAt = func(p string) {
		if p == point {
			panic(crashSig{p})
		}
	}
	t.Cleanup(func() { crashAt = old })
}

// runCrash runs a move expected to "crash" at the point set by crashOn;
// the crash hook is cleared afterwards.
func runCrash(t *testing.T, from, to Side, hashes ...uint64) {
	t.Helper()
	defer func() {
		crashAt = func(string) {}
		r := recover()
		if _, ok := r.(crashSig); !ok {
			if r != nil {
				panic(r)
			}
			t.Fatal("the run did not crash")
		}
	}()
	Run(context.Background(), from, to, hashes, Options{})
}

// recoverAll recovers every interrupted move into b and sums the results.
func recoverAll(t *testing.T, a, b Side) (Result, []Item) {
	t.Helper()
	list, err := ListInterrupted(b.Lib, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var tot Result
	var items []Item
	for _, in := range list {
		res, err := Recover(context.Background(), a, b, in.Path, Options{OnItem: func(it Item) { items = append(items, it) }})
		if err != nil {
			t.Fatal(err)
		}
		tot.Recovered += res.Recovered
		tot.Moved += res.Moved
		tot.Existing += res.Existing
		tot.Restored += res.Restored
		tot.Failed += res.Failed
		tot.SourcePending += res.SourcePending
	}
	return tot, items
}

func noInterrupted(t *testing.T, s Side) {
	t.Helper()
	if list, err := ListInterrupted(s.Lib, time.Now()); err != nil || len(list) != 0 {
		t.Fatalf("interrupted %+v %v", list, err)
	}
}

// A crash after the batch's rows and source steps, before the user data
// merge: the entries are still open (done follows the merge), so
// recovery merges.
func TestCrashBeforeMerge(t *testing.T) {
	ctx := context.Background()
	for _, cross := range []bool{false, true} {
		a, b := newSide(t, "a"), newSide(t, "b")
		if cross {
			crossVolume(t)
		}
		m := photo(t, a, "image/2024/06/u.jpg", content("u"), true)
		if _, err := a.User.SetRating(ctx, []uint64{m.Hash}, 5); err != nil {
			t.Fatal(err)
		}
		if _, err := a.User.TagMedia(ctx, []uint64{m.Hash}, nil, nil, []string{"Keep"}); err != nil {
			t.Fatal(err)
		}
		crashOn(t, "before-merge")
		runCrash(t, a, b, m.Hash)
		if _, ok := getRow(t, b.DB, m.Hash); !ok {
			t.Fatal("the crash point should be after the target row")
		}
		res, _ := recoverAll(t, a, b)
		if res.Recovered != 1 || res.Failed != 0 {
			t.Fatalf("cross=%v: result %+v", cross, res)
		}
		if r, _ := b.User.Rating(ctx, m.Hash); r != 5 {
			t.Errorf("cross=%v: rating %d after recovery, want 5", cross, r)
		}
		if tags, _ := b.User.TagsOf(ctx, m.Hash); len(tags) != 1 {
			t.Errorf("cross=%v: tags %+v", cross, tags)
		}
		if cross {
			// The source's user.db trash row too.
			if at, _ := a.User.TrashedAt(ctx, []uint64{m.Hash}); len(at) != 1 {
				t.Error("source user.db trash row missing after recovery")
			}
		}
		noInterrupted(t, b)
	}
}

// A crash right after a file is reported (its done line follows the
// merge): recovery merges its user data.
func TestCrashAfterReportBeforeMerge(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/x.jpg", content("x"), true)
	if _, err := a.User.SetRating(ctx, []uint64{m.Hash}, 4); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { recover() }()
		Run(ctx, a, b, []uint64{m.Hash}, Options{OnItem: func(it Item) {
			if it.Status == StatusMoved {
				panic("crash")
			}
		}})
	}()
	list, _ := ListInterrupted(b.Lib, time.Now())
	if len(list) != 1 || list[0].Open != 1 {
		t.Fatalf("the entry should still be open: %+v", list)
	}
	recoverAll(t, a, b)
	if r, _ := b.User.Rating(ctx, m.Hash); r != 4 {
		t.Errorf("rating %d, want 4", r)
	}
}

// A crash between the source copy's rename to the source trash and its
// catalog update: recovery finds it in the trash and marks the row
// trashed, for a copy, a duplicate, and a taken trash name.
func TestCrashAfterMoveToTrash(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"copy", "existing", "taken"} {
		a, b := newSide(t, "a"), newSide(t, "b")
		m := photo(t, a, "image/2024/06/t.jpg", content("t"+kind), true)
		switch kind {
		case "copy":
			crossVolume(t)
		case "existing":
			photo(t, b, "image/2024/06/t.jpg", content("t"+kind), true)
		case "taken":
			crossVolume(t)
			tp := a.Lib.TrashPath(m.RelPath)
			if err := os.MkdirAll(filepath.Dir(tp), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tp, content("older"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		crashOn(t, "after-movetotrash")
		runCrash(t, a, b, m.Hash)
		if exists(a.Lib.Abs(m.RelPath)) {
			t.Fatalf("%s: the crash point should be after the rename", kind)
		}
		res, items := recoverAll(t, a, b)
		if res.Recovered != 1 || res.SourcePending != 0 || res.Failed != 0 {
			t.Fatalf("%s: result %+v items %+v", kind, res, items)
		}
		row, ok := getRow(t, a.DB, m.Hash)
		if !ok || row.DeletedAt.IsZero() || !exists(a.Lib.TrashPath(row.RelPath)) {
			t.Fatalf("%s: source row %+v (ok %v): want trashed, with its file in the trash", kind, row, ok)
		}
		if got, _, _ := library.HashFile(a.Lib.TrashPath(row.RelPath)); got != m.Hash {
			t.Errorf("%s: the source row names another trash file", kind)
		}
		if th, _, _, _ := a.Store.Has(ctx, m.Hash); !th {
			t.Errorf("%s: source renditions deleted", kind)
		}
		if at, _ := a.User.TrashedAt(ctx, []uint64{m.Hash}); len(at) != 1 {
			t.Errorf("%s: source user.db trash row missing", kind)
		}
		noInterrupted(t, b)
	}
}

// The same crash for a duplicate whose copy in the target is gone by
// recovery time: the source file comes back from the trash to its place.
func TestCrashAfterMoveToTrashTargetGone(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/e.jpg", content("e"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindExisting, m.RelPath, "")
	if err := a.Lib.MoveToTrash(m.RelPath); err != nil {
		t.Fatal(err)
	}
	res, items := c.recover(t)
	if res.Failed != 1 || !errors.Is(items[0].Err, ErrInterrupted) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	row, ok := getRow(t, a.DB, m.Hash)
	if !ok || !row.DeletedAt.IsZero() || !exists(a.Lib.Abs(m.RelPath)) || exists(a.Lib.TrashPath(m.RelPath)) {
		t.Fatalf("source row %+v: want live with its file back in place", row)
	}
	noInterrupted(t, b)
}

// An entry whose step fails in recovery stays open: the journal is not
// ended, the record stays running, and a later Recover finishes it.
func TestRecoverLeavesFailedStepOpen(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/r.jpg", content("r"), true)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeRename)
	if err := a.Lib.MoveFileTo(m.RelPath, b.Lib, m.RelPath); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DB.ExecContext(ctx, `CREATE TRIGGER failall BEFORE INSERT ON media
		BEGIN SELECT RAISE(ABORT, 'transient'); END`); err != nil {
		t.Fatal(err)
	}
	res, _ := c.recover(t)
	if res.Failed != 1 || res.Recovered != 0 {
		t.Fatalf("result %+v", res)
	}
	if journalEnded(t, b, c.id) {
		t.Fatal("journal ended with an open entry")
	}
	if rec := c.record(t); rec.Outcome != userdata.ImportRunning {
		t.Fatalf("record closed with an open entry: %+v", rec)
	}
	if _, err := b.DB.ExecContext(ctx, "DROP TRIGGER failall"); err != nil {
		t.Fatal(err)
	}
	res, _ = recoverAll(t, a, b)
	if res.Recovered != 1 {
		t.Fatalf("second recovery %+v", res)
	}
	if _, ok := getRow(t, b.DB, m.Hash); !ok {
		t.Fatal("target row missing")
	}
	if rec := c.record(t); rec.Outcome != userdata.ImportFailed || rec.Imported != 1 {
		t.Errorf("record %+v", rec)
	}
	noInterrupted(t, b)
}

// A placed file that cannot be read is not the lost-track case: the
// entry stays open and is finished once it can be read.
func TestRecoverPlacedFileUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not apply to root")
	}
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/p.jpg", content("p"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeRename)
	if err := a.Lib.MoveFileTo(m.RelPath, b.Lib, m.RelPath); err != nil {
		t.Fatal(err)
	}
	p := b.Lib.Abs(m.RelPath)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o644) })
	res, items := c.recover(t)
	if res.Failed != 1 || errors.Is(items[0].Err, ErrLostTrack) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if journalEnded(t, b, c.id) {
		t.Fatal("journal ended")
	}
	if _, ok := getRow(t, a.DB, m.Hash); !ok {
		t.Fatal("source row changed")
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if res, _ := recoverAll(t, a, b); res.Recovered != 1 {
		t.Fatalf("second recovery %+v", res)
	}
	noInterrupted(t, b)
}

// Cancelling a recovery stops between entries; the rest stay open.
func TestRecoverCancelled(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m1 := photo(t, a, "image/2024/06/c1.jpg", content("c1"), false)
	m2 := photo(t, a, "image/2024/06/c2.jpg", content("c2"), false)
	c := startCrashed(t, a, b)
	for _, m := range []catalog.Media{m1, m2} {
		c.begin(t, m, kindMove, m.RelPath, modeRename)
		if err := a.Lib.MoveFileTo(m.RelPath, b.Lib, m.RelPath); err != nil {
			t.Fatal(err)
		}
	}
	c.j.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	list, _ := ListInterrupted(b.Lib, time.Now())
	res, err := Recover(ctx, a, b, list[0].Path, Options{OnProgress: func(Progress) { cancel() }})
	if err != nil || !res.Cancelled || res.Recovered != 1 {
		t.Fatalf("result %+v %v", res, err)
	}
	if _, ok := getRow(t, b.DB, m1.Hash); !ok {
		t.Fatal("the first entry should be finished")
	}
	if _, ok := getRow(t, b.DB, m2.Hash); ok {
		t.Fatal("the second entry should not be started")
	}
	if journalEnded(t, b, c.id) || c.record(t).Outcome != userdata.ImportRunning {
		t.Fatal("journal ended or record closed while an entry is open")
	}
	if res, _ := recoverAll(t, a, b); res.Recovered != 1 {
		t.Fatalf("resume %+v", res)
	}
	if rec := c.record(t); rec.Imported != 2 {
		t.Errorf("record %+v", rec)
	}
	noInterrupted(t, b)
}

// A journal that came along with a copy of the target's folder is not
// listed there and not recovered into another library.
func TestJournalOfAnotherTarget(t *testing.T) {
	a, b, other := newSide(t, "a"), newSide(t, "b"), newSide(t, "other")
	m := photo(t, a, "image/2024/06/o.jpg", content("o"), false)
	c := startCrashed(t, a, b)
	c.begin(t, m, kindMove, m.RelPath, modeCopy)
	c.j.close()
	dst := JournalPath(other.Lib.Root(), c.id)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.j.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if list, _ := ListInterrupted(other.Lib, time.Now()); len(list) != 0 {
		t.Fatalf("listed in the copy: %+v", list)
	}
	if list, _ := ListInterruptedAt(other.Lib.Root()); len(list) != 0 {
		t.Fatalf("listed in the copy (read only): %+v", list)
	}
	if _, err := Recover(context.Background(), a, other, dst, Options{}); !errors.Is(err, ErrOtherTarget) {
		t.Fatalf("Recover into the copy: %v", err)
	}
	// A target library folder that moved keeps its journals.
	if !addressedTo(LibraryRef{Root: filepath.Join(t.TempDir(), "moved-away")}, other.Lib.Root()) {
		t.Fatal("a moved target should match")
	}
	if list, _ := ListInterrupted(b.Lib, time.Now()); len(list) != 1 {
		t.Fatalf("the original's journal: %+v", list)
	}
}

// A crash after the target's restore committed, before its user.db trash
// row went: recovery removes it.
func TestCrashAfterRestoreBeforeUntrash(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/g.jpg", content("g"), false)
	bm := photo(t, b, "image/2024/06/g.jpg", content("g"), false)
	if err := b.Lib.MoveToTrash(bm.RelPath); err != nil {
		t.Fatal(err)
	}
	if err := b.DB.SetDeleted(ctx, bm.Hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.User.Trash(ctx, []uint64{bm.Hash}, time.Now()); err != nil {
		t.Fatal(err)
	}
	crashOn(t, "before-merge")
	runCrash(t, a, b, m.Hash)
	res, _ := recoverAll(t, a, b)
	if res.Restored != 1 || res.Recovered != 1 {
		t.Fatalf("result %+v", res)
	}
	if at, _ := b.User.TrashedAt(ctx, []uint64{m.Hash}); len(at) != 0 {
		t.Error("target user.db trash row kept")
	}
	if row, _ := getRow(t, a.DB, m.Hash); row.DeletedAt.IsZero() {
		t.Error("source not trashed")
	}
	noInterrupted(t, b)
}

// A crash inside the target's restore, after its rename out of the trash
// and before the catalog update (at the trashed path or at a variant):
// recovery finishes the restore.
func TestCrashInsideRestore(t *testing.T) {
	ctx := context.Background()
	for _, variant := range []bool{false, true} {
		a, b := newSide(t, "a"), newSide(t, "b")
		m := photo(t, a, "image/2024/06/h.jpg", content("h"), false)
		bm := photo(t, b, "image/2024/06/h.jpg", content("h"), false)
		if err := b.Lib.MoveToTrash(bm.RelPath); err != nil {
			t.Fatal(err)
		}
		if err := b.DB.SetDeleted(ctx, bm.Hash, time.Now()); err != nil {
			t.Fatal(err)
		}
		c := startCrashed(t, a, b)
		c.begin(t, m, kindRestore, bm.RelPath, "")
		want := bm.RelPath
		if variant {
			// Another file took the path: the restore picked a variant.
			if err := os.WriteFile(b.Lib.Abs(bm.RelPath), content("in the way"), 0o644); err != nil {
				t.Fatal(err)
			}
			want = "image/2024/06/h_1.jpg"
		}
		if err := b.Lib.RestoreTo(bm.RelPath, want); err != nil {
			t.Fatal(err)
		}
		res, items := c.recover(t)
		if res.Restored != 1 || res.Recovered != 1 {
			t.Fatalf("variant=%v: result %+v items %+v", variant, res, items)
		}
		if row, _ := getRow(t, b.DB, m.Hash); !row.DeletedAt.IsZero() || row.RelPath != want {
			t.Errorf("variant=%v: target row %+v", variant, row)
		}
		if row, _ := getRow(t, a.DB, m.Hash); row.DeletedAt.IsZero() || !exists(a.Lib.TrashPath(m.RelPath)) {
			t.Errorf("variant=%v: source not trashed", variant)
		}
	}
}

// The folders a recovery relies on are synced up to the library root
// and no further.
func TestSyncParents(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "image", "2024", "06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	syncParents(root, dir, seen)
	for _, d := range []string{dir, filepath.Dir(dir), filepath.Join(root, "image"), root} {
		if !seen[d] {
			t.Errorf("%s not synced", d)
		}
	}
	if len(seen) != 4 {
		t.Errorf("synced %v", seen)
	}
}
