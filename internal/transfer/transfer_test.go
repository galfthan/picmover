package transfer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/testimg"
	"picmover/internal/thumbs"
	"picmover/internal/userdata"
)

// newSide creates an empty library at <tmp>/<name> with its catalog,
// user.db and a writable store.
func newSide(t *testing.T, name string) Side {
	t.Helper()
	ctx := context.Background()
	lib, err := library.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := userdata.Open(ctx, filepath.Join(lib.Root(), library.UserDBName))
	if err != nil {
		t.Fatal(err)
	}
	store, err := thumbs.OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		user.Close()
		db.Close()
	})
	return Side{Lib: lib, DB: db, User: user, Store: store, Name: name}
}

// photo writes content at rel and records it (with renditions when
// thumbs is set); content must be unique per test.
func photo(t *testing.T, s Side, rel string, content []byte, withThumbs bool) catalog.Media {
	t.Helper()
	ctx := context.Background()
	p := s.Lib.Abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	h, size, err := library.HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m := catalog.Media{
		Hash: h, Size: size, RelPath: rel, FileType: "image", Ext: ".jpg",
		TakenAt: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC), TakenAtSource: "exif",
		Width: 16, Height: 16, CameraMake: "Acme", CameraModel: "One", OriginalPath: "/card/" + filepath.Base(rel),
		ThumbState: catalog.ThumbStatePending,
	}
	if withThumbs {
		jpg := testimg.JPEG(testimg.Options{Width: 16, Height: 16, NoExif: true})
		if err := s.Store.Put(ctx, h, jpg, 16, 16, nil, jpg); err != nil {
			t.Fatal(err)
		}
		m.ThumbState, m.PHash, m.ThumbAt = catalog.ThumbStateOK, 0x1234, time.Now()
	}
	if _, err := s.DB.InsertMedia(ctx, nil, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func content(seed string) []byte { return []byte("picture " + seed + strings.Repeat(".", 100)) }

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func getRow(t *testing.T, db *catalog.DB, h uint64) (catalog.Media, bool) {
	t.Helper()
	m, err := db.GetByHash(context.Background(), h)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return m, true
}

func crossVolume(t *testing.T) {
	t.Helper()
	old := volumeShared
	volumeShared = func(string, string) bool { return false }
	t.Cleanup(func() { volumeShared = old })
}

func doMove(t *testing.T, from, to Side, hashes ...uint64) (Result, []Item) {
	t.Helper()
	var items []Item
	res, err := Run(context.Background(), from, to, hashes, Options{OnItem: func(it Item) { items = append(items, it) }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, items
}

func journalEnded(t *testing.T, s Side, id int64) bool {
	t.Helper()
	p, err := readJournal(filepath.Join(journalDir(s.Lib), strconvI(id)+".journal"))
	if err != nil {
		t.Fatal(err)
	}
	return p.ended
}

func strconvI(i int64) string { return strconv.FormatInt(i, 10) }

func TestMoveSameVolume(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "peter"), newSide(t, "penny")
	m1 := photo(t, a, "image/2024/06/one.jpg", content("1"), true)
	m2 := photo(t, a, "image/2024/06/two.jpg", content("2"), false)
	if _, err := a.User.SetRating(ctx, []uint64{m1.Hash}, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := a.User.TagMedia(ctx, []uint64{m1.Hash, m2.Hash}, nil, nil, []string{"Summer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.User.CreateAlbum(ctx, "Holiday", []uint64{m2.Hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.User.Ignore(ctx, []userdata.Pair{userdata.NewPair(m1.Hash, m2.Hash)}); err != nil {
		t.Fatal(err)
	}

	res, items := doMove(t, a, b, m1.Hash, m2.Hash)
	if res.Moved != 2 || res.Failed != 0 || res.Bytes != 0 || res.RecordID == 0 {
		t.Fatalf("result %+v items %+v", res, items)
	}
	for _, m := range []catalog.Media{m1, m2} {
		if exists(a.Lib.Abs(m.RelPath)) || exists(a.Lib.TrashPath(m.RelPath)) {
			t.Errorf("%s still in the source", m.RelPath)
		}
		if !exists(b.Lib.Abs(m.RelPath)) {
			t.Errorf("%s not in the target", m.RelPath)
		}
		if _, ok := getRow(t, a.DB, m.Hash); ok {
			t.Errorf("%s: source row kept", m.RelPath)
		}
		row, ok := getRow(t, b.DB, m.Hash)
		if !ok || row.RelPath != m.RelPath || row.CameraModel != "One" || row.OriginalPath != m.OriginalPath ||
			!row.TakenAt.Equal(m.TakenAt) || row.ImportSessionID == 0 || !row.DeletedAt.IsZero() {
			t.Errorf("target row %+v", row)
		}
	}
	// Renditions: copied for the photo that had them, gone from the source.
	if th, _, pv, _ := b.Store.Has(ctx, m1.Hash); !th || !pv {
		t.Error("renditions not copied")
	}
	if th, _, _, _ := a.Store.Has(ctx, m1.Hash); th {
		t.Error("source renditions kept")
	}
	if row, _ := getRow(t, b.DB, m1.Hash); row.ThumbState != catalog.ThumbStateOK || row.PHash != 0x1234 {
		t.Errorf("thumb state %q phash %x", row.ThumbState, row.PHash)
	}
	if row, _ := getRow(t, b.DB, m2.Hash); row.ThumbState != catalog.ThumbStatePending {
		t.Errorf("thumb state %q, want pending", row.ThumbState)
	}
	// User data.
	if r, _ := b.User.Rating(ctx, m1.Hash); r != 4 {
		t.Errorf("rating %d", r)
	}
	tags, _ := b.User.TagsOf(ctx, m2.Hash)
	if len(tags) != 1 || tags[0].Name != "Summer" {
		t.Errorf("tags %+v", tags)
	}
	albums, _ := b.User.AlbumsOf(ctx, m2.Hash)
	if len(albums) != 1 || albums[0].Name != "Holiday" {
		t.Errorf("albums %+v", albums)
	}
	if ign, _ := b.User.Ignored(ctx); len(ign) != 1 {
		t.Errorf("ignored pairs %v", ign)
	}
	// Record and journal.
	rec, err := b.User.GetImport(ctx, res.RecordID)
	if err != nil || rec.Mode != userdata.ImportModeTransfer || rec.Outcome != userdata.ImportDone ||
		rec.Imported != 2 || rec.SourceName != "peter" || rec.Items != 2 {
		t.Errorf("record %+v %v", rec, err)
	}
	if !journalEnded(t, b, res.RecordID) {
		t.Error("journal not ended")
	}
}

func TestMoveCrossVolume(t *testing.T) {
	crossVolume(t)
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/x.jpg", content("x"), true)
	res, _ := doMove(t, a, b, m.Hash)
	if res.Moved != 1 || res.Bytes != m.Size {
		t.Fatalf("result %+v", res)
	}
	if !exists(b.Lib.Abs(m.RelPath)) || exists(a.Lib.Abs(m.RelPath)) || !exists(a.Lib.TrashPath(m.RelPath)) {
		t.Fatal("want: in the target, and the original in the source trash")
	}
	got, ok := getRow(t, a.DB, m.Hash)
	if !ok || got.DeletedAt.IsZero() {
		t.Errorf("source row should be trashed: %+v", got)
	}
	if at, _ := a.User.TrashedAt(ctx, []uint64{m.Hash}); len(at) != 1 {
		t.Error("source user.db trash row missing")
	}
	if th, _, _, _ := a.Store.Has(ctx, m.Hash); !th {
		t.Error("source renditions should stay for the trash page")
	}
	if !journalEnded(t, b, res.RecordID) {
		t.Error("journal not ended")
	}
}

func TestExistingAndRestored(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	live := photo(t, a, "image/2024/06/live.jpg", content("live"), false)
	photo(t, b, "image/2023/01/other-name.jpg", content("live"), false)
	gone := photo(t, a, "image/2024/06/gone.jpg", content("gone"), false)
	bg := photo(t, b, "image/2024/06/gone.jpg", content("gone"), false)
	if err := b.Lib.MoveToTrash(bg.RelPath); err != nil {
		t.Fatal(err)
	}
	if err := b.DB.SetDeleted(ctx, bg.Hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.User.SetRating(ctx, []uint64{live.Hash}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.User.SetRating(ctx, []uint64{live.Hash}, 5); err != nil {
		t.Fatal(err)
	}

	res, items := doMove(t, a, b, live.Hash, gone.Hash)
	if res.Existing != 1 || res.Restored != 1 || res.Moved != 0 || res.Failed != 0 {
		t.Fatalf("result %+v items %+v", res, items)
	}
	for _, m := range []catalog.Media{live, gone} {
		if !exists(a.Lib.TrashPath(m.RelPath)) || exists(a.Lib.Abs(m.RelPath)) {
			t.Errorf("%s: source copy should be in the source trash (also on one volume)", m.RelPath)
		}
	}
	if row, _ := getRow(t, b.DB, gone.Hash); !row.DeletedAt.IsZero() || !exists(b.Lib.Abs(row.RelPath)) {
		t.Errorf("target trashed copy not restored: %+v", row)
	}
	if r, _ := b.User.Rating(ctx, live.Hash); r != 2 {
		t.Errorf("rating %d: the target's own rating must win", r)
	}
}

func TestNameTakenInTarget(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/IMG_1.jpg", content("a"), false)
	photo(t, b, "image/2024/06/img_1.jpg", content("b"), false) // case-insensitive clash
	res, items := doMove(t, a, b, m.Hash)
	if res.Moved != 1 || items[0].Dest == m.RelPath || !strings.HasPrefix(items[0].Dest, "image/2024/06/IMG_1") {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if !exists(b.Lib.Abs(items[0].Dest)) {
		t.Fatal("not placed at the new name")
	}
}

func TestSourceChangedIsLeftAlone(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/c.jpg", content("c"), false)
	if err := os.WriteFile(a.Lib.Abs(m.RelPath), content("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cross := range []bool{false, true} {
		if cross {
			crossVolume(t)
		}
		res, items := doMove(t, a, b, m.Hash)
		if res.Failed != 1 || !errors.Is(items[0].Err, ErrSourceMismatch) && !errors.Is(items[0].Err, library.ErrHashMismatch) {
			t.Fatalf("cross=%v: result %+v items %+v", cross, res, items)
		}
		if Message(items[0].Err) != ErrSourceMismatch.Error() {
			t.Errorf("message %q", Message(items[0].Err))
		}
		if !exists(a.Lib.Abs(m.RelPath)) || exists(b.Lib.Abs(m.RelPath)) {
			t.Fatalf("cross=%v: the file must stay in the source only", cross)
		}
		if _, ok := getRow(t, b.DB, m.Hash); ok {
			t.Fatal("target row written")
		}
	}
}

func TestRefusals(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/t.jpg", content("t"), false)
	if err := a.DB.SetDeleted(ctx, m.Hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, items := doMove(t, a, b, m.Hash, 42)
	if res.Failed != 2 || !errors.Is(items[0].Err, ErrTrashedInSource) || !errors.Is(items[1].Err, ErrNotInSource) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if _, err := Run(ctx, a, a, []uint64{m.Hash}, Options{}); !errors.Is(err, ErrSameLibrary) {
		t.Fatalf("same library: %v", err)
	}
}

func TestNoSpace(t *testing.T) {
	crossVolume(t)
	old := freeSpace
	freeSpace = func(string) int64 { return 1000 }
	t.Cleanup(func() { freeSpace = old })
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/s.jpg", content("s"), false)
	p, err := MakePlan(context.Background(), a, b, []uint64{m.Hash})
	if err != nil || !p.NoSpace || p.SameVolume || p.Move != 1 {
		t.Fatalf("plan %+v %v", p, err)
	}
	_, err = Run(context.Background(), a, b, []uint64{m.Hash}, Options{})
	var ns *NoSpaceError
	if !errors.As(err, &ns) || !exists(a.Lib.Abs(m.RelPath)) {
		t.Fatalf("run: %v", err)
	}
}

func TestSourceTrashNameTaken(t *testing.T) {
	crossVolume(t)
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/d.jpg", content("d"), false)
	// The source trash holds another file at the same path.
	tp := a.Lib.TrashPath(m.RelPath)
	if err := os.MkdirAll(filepath.Dir(tp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tp, content("older"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, items := doMove(t, a, b, m.Hash)
	if res.Moved != 1 || items[0].SourceErr != nil {
		t.Fatalf("result %+v items %+v", res, items)
	}
	row, _ := getRow(t, a.DB, m.Hash)
	if row.DeletedAt.IsZero() || row.RelPath == m.RelPath || !exists(a.Lib.TrashPath(row.RelPath)) {
		t.Fatalf("source row %+v", row)
	}
	if got, _ := os.ReadFile(tp); string(got) != string(content("older")) {
		t.Fatal("the older trash file was replaced")
	}
}

func TestCancelled(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m1 := photo(t, a, "image/2024/06/y.jpg", content("y"), false)
	m2 := photo(t, a, "image/2024/06/z.jpg", content("z"), false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := Run(ctx, a, b, []uint64{m1.Hash, m2.Hash}, Options{OnProgress: func(p Progress) {
		if p.Phase == PhaseMove && p.Files == 1 {
			cancel()
		}
	}})
	if err != nil || !res.Cancelled || res.Moved != 1 {
		t.Fatalf("result %+v %v", res, err)
	}
	if !exists(b.Lib.Abs(m1.RelPath)) || !exists(a.Lib.Abs(m2.RelPath)) {
		t.Fatal("the first file should be moved and recorded, the second left")
	}
	if _, ok := getRow(t, b.DB, m1.Hash); !ok {
		t.Fatal("a moved file must be recorded although cancelled")
	}
	rec, _ := b.User.GetImport(context.Background(), res.RecordID)
	if rec.Outcome != userdata.ImportCancelled || rec.Imported != 1 {
		t.Errorf("record %+v", rec)
	}
}

// failInserts makes the target catalog refuse rows whose path contains
// "bad"; the returned func lifts it.
func failInserts(t *testing.T, s Side) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER failbad BEFORE INSERT ON media WHEN NEW.rel_path LIKE '%bad%'
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := s.DB.ExecContext(ctx, "DROP TRIGGER IF EXISTS failbad"); err != nil {
			t.Fatal(err)
		}
	}
}

// A journal write that fails in a flush (the fail line of an undone
// placement) stops the run, but the batch is never flushed again: the
// undone file stays in the source with its row, and every file of the
// batch is reported.
func TestJournalErrorInFlush(t *testing.T) {
	ctx := context.Background()
	for _, cross := range []bool{false, true} {
		if cross {
			crossVolume(t)
		}
		a, b := newSide(t, "a"), newSide(t, "b")
		good := photo(t, a, "image/2024/06/good.jpg", content("g"), false)
		bad := photo(t, a, "image/2024/06/bad.jpg", content("b"), false)
		lift := failInserts(t, b)
		once := false
		journalFail = func(bs []byte) error {
			if !once && strings.HasPrefix(string(bs), `{"op":"fail"`) {
				once = true
				lift() // the catalog error was transient
				return os.ErrPermission
			}
			return nil
		}
		t.Cleanup(func() { journalFail = func([]byte) error { return nil } })
		var items []Item
		res, err := Run(ctx, a, b, []uint64{good.Hash, bad.Hash}, Options{BatchSize: 2,
			OnItem: func(it Item) { items = append(items, it) }})
		journalFail = func([]byte) error { return nil }
		if err == nil || res.Moved != 1 || res.Failed != 1 || len(items) != 2 {
			t.Fatalf("cross=%v: result %+v %v items %+v", cross, res, err, items)
		}
		if _, ok := getRow(t, b.DB, bad.Hash); ok {
			t.Fatalf("cross=%v: the undone file was recorded in the target", cross)
		}
		if row, ok := getRow(t, a.DB, bad.Hash); !ok || !row.DeletedAt.IsZero() || !exists(a.Lib.Abs(bad.RelPath)) {
			t.Fatalf("cross=%v: the undone file must stay live in the source: %+v", cross, row)
		}
		if exists(b.Lib.Abs(bad.RelPath)) {
			t.Fatalf("cross=%v: the undone file is in the target", cross)
		}
		// Its entry stays open (no fail line): recovery finds nothing placed.
		res, err = Run(ctx, a, b, nil, Options{})
		if err != nil || res.Recovered != 0 {
			t.Fatalf("cross=%v: recovering run %+v %v", cross, res, err)
		}
		if list, _ := ListInterrupted(b.Lib, time.Now()); len(list) != 0 {
			t.Fatalf("cross=%v: interrupted %+v", cross, list)
		}
		if row, ok := getRow(t, a.DB, bad.Hash); !ok || !row.DeletedAt.IsZero() || !exists(a.Lib.Abs(bad.RelPath)) {
			t.Fatalf("cross=%v: after recovery the file must stay live in the source: %+v", cross, row)
		}
	}
}

// A row the batch fallback does not insert because the target catalog
// got another row of the content meanwhile is not recorded: the placement
// is undone.
func TestInsertNotDoneIsUndone(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/i.jpg", content("i"), false)
	old := crashAt
	crashAt = func(p string) {
		if p == "before-insert" {
			other := m
			other.ID, other.RelPath = 0, "image/elsewhere/i.jpg"
			if _, err := b.DB.InsertMedia(ctx, nil, &other); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { crashAt = old })
	res, items := doMove(t, a, b, m.Hash)
	if res.Failed != 1 || res.Moved != 0 {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if !exists(a.Lib.Abs(m.RelPath)) || exists(b.Lib.Abs(m.RelPath)) {
		t.Fatal("the placement should be undone")
	}
	if row, ok := getRow(t, a.DB, m.Hash); !ok || !row.DeletedAt.IsZero() {
		t.Fatalf("source row %+v", row)
	}
	if !journalEnded(t, b, res.RecordID) {
		t.Error("journal not ended")
	}
}

// When a row is not inserted and its placement cannot be undone, the
// journal stays open and recovery records the file.
func TestUndoFailedLeavesJournalOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not apply to root")
	}
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/bad.jpg", content("u"), false)
	lift := failInserts(t, b)
	dir := a.Lib.Abs("image/2024/06")
	old := crashAt
	crashAt = func(p string) {
		if p == "before-insert" {
			os.Chmod(dir, 0o555) // the rename back fails
		}
	}
	t.Cleanup(func() { crashAt = old; os.Chmod(dir, 0o755) })
	res, _ := doMove(t, a, b, m.Hash)
	crashAt = old
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lift()
	if res.Failed != 1 || journalEnded(t, b, res.RecordID) {
		t.Fatalf("result %+v: want failed with the journal open", res)
	}
	rec, _ := recoverAll(t, a, b)
	if rec.Recovered != 1 {
		t.Fatalf("recovery %+v", rec)
	}
	if row, ok := getRow(t, b.DB, m.Hash); !ok || !exists(b.Lib.Abs(row.RelPath)) {
		t.Fatalf("target row %+v", row)
	}
}

// A trashed copy in the target that does not hash to the content is not
// restored, and the source is left alone.
func TestRestoreChecksTrashedCopy(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/s.jpg", content("s"), false)
	bm := photo(t, b, "image/2024/06/s.jpg", content("s"), false)
	if err := b.Lib.MoveToTrash(bm.RelPath); err != nil {
		t.Fatal(err)
	}
	if err := b.DB.SetDeleted(ctx, bm.Hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Lib.TrashPath(bm.RelPath), content("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, items := doMove(t, a, b, m.Hash)
	if res.Failed != 1 || res.Restored != 0 || !errors.Is(items[0].Err, ErrTargetTrashMismatch) {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if Message(items[0].Err) != ErrTargetTrashMismatch.Error() {
		t.Errorf("message %q", Message(items[0].Err))
	}
	if !exists(a.Lib.Abs(m.RelPath)) || exists(a.Lib.TrashPath(m.RelPath)) || exists(b.Lib.Abs(bm.RelPath)) {
		t.Fatal("nothing should move")
	}
	if row, _ := getRow(t, b.DB, bm.Hash); row.DeletedAt.IsZero() {
		t.Fatal("target row restored")
	}
	if !journalEnded(t, b, res.RecordID) {
		t.Error("journal not ended")
	}
}

// Content already in the target whose source file is gone: the source
// row is forgotten, nothing stays pending.
func TestExistingSourceMissing(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/gone.jpg", content("gone"), false)
	photo(t, b, "image/2024/06/gone.jpg", content("gone"), false)
	if err := os.Remove(a.Lib.Abs(m.RelPath)); err != nil {
		t.Fatal(err)
	}
	res, items := doMove(t, a, b, m.Hash)
	if res.Existing != 1 || res.SourcePending != 0 || items[0].SourceErr != nil {
		t.Fatalf("result %+v items %+v", res, items)
	}
	if _, ok := getRow(t, a.DB, m.Hash); ok {
		t.Error("source row kept")
	}
	if !journalEnded(t, b, res.RecordID) {
		t.Error("journal not ended")
	}
}

// A plan only reads: an old finished journal is not pruned.
func TestPlanDoesNotPrune(t *testing.T) {
	a, b := newSide(t, "a"), newSide(t, "b")
	m := photo(t, a, "image/2024/06/k.jpg", content("k"), false)
	res, _ := doMove(t, a, b, m.Hash)
	p := JournalPath(b.Lib.Root(), res.RecordID)
	old := time.Now().Add(-2 * keepJournals)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := MakePlan(context.Background(), a, b, []uint64{1}); err != nil {
		t.Fatal(err)
	}
	if !exists(p) {
		t.Fatal("the plan pruned a journal")
	}
	if _, err := ListInterrupted(b.Lib, time.Now()); err != nil || exists(p) {
		t.Fatalf("ListInterrupted should prune it: %v", err)
	}
}

func TestMergeFromAlbumsAndTags(t *testing.T) {
	ctx := context.Background()
	a, b := newSide(t, "a"), newSide(t, "b")
	h1, h2 := uint64(1), uint64(2)
	if _, err := a.User.TagMedia(ctx, []uint64{h1}, nil, nil, []string{"BEACH"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.User.CreateTag(ctx, "beach"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.User.CreateAlbum(ctx, "Trip", []uint64{h1, h2}); err != nil {
		t.Fatal(err)
	}
	first, _ := b.User.CreateAlbum(ctx, "Trip", []uint64{99})
	if _, err := b.User.CreateAlbum(ctx, "Trip", nil); err != nil {
		t.Fatal(err)
	}
	res, err := b.User.MergeFrom(ctx, a.User, []uint64{h1, h2})
	if err != nil {
		t.Fatal(err)
	}
	if res.TagsCreated != 0 || res.Tags != 1 || res.AlbumsCreated != 0 || res.Albums != 2 {
		t.Fatalf("merge %+v", res)
	}
	hs, _ := b.User.AlbumHashes(ctx, first.ID)
	if len(hs) != 3 {
		t.Fatalf("the oldest album of that name should get them: %v", hs)
	}
	// Again: nothing new.
	if res, _ := b.User.MergeFrom(ctx, a.User, []uint64{h1, h2}); res != (userdata.MergeResult{}) {
		t.Fatalf("second merge %+v", res)
	}
}
