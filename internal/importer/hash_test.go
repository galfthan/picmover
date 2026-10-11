package importer

import (
	"context"
	"path/filepath"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// Result.Hash is set for imported, existing and duplicate results (the
// content hash, the one the catalog row has) and 0 for the others.
func TestResultHash(t *testing.T) {
	e := newEnv(t)
	a := jpegAt(date(2020, 3, 5), 1)
	b := jpegAt(date(2020, 3, 6), 2)
	srcA := writeFile(t, filepath.Join(e.src, "a.jpg"), a)
	writeFile(t, filepath.Join(e.src, "copy-of-a.jpg"), a)
	srcB := writeFile(t, filepath.Join(e.src, "b.jpg"), b)
	writeFile(t, e.lib.Abs("image/2020/03/b.jpg"), b) // identical at the destination: existing
	writeFile(t, filepath.Join(e.src, "notes.txt"), []byte("text"))
	hashA, _, _ := library.HashFile(srcA)
	hashB, _, _ := library.HashFile(srcB)

	o := e.opts()
	o.NoThumbs = true
	st, c := e.mustRun(o)
	if st.Imported != 1 || st.Existing != 1 || st.Duplicates != 1 || st.NonMedia != 1 {
		t.Fatalf("stats %+v", st)
	}
	rows := e.rows()
	for _, r := range c.results {
		switch r.Status {
		case StatusImported:
			if r.Hash != hashA || rows[r.Dest].Hash != hashA || r.Restored {
				t.Errorf("imported %+v", r)
			}
		case StatusExisting:
			if r.Hash != hashB || rows[r.Dest].Hash != hashB {
				t.Errorf("existing %+v", r)
			}
		case StatusDuplicate:
			if r.Hash != hashA {
				t.Errorf("duplicate %+v", r)
			}
		default:
			if r.Hash != 0 {
				t.Errorf("%s with a hash: %+v", r.Status, r)
			}
		}
	}

	// A second run: every media file is a duplicate already in the catalog.
	_, c = e.mustRun(o)
	for _, r := range c.byStatus(StatusDuplicate) {
		if r.Hash != hashA && r.Hash != hashB {
			t.Errorf("rerun duplicate %+v", r)
		}
	}
	if n := len(c.byStatus(StatusDuplicate)); n != 3 {
		t.Errorf("%d duplicates on rerun", n)
	}
}

// A restored result carries the hash and Restored.
func TestResultHashRestored(t *testing.T) {
	e := newEnv(t)
	hash, _ := trashOne(t, e, "a.jpg", jpegAt(date(2020, 3, 5), 1))
	o := e.opts()
	o.NoThumbs = true
	_, c := e.mustRun(o)
	res := c.byStatus(StatusImported)
	if len(res) != 1 || res[0].Hash != hash || !res[0].Restored {
		t.Fatalf("restored results %+v", res)
	}
}

// A row whose insert lost the hash race is reported as a duplicate with
// the hash; a failed batch reports errors without one (nothing was
// recorded).
func TestResultHashCommit(t *testing.T) {
	e := newEnv(t)
	p := pendingCopy(t, e, "image/2020/03/a_1.jpg", jpegAt(date(2020, 3, 5), 1))
	if _, err := e.db.InsertMedia(context.Background(), nil, &catalog.Media{Hash: p.hash, Size: p.media.Size,
		RelPath: "image/2020/03/a.jpg", FileType: "image", Ext: "jpg"}); err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	if err := New(e.lib, e.db, Options{OnResult: c.add}).commit([]*pending{p}); err != nil {
		t.Fatal(err)
	}
	if d := c.byStatus(StatusDuplicate); len(d) != 1 || d[0].Hash != p.hash {
		t.Fatalf("lost race %+v", c.results)
	}

	p = pendingCopy(t, e, "image/2020/03/b.jpg", jpegAt(date(2020, 3, 6), 2))
	e.db.Close()
	c = &collector{}
	if err := New(e.lib, e.db, Options{OnResult: c.add}).commit([]*pending{p}); err == nil {
		t.Fatal("commit on a closed catalog succeeded")
	}
	if errs := c.byStatus(StatusError); len(errs) != 1 || errs[0].Hash != 0 {
		t.Fatalf("failed batch %+v", c.results)
	}
}
