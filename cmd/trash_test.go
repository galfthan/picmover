package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// rebuild records the trash as trashed rows, with the user.db trash time
// when there is one, and leaves user.db's ratings alone.
func TestRebuildRecordsTrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lib")
	lib, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, lib.Abs("image/2021/03/live.jpg"), 1)
	writeJPEG(t, lib.TrashPath("image/2021/03/a.jpg"), 2)
	writeJPEG(t, lib.TrashPath("image/2021/03/b.jpg"), 3)
	mt := time.Date(2022, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(lib.TrashPath("image/2021/03/b.jpg"), mt, mt); err != nil {
		t.Fatal(err)
	}
	hashA, _, err := library.HashFile(lib.TrashPath("image/2021/03/a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, err := userdata.Open(ctx, filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0)
	u.Trash(ctx, []uint64{hashA}, at)
	u.SetRating(ctx, []uint64{hashA}, 4)
	u.Close()

	if out, err := run(t, "rebuild", "--quiet", "--no-thumbs", dir); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	db, err := catalog.OpenExisting(ctx, lib.DBPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.List(ctx, catalog.ListOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	for _, m := range rows {
		got[m.RelPath] = m.DeletedAt
	}
	if len(got) != 3 || !got["image/2021/03/live.jpg"].IsZero() || !got["image/2021/03/a.jpg"].Equal(at) ||
		!got["image/2021/03/b.jpg"].Equal(mt) {
		t.Fatalf("rows %v", got)
	}
	for _, rel := range []string{"image/2021/03/a.jpg", "image/2021/03/b.jpg"} {
		if _, err := os.Stat(lib.TrashPath(rel)); err != nil {
			t.Fatalf("trash file moved: %v", err)
		}
	}
	u, err = userdata.Open(ctx, filepath.Join(dir, userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if r, _ := u.Rating(ctx, hashA); r != 4 {
		t.Fatalf("rating lost by rebuild: %d", r)
	}

	// verify checks trashed rows at their trash path.
	if out, err := run(t, "verify", dir); err != nil || !strings.Contains(out, "Missing files: 0") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	if err := os.Remove(lib.TrashPath("image/2021/03/b.jpg")); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, lib.Abs("image/2021/03/b.jpg"), 3) // the file is back in the library, not in the trash
	out, err := run(t, "verify", dir)
	if err == nil || !strings.Contains(out, "Missing files: 1\n  image/2021/03/b.jpg") ||
		!strings.Contains(out, "Orphan files (not in catalog): 1\n  image/2021/03/b.jpg") {
		t.Fatalf("verify after removing a trash file: %v\n%s", err, out)
	}
}
