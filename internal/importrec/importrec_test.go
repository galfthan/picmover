package importrec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/userdata"
)

func openUser(t *testing.T) *userdata.DB {
	t.Helper()
	u, err := userdata.Open(context.Background(), filepath.Join(t.TempDir(), userdata.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	return u
}

func TestItemStatusAndOutcome(t *testing.T) {
	for _, c := range []struct {
		res  importer.Result
		want string
	}{
		{importer.Result{Status: importer.StatusImported, Hash: 1}, userdata.ItemImported},
		{importer.Result{Status: importer.StatusImported, Hash: 1, Restored: true}, userdata.ItemRestored},
		{importer.Result{Status: importer.StatusExisting, Hash: 1}, userdata.ItemExisting},
		{importer.Result{Status: importer.StatusDuplicate, Hash: 1}, ""},
		{importer.Result{Status: importer.StatusError}, ""},
		{importer.Result{Status: importer.StatusImported}, ""}, // no hash
	} {
		if got, ok := ItemStatus(c.res); got != c.want || ok != (c.want != "") {
			t.Errorf("ItemStatus(%+v) = %q %v", c.res, got, ok)
		}
	}
	for err, want := range map[error]string{
		nil: userdata.ImportDone,
		&importer.ThumbStageError{Err: errors.New("x")}: userdata.ImportDone,
		fmt.Errorf("run: %w", context.Canceled):         userdata.ImportCancelled,
		errors.New("disk full"):                         userdata.ImportFailed,
	} {
		if got := Outcome(err); got != want {
			t.Errorf("Outcome(%v) = %s, want %s", err, got, want)
		}
	}
}

func TestRecorder(t *testing.T) {
	ctx := context.Background()
	u := openUser(t)
	old := hostName
	hostName = func() string { return "testhost" }
	t.Cleanup(func() { hostName = old })
	r := Begin(ctx, u, nil, "/media/phone/DCIM/", userdata.ImportModeMove, nil)
	if r.ID() == 0 || r.Err() != nil {
		t.Fatalf("begin %d %v", r.ID(), r.Err())
	}
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			r.OnResult(importer.Result{Status: importer.StatusImported, Hash: uint64(i%25 + 1)})
		})
	}
	wg.Wait()
	r.OnResult(importer.Result{Status: importer.StatusImported, Hash: 100, Restored: true})
	r.OnResult(importer.Result{Status: importer.StatusDuplicate, Hash: 200})
	// A cancelled context still records.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Finish(cctx, userdata.ImportCancelled, importer.Stats{Imported: 26, Duplicates: 1, SkippedSmall: 2,
		NonMedia: 3, Errors: 4, BytesCopied: 999}, 7); err != nil {
		t.Fatal(err)
	}
	rec, err := u.GetImport(ctx, r.ID())
	if err != nil || rec.Source != "/media/phone/DCIM" || rec.SourceName != "DCIM" || rec.Mode != "move" ||
		rec.Host != "testhost" || rec.Outcome != "cancelled" || rec.Imported != 26 || rec.Duplicates != 1 ||
		rec.Skipped != 5 || rec.Errors != 4 || rec.Bytes != 999 || rec.CatalogSession != 7 {
		t.Fatalf("record %+v %v", rec, err)
	}
	hs, _ := u.ImportHashes(ctx, r.ID())
	if len(hs) != 26 {
		t.Fatalf("hashes %v", hs)
	}
	if refs, _ := u.ImportsOf(ctx, 200); len(refs) != 0 {
		t.Fatal("duplicate recorded as an item")
	}

	// No user.db: nothing is recorded, the failure is logged and kept.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	r = Begin(ctx, nil, errors.New("schema version 9"), "/x", userdata.ImportModeCopy, log)
	r.OnResult(importer.Result{Status: importer.StatusImported, Hash: 1})
	if err := r.Finish(ctx, userdata.ImportDone, importer.Stats{}, 0); err == nil || !strings.Contains(err.Error(), "schema version 9") {
		t.Fatalf("finish without user.db: %v", err)
	}
	if r.ID() != 0 || !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("id %d log %s", r.ID(), buf.String())
	}

	// A record deleted while the import runs: Finish fails, logged.
	buf.Reset()
	r = Begin(ctx, u, nil, "/y", userdata.ImportModeCopy, log)
	if err := u.DeleteImport(ctx, r.ID()); err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(ctx, userdata.ImportDone, importer.Stats{}, 0); !errors.Is(err, userdata.ErrNotFound) {
		t.Fatalf("finish deleted: %v", err)
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("not logged: %s", buf.String())
	}
}

// Backfill: a catalog with two sessions, one without rows.
func TestBackfill(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "media.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	s1, _ := cat.BeginSession(ctx, "/home/u/Pictures", "copy")
	s2, _ := cat.BeginSession(ctx, "/media/empty", "move")
	if err := cat.FinishSession(ctx, s1, catalog.SessionStats{Imported: 2, Duplicates: 1, NonMedia: 1}); err != nil {
		t.Fatal(err)
	}
	if err := cat.FinishSession(ctx, s2, catalog.SessionStats{Duplicates: 3}); err != nil {
		t.Fatal(err)
	}
	for i, m := range []catalog.Media{
		{Hash: 11, RelPath: "image/a.jpg", ImportSessionID: s1},
		{Hash: 12, RelPath: "image/b.jpg", ImportSessionID: s1, DeletedAt: time.Unix(5, 0)},
	} {
		m.Size, m.FileType, m.Ext = int64(i+1), "image", "jpg"
		if _, err := cat.InsertMedia(ctx, nil, &m); err != nil {
			t.Fatal(err)
		}
	}
	u := openUser(t)
	n, err := Backfill(ctx, cat, u)
	if err != nil || n != 1 {
		t.Fatalf("backfill %d %v", n, err)
	}
	l, _ := u.ListImports(ctx)
	if len(l) != 1 || l[0].CatalogSession != s1 || l[0].Mode != "copy" || l[0].Outcome != "done" ||
		l[0].Imported != 2 || l[0].Duplicates != 1 || l[0].Skipped != 1 || l[0].Items != 2 || l[0].SourceName != "Pictures" {
		t.Fatalf("list %+v", l)
	}
	hs, _ := u.ImportHashes(ctx, l[0].ID)
	if !reflect.DeepEqual(hs, []uint64{11, 12}) {
		t.Fatalf("hashes %v (trashed rows included)", hs)
	}
	// Once per library.
	if n, err := Backfill(ctx, cat, u); err != nil || n != 0 {
		t.Fatalf("second backfill %d %v", n, err)
	}
}
