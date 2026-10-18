package importrec

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/transfer"
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
		&importer.ThumbStageError{Err: errors.New("x")}:       userdata.ImportDone,
		fmt.Errorf("run: %w", context.Canceled):               userdata.ImportCancelled,
		errors.New("disk full"):                               userdata.ImportFailed,
		fmt.Errorf("run: %w", importer.ErrSourceDisconnected): userdata.ImportDisconnected,
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
		NonMedia: 3, Errors: 4, BytesCopied: 999}, 7, nil); err != nil {
		t.Fatal(err)
	}
	rec, err := u.GetImport(ctx, r.ID())
	if err != nil || rec.Source != "/media/phone/DCIM" || rec.SourceName != "DCIM" || rec.Mode != "move" ||
		rec.Host != "testhost" || rec.Outcome != "cancelled" || rec.Imported != 26 || rec.Duplicates != 1 ||
		rec.Skipped != 5 || rec.Errors != 4 || rec.Bytes != 999 || rec.CatalogSession != 7 {
		t.Fatalf("record %+v %v", rec, err)
	}
	if k := rec.Kinds; k == nil || *k != (userdata.ImportKinds{}) {
		t.Fatalf("kinds %+v", k)
	}
	// The imported split by kind is recorded, and the disconnect outcome.
	r2 := Begin(ctx, u, nil, "/media/phone", userdata.ImportModeCopy, nil)
	if err := r2.Finish(ctx, Outcome(importer.ErrSourceDisconnected), importer.Stats{Imported: 6, ImportedPhotos: 3,
		ImportedRaw: 2, ImportedVideos: 1}, 0, nil); err != nil {
		t.Fatal(err)
	}
	if rec, err := u.GetImport(ctx, r2.ID()); err != nil || rec.Outcome != userdata.ImportDisconnected || rec.Imported != 6 ||
		rec.Kinds == nil || *rec.Kinds != (userdata.ImportKinds{Photos: 3, Raw: 2, Videos: 1}) {
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
	if err := r.Finish(ctx, userdata.ImportDone, importer.Stats{}, 0, nil); err == nil || !strings.Contains(err.Error(), "schema version 9") {
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
	if err := r.Finish(ctx, userdata.ImportDone, importer.Stats{}, 0, nil); !errors.Is(err, userdata.ErrNotFound) {
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

// fakeNewest answers NewestOf with a fixed hash.
type fakeNewest uint64

func (f fakeNewest) NewestOf(context.Context, []uint64) (uint64, bool, error) {
	return uint64(f), f != 0, nil
}

func TestRecorderCoverSessionAndRebuild(t *testing.T) {
	ctx := context.Background()
	u := openUser(t)
	r := Begin(ctx, u, nil, "/a", userdata.ImportModeCopy, nil)
	r.SetSession(ctx, 0) // ignored
	r.SetSession(ctx, 41)
	r.SetSession(ctx, 42) // written once
	if rec, _ := u.GetImport(ctx, r.ID()); rec.CatalogSession != 41 || rec.Outcome != userdata.ImportRunning {
		t.Fatalf("session while running %+v", rec)
	}
	r.OnResult(importer.Result{Status: importer.StatusImported, Hash: 1})
	r.OnResult(importer.Result{Status: importer.StatusImported, Hash: 2})
	if err := r.Finish(ctx, userdata.ImportDone, importer.Stats{Imported: 2}, 41, fakeNewest(1)); err != nil {
		t.Fatal(err)
	}
	if rec, _ := u.GetImport(ctx, r.ID()); rec.Items != 2 || rec.Cover != 1 || rec.CatalogSession != 41 {
		t.Fatalf("finished %+v", rec)
	}

	// A rebuild keeps its counts and no items.
	r = Begin(ctx, u, nil, "/lib", userdata.ImportModeRebuild, nil)
	r.OnResult(importer.Result{Status: importer.StatusExisting, Hash: 1})
	if err := r.Finish(ctx, userdata.ImportDone, importer.Stats{Existing: 1}, 0, fakeNewest(1)); err != nil {
		t.Fatal(err)
	}
	rec, _ := u.GetImport(ctx, r.ID())
	hs, _ := u.ImportHashes(ctx, r.ID())
	if rec.Existing != 1 || rec.KeepsItems() || rec.Cover != 0 || len(hs) != 0 {
		t.Fatalf("rebuild record %+v %v", rec, hs)
	}
}

func TestCloseCrashed(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Open(ctx, filepath.Join(t.TempDir(), "media.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	sid, _ := cat.BeginSession(ctx, "/media/phone", "copy")
	for i, h := range []uint64{21, 22} {
		m := catalog.Media{Hash: h, RelPath: fmt.Sprintf("image/%d.jpg", h), ImportSessionID: sid, Size: int64(i + 1),
			FileType: "image", Ext: "jpg", TakenAt: time.Unix(int64(100-i), 0)}
		if _, err := cat.InsertMedia(ctx, nil, &m); err != nil {
			t.Fatal(err)
		}
	}
	u := openUser(t)
	crashed := Begin(ctx, u, nil, "/media/phone", userdata.ImportModeCopy, nil)
	crashed.SetSession(ctx, sid)
	unknown := Begin(ctx, u, nil, "/media/other", userdata.ImportModeCopy, nil) // died before its session
	live := Begin(ctx, u, nil, "/media/live", userdata.ImportModeCopy, nil)     // a job still running
	n, err := CloseCrashed(ctx, u, cat.SessionHashes, func(id int64) bool { return id == live.ID() }, nil)
	if err != nil || n != 2 {
		t.Fatalf("closed %d %v", n, err)
	}
	rec, _ := u.GetImport(ctx, crashed.ID())
	hs, _ := u.ImportHashes(ctx, crashed.ID())
	if rec.Outcome != userdata.ImportFailed || rec.FinishedAt.IsZero() || rec.Items != 2 || rec.Cover != 21 ||
		rec.Imported != 2 || !reflect.DeepEqual(hs, []uint64{21, 22}) {
		t.Fatalf("crashed record %+v %v", rec, hs)
	}
	if rec, _ := u.GetImport(ctx, unknown.ID()); rec.Outcome != userdata.ImportFailed || rec.Items != 0 {
		t.Fatalf("record without session %+v", rec)
	}
	if rec, _ := u.GetImport(ctx, live.ID()); rec.Outcome != userdata.ImportRunning {
		t.Fatalf("owned record closed %+v", rec)
	}
	// Nothing is left to close but the owned one.
	if n, err := CloseCrashed(ctx, u, cat.SessionHashes, func(id int64) bool { return id == live.ID() }, nil); err != nil || n != 0 {
		t.Fatalf("again %d %v", n, err)
	}
}

// A running transfer record is left to the move's recovery while its
// journal exists, and closed when it has none (the process died before
// creating it).
func TestCloseCrashedTransfer(t *testing.T) {
	ctx := context.Background()
	u := openUser(t)
	root := filepath.Dir(u.Path())
	withJournal := Begin(ctx, u, nil, "/libs/a", userdata.ImportModeTransfer, nil)
	without := Begin(ctx, u, nil, "/libs/a", userdata.ImportModeTransfer, nil)
	p := transfer.JournalPath(root, withJournal.ID())
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := CloseCrashed(ctx, u, nil, nil, nil)
	if err != nil || n != 1 {
		t.Fatalf("closed %d %v", n, err)
	}
	if rec, _ := u.GetImport(ctx, without.ID()); rec.Outcome != userdata.ImportFailed {
		t.Fatalf("record without a journal %+v", rec)
	}
	if rec, _ := u.GetImport(ctx, withJournal.ID()); rec.Outcome != userdata.ImportRunning {
		t.Fatalf("record with a journal closed %+v", rec)
	}
}

// BackfillFile reads an older (schema version 2) catalog, and a record
// written before it (a rebuild's own) does not prevent it.
func TestBackfillFileV2(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "media.db")
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
CREATE TABLE media (id INTEGER PRIMARY KEY, hash INTEGER NOT NULL UNIQUE, size INTEGER, rel_path TEXT NOT NULL,
    file_type TEXT NOT NULL, ext TEXT NOT NULL, taken_at INTEGER, imported_at INTEGER NOT NULL,
    import_session_id INTEGER, rating INTEGER NOT NULL DEFAULT 0, deleted_at INTEGER);
CREATE TABLE import_sessions (id INTEGER PRIMARY KEY, started_at INTEGER NOT NULL, finished_at INTEGER,
    source TEXT NOT NULL, mode TEXT NOT NULL, imported INTEGER NOT NULL DEFAULT 0, existing INTEGER NOT NULL DEFAULT 0,
    duplicates INTEGER NOT NULL DEFAULT 0, skipped_small INTEGER NOT NULL DEFAULT 0, non_media INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0);
INSERT INTO import_sessions (id, started_at, finished_at, source, mode, imported) VALUES (1, 1000, 1100, '/old/DCIM', 'copy', 2);
INSERT INTO media (hash, rel_path, file_type, ext, taken_at, imported_at, import_session_id) VALUES
    (11, 'a.jpg', 'image', 'jpg', 50, 1, 1), (12, 'b.jpg', 'image', 'jpg', 60, 1, 1);
PRAGMA user_version = 2;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	u := openUser(t)
	rb := Begin(ctx, u, nil, "/lib", userdata.ImportModeRebuild, nil)
	rb.Finish(ctx, userdata.ImportDone, importer.Stats{}, 0, nil)
	n, err := BackfillFile(ctx, p, u)
	if err != nil || n != 1 {
		t.Fatalf("backfill from a v2 catalog %d %v", n, err)
	}
	l, _ := u.ListImports(ctx)
	if len(l) != 2 || l[1].SourceName != "DCIM" || l[1].Items != 2 || l[1].Cover != 12 {
		t.Fatalf("list %+v", l)
	}
	// A failure leaves the marker unset, so a later backfill tries again.
	u2 := openUser(t)
	if _, err := BackfillFile(ctx, filepath.Join(t.TempDir(), "missing.db"), u2); err == nil {
		t.Fatal("backfill from a missing catalog succeeded")
	}
	if ok, _ := u2.ImportsBackfilled(ctx); ok {
		t.Fatal("marked after a failed backfill")
	}
	if n, err := BackfillFile(ctx, p, u2); err != nil || n != 1 {
		t.Fatalf("retry %d %v", n, err)
	}
}
