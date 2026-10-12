package transfer

import (
	"context"
	"errors"
	"os"
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
