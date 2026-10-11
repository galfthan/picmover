package importer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"picmover/internal/catalog"
	"picmover/internal/imaging"
	"picmover/internal/testimg"
)

// Rows whose renditions predate the large size (thumbnail and preview,
// no large) get it from the stored preview: without reading the original
// (deleted here after import), without touching the catalog row, counted
// in LargeDone; the next pass finds nothing to do. A row without a
// preview is skipped. ThumbLimit bounds the fills too.
func TestRunThumbsFillsMissingLarge(t *testing.T) {
	e := newEnv(t)
	for i := range 4 {
		writeFile(t, filepath.Join(e.src, "f"+string(rune('a'+i))+".jpg"),
			testimg.JPEG(testimg.Options{Width: 1200, Height: 900, DateTime: date(2020, 3, 5), Seed: byte(i)}))
	}
	st, _ := e.mustRun(e.opts())
	if st.Imported != 4 || st.ThumbsDone != 4 || st.LargeDone != 0 {
		t.Fatalf("import = %+v", st)
	}
	store := openStore(t, e.lib)
	rows := e.rows()
	var noPreview catalog.Media
	i := 0
	for _, m := range rows {
		// Fresh renditions include the large thumbnail (WebP, 640 px).
		b, err := os.ReadFile(store.LargePath(m.Hash))
		if err != nil {
			t.Fatalf("%s: large: %v", m.RelPath, err)
		}
		img, f, err := imaging.Decode(context.Background(), bytes.NewReader(b), int64(len(b)), imaging.Options{})
		if err != nil || f != imaging.FormatWebP || img.Bounds().Dx() != 640 {
			t.Fatalf("%s: large %q %v %v", m.RelPath, f, img.Bounds(), err)
		}
		// Simulate renditions made before the large size: drop the large
		// thumbnail; delete the original so any read of it would fail.
		os.Remove(store.LargePath(m.Hash))
		if i == 0 {
			// This one has no preview either: skipped.
			os.Remove(store.PreviewPath(m.Hash))
			noPreview = m
		} else if err := os.Remove(e.lib.Abs(m.RelPath)); err != nil {
			t.Fatal(err)
		}
		i++
	}

	// ThumbLimit bounds the fills.
	st, err := New(e.lib, e.db, Options{ThumbLimit: 2}).RunThumbs(context.Background(), 0, false)
	if err != nil || st.LargeDone != 2 || st.ThumbsDone+st.ThumbsFailed+st.ThumbsUnsupported != 0 {
		t.Fatalf("limited RunThumbs = %+v, %v", st, err)
	}
	st, err = New(e.lib, e.db, Options{}).RunThumbs(context.Background(), 0, false)
	if err != nil || st.LargeDone != 1 || st.ThumbsDone+st.ThumbsFailed+st.ThumbsUnsupported != 0 {
		t.Fatalf("RunThumbs = %+v, %v", st, err)
	}
	for rel, m := range e.rows() {
		before := rows[rel]
		if m.ThumbState != catalog.ThumbStateOK || !m.ThumbAt.Equal(before.ThumbAt) || m.PHash != before.PHash {
			t.Errorf("%s: catalog row changed: %+v -> %+v", rel, before, m)
		}
		_, large, _, err := store.Has(context.Background(), m.Hash)
		if err != nil {
			t.Fatal(err)
		}
		if want := m.Hash != noPreview.Hash; large != want {
			t.Errorf("%s: large = %v, want %v", rel, large, want)
		}
	}
	// Nothing left to do.
	st, err = New(e.lib, e.db, Options{}).RunThumbs(context.Background(), 0, false)
	if err != nil || st.LargeDone != 0 {
		t.Errorf("third RunThumbs = %+v, %v", st, err)
	}
	if ents, _ := os.ReadDir(e.lib.TempDir()); len(ents) != 0 {
		t.Errorf("temp files left: %v", ents)
	}
	// The originals were never recreated or touched.
	for rel, m := range e.rows() {
		if m.Hash == noPreview.Hash {
			continue
		}
		if _, err := os.Stat(e.lib.Abs(rel)); !os.IsNotExist(err) {
			t.Errorf("%s: original exists again (%v)", rel, err)
		}
	}
}
