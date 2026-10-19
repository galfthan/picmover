package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/library"
)

// shortBusy makes backup.db give up on a lock quickly, so a reader that
// holds its read lock for a second is "too long" (as a plan over a slow
// USB drive is with the real 10 s busy_timeout).
func shortBusy(t *testing.T, budget time.Duration) {
	t.Helper()
	t.Cleanup(SetBusyLimitsForTest(50*time.Millisecond, budget))
}

// reader holds a read transaction on the set's backup.db (SQLite's
// SHARED lock) from another connection, as the Backups page or another
// program does, until release is called.
type reader struct {
	db   *sql.DB
	conn *sql.Conn
	once sync.Once
}

func holdRead(t *testing.T, s *Set) *reader {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(s.MetaDir(), DBName))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM files").Scan(&n); err != nil {
		t.Fatal(err)
	}
	r := &reader{db: db, conn: conn}
	t.Cleanup(r.release)
	return r
}

func (r *reader) release() {
	r.once.Do(func() {
		r.conn.ExecContext(context.Background(), "COMMIT")
		r.conn.Close()
		r.db.Close()
	})
}

// syncBuffer is a log buffer the run's goroutines write to.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestBackupWaitsForReader: a reader holds backup.db's read lock while
// a backup runs, much longer than busy_timeout. The backup waits for it
// (warning in the log) instead of failing, and records every row and
// the run once the reader lets go (issue #22).
func TestBackupWaitsForReader(t *testing.T) {
	shortBusy(t, time.Minute)
	f := newFx(t)
	f.run(Options{})
	f.media("image/2024/08/d.jpg", "DDDD", 2024)
	f.media("image/2024/08/e.jpg", "EEEEE", 2024)
	set := f.set(false)
	r := holdRead(t, set)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		r.release()
	}()
	var logBuf syncBuffer
	st, err := Run(f.ctx, f.src(), set, Options{Owner: f.id, Logger: slog.New(slog.NewTextHandler(&logBuf, nil))})
	if err != nil {
		t.Fatalf("backup failed while a reader held backup.db: %v\n%s", err, logBuf.String())
	}
	if st.Copied != 2 || st.Error != "" {
		t.Fatalf("stats %+v", st)
	}
	if !strings.Contains(logBuf.String(), "backup.db on the target is busy") {
		t.Errorf("no busy warning in the log:\n%s", logBuf.String())
	}
	rows := rowsOf(t, set)
	for _, rel := range []string{"image/2024/08/d.jpg", "image/2024/08/e.jpg", "image/2024/07/a.jpg"} {
		if rows[rel].Hash != f.hashes[rel] {
			t.Errorf("row %s = %+v", rel, rows[rel])
		}
	}
	runs, err := set.Runs(f.ctx, 10)
	if err != nil || len(runs) != 2 || runs[0].FinishedAt.IsZero() {
		t.Fatalf("runs %+v %v", runs, err)
	}
}

// TestBackupBusyGivesUpSafely: a reader that never lets go within the
// budget stops the run with an error naming the busy database. The
// copies whose rows could not be written stay on the target, and the
// next run adopts them by hash (nothing copied twice, nothing removed,
// nothing recorded as verified that was not).
func TestBackupBusyGivesUpSafely(t *testing.T) {
	shortBusy(t, 400*time.Millisecond)
	f := newFx(t)
	f.run(Options{})
	f.media("image/2024/08/d.jpg", "DDDD", 2024)
	set := f.set(false)
	var r *reader
	crashAfter = func(done int) bool {
		if done == 1 {
			r = holdRead(t, set) // after the run began: its rows cannot be written
		}
		return false
	}
	defer func() { crashAfter = nil }()
	st, err := Run(f.ctx, f.src(), set, Options{Owner: f.id})
	crashAfter = nil
	if err == nil || !IsBusy(err) {
		t.Fatalf("want a busy error, got %v", err)
	}
	if msg := RunError(err); !strings.Contains(msg, "busy") || strings.Contains(msg, "could not be used") {
		t.Fatalf("RunError = %q", msg)
	}
	if st.Error == "" || st.Copied != 1 {
		t.Fatalf("stats %+v", st)
	}
	if _, ok := rowsOf(t, set)["image/2024/08/d.jpg"]; ok {
		t.Fatal("the row was written although the commit failed?")
	}
	if got := readFile(t, set.Abs("image/2024/08/d.jpg")); got != "DDDD" {
		t.Fatalf("copy on the target: %q", got)
	}
	r.release()
	st = f.run(Options{})
	if st.Adopted != 1 || st.Copied != 0 {
		t.Fatalf("next run: %+v", st)
	}
	row := rowsOf(t, set)["image/2024/08/d.jpg"]
	if row.Hash != f.hashes["image/2024/08/d.jpg"] || row.VerifiedAt != 0 {
		t.Fatalf("adopted row %+v", row)
	}
}

// TestFilesChunks: Files reads every row across chunk boundaries, in
// path order, once.
func TestFilesChunks(t *testing.T) {
	old := filesChunk
	filesChunk = 2
	defer func() { filesChunk = old }()
	f := newFx(t)
	for i := range 7 {
		f.media(fmt.Sprintf("image/2020/01/F%02d.jpg", i), strings.Repeat("x", i+1), 2020)
	}
	f.run(Options{})
	rows, err := f.set(false).Files(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.RelPath] {
			t.Fatalf("%s twice", r.RelPath)
		}
		seen[r.RelPath] = true
	}
	if len(rows) != 5+7 {
		t.Fatalf("%d rows: %v", len(rows), seen)
	}
}

// TestRunErrorNames: the client text names the cause.
func TestRunErrorNames(t *testing.T) {
	for err, want := range map[error]string{
		nil:                                "",
		context.Canceled:                   "",
		errors.New("x"):                    "unexpected",
		fmt.Errorf("w: %w", errTargetGone): "unplugged",
		errors.New("backup: write rows: database is locked (5) (SQLITE_BUSY)"): "busy",
	} {
		got := RunError(err)
		if (want == "" && got != "") || !strings.Contains(got, want) {
			t.Errorf("RunError(%v) = %q, want %q", err, got, want)
		}
	}
}

// TestSetHeld: a run's lock is seen without taking it; a released one
// is not.
func TestSetHeld(t *testing.T) {
	f := newFx(t)
	f.run(Options{})
	set := f.set(false)
	if _, held := SetHeld(f.target, "photos"); held {
		t.Fatal("held before a run")
	}
	unlock, ok, err := set.Lock("backup")
	if err != nil || !ok {
		t.Skipf("no locking here: %v", err)
	}
	h, held := SetHeld(f.target, "photos")
	if !held || h.Cmd != "backup" {
		t.Fatalf("held %v %+v", held, h)
	}
	var le *library.ErrLocked
	if !errors.As(error(h), &le) {
		t.Fatal("not an ErrLocked")
	}
	unlock()
	if _, held := SetHeld(f.target, "photos"); held {
		t.Fatal("held after unlock")
	}
}
