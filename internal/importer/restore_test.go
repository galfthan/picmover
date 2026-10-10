package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/library"
)

// trashOne imports data from the source as name and trashes its row like
// the server does; it returns the row's hash and catalog path.
func trashOne(t *testing.T, e *env, name string, data []byte) (uint64, string) {
	t.Helper()
	src := writeFile(t, filepath.Join(e.src, name), data)
	st, _ := e.mustRun(e.opts())
	if st.Imported != 1 {
		t.Fatalf("first import: %+v", st)
	}
	hash, _, err := library.HashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.db.GetByHash(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.lib.MoveToTrash(m.RelPath); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetDeleted(context.Background(), hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	return hash, m.RelPath
}

// Importing content that is in the trash restores the trashed row instead
// of reporting a duplicate that "Empty trash" would then delete; a move
// deletes the source only after the restored copy is verified.
func TestImportRestoresTrashed(t *testing.T) {
	e := newEnv(t)
	data := jpegAt(date(2020, 3, 5), 1)
	hash, rel := trashOne(t, e, "a.jpg", data)
	src := filepath.Join(e.src, "a.jpg")

	var untrashed []uint64
	o := e.opts()
	o.Move = true
	o.NoThumbs = true
	o.OnRestored = func(_ context.Context, h uint64) error { untrashed = append(untrashed, h); return nil }
	st, c := e.mustRun(o)
	if st.Imported != 1 || st.Duplicates != 0 {
		t.Fatalf("stats %+v", st)
	}
	res := c.byStatus(StatusImported)
	if len(res) != 1 || res[0].Dest != rel || !strings.Contains(res[0].Message, "restored from trash") ||
		!strings.Contains(res[0].Message, "source removed") {
		t.Fatalf("results %+v", res)
	}
	if exists(src) {
		t.Error("move source kept")
	}
	if b, err := os.ReadFile(e.lib.Abs(rel)); err != nil || string(b) != string(data) {
		t.Fatalf("restored file: %v", err)
	}
	if exists(e.lib.TrashPath(rel)) {
		t.Error("trash file still there")
	}
	m, err := e.db.GetByHash(context.Background(), hash)
	if err != nil || !m.DeletedAt.IsZero() || m.RelPath != rel {
		t.Fatalf("row %+v %v", m, err)
	}
	if len(untrashed) != 1 || untrashed[0] != hash {
		t.Errorf("OnRestored %v", untrashed)
	}
}

// The restore picks a free name when the row's path is taken, and a
// copy import keeps the source.
func TestImportRestoresTrashedIntoTakenName(t *testing.T) {
	e := newEnv(t)
	data := jpegAt(date(2020, 3, 5), 2)
	_, rel := trashOne(t, e, "b.jpg", data)
	writeFile(t, e.lib.Abs(rel), []byte("someone else"))
	o := e.opts()
	o.NoThumbs = true
	st, c := e.mustRun(o)
	res := c.byStatus(StatusImported)
	if st.Imported != 1 || len(res) != 1 || res[0].Dest == rel || !strings.HasSuffix(res[0].Dest, "b_1.jpg") {
		t.Fatalf("stats %+v results %+v", st, res)
	}
	if !exists(filepath.Join(e.src, "b.jpg")) {
		t.Error("copy import removed its source")
	}
	if b, _ := os.ReadFile(e.lib.Abs(rel)); string(b) != "someone else" {
		t.Error("taken file replaced")
	}
}

// A trashed copy that no longer matches its row is not restored, and the
// move source is kept.
func TestImportTrashedCopyMismatchKeepsSource(t *testing.T) {
	e := newEnv(t)
	hash, rel := trashOne(t, e, "c.jpg", jpegAt(date(2020, 3, 5), 3))
	if err := os.WriteFile(e.lib.TrashPath(rel), []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := e.opts()
	o.Move = true
	o.NoThumbs = true
	st, c, err := e.run(o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Errors != 1 || st.Imported != 0 || len(c.byStatus(StatusError)) != 1 {
		t.Fatalf("stats %+v", st)
	}
	if !exists(filepath.Join(e.src, "c.jpg")) {
		t.Fatal("source deleted although nothing was restored")
	}
	if m, _ := e.db.GetByHash(context.Background(), hash); m.DeletedAt.IsZero() {
		t.Error("row made live")
	}
}

// A source that contains another library never imports its catalog, its
// user.db or anything under its .picmover directory (previews are JPEGs).
func TestImportSkipsOtherLibrariesFiles(t *testing.T) {
	e := newEnv(t)
	other := filepath.Join(e.src, "other")
	writeFile(t, filepath.Join(other, "media.db"), []byte("x"))
	writeFile(t, filepath.Join(other, "USER.DB-wal"), []byte("x"))
	writeFile(t, filepath.Join(other, ".picmover", "previews", "5d", "5d00.jpg"), jpegAt(date(2021, 1, 1), 7))
	writeFile(t, filepath.Join(other, ".PicMover", "x.jpg"), jpegAt(date(2021, 1, 2), 8))
	writeFile(t, filepath.Join(other, "image", "2021", "01", "p.jpg"), jpegAt(date(2021, 1, 3), 9))
	o := e.opts()
	o.Move = true
	o.NoThumbs = true
	st, _ := e.mustRun(o)
	if st.Imported != 1 || st.Scanned != 1 || st.NonMedia != 0 {
		t.Fatalf("stats %+v", st)
	}
	for _, p := range []string{"media.db", "USER.DB-wal", ".picmover/previews/5d/5d00.jpg", ".PicMover/x.jpg"} {
		if !exists(filepath.Join(other, filepath.FromSlash(p))) {
			t.Errorf("%s moved", p)
		}
	}
}
