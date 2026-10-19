package server

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/backup"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// holdSetRead holds a read transaction on a set's backup.db from another
// connection (another program reading the drive); the returned function
// lets go.
func holdSetRead(t *testing.T, dbPath string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
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
	done := false
	release := func() {
		if done {
			return
		}
		done = true
		conn.ExecContext(ctx, "COMMIT")
		conn.Close()
		db.Close()
	}
	t.Cleanup(release)
	return release
}

// Issue #22: a backup job that cannot write backup.db because another
// program keeps it open fails with a message naming that cause (not "the
// target could not be used"); the outcome is kept in user.db, so the
// Backups page shows it on the target and the Libraries page as the
// library's failure; the next backup clears it.
func TestBackupJobBusyDatabase(t *testing.T) {
	t.Cleanup(backup.SetBusyLimitsForTest(50*time.Millisecond, 300*time.Millisecond))
	f := newFixture(t)
	tg := f.addTarget("drive")
	if j := f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`); j.State != stateDone {
		t.Fatalf("first backup %+v", j)
	}
	writeFile(t, f.lib.Abs("notes-2.txt"), []byte("more"))
	release := holdSetRead(t, filepath.Join(f.base, "drive", "lib", library.MetaDir, backup.DBName))
	j := f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	if j.State != stateFailed || j.ExitCode != 2 || j.Error == nil {
		t.Fatalf("busy backup %+v", j)
	}
	if msg := *j.Error; !strings.Contains(msg, "busy") || strings.Contains(msg, "could not be used") {
		t.Fatalf("job error %q", msg)
	}
	got := f.listing().Targets[0]
	if got.LastAttempt == nil || !strings.Contains(got.LastAttempt.Error, "busy") || got.LastBackupAt == nil {
		t.Fatalf("listing after the failure %+v %+v", got, got.LastAttempt)
	}
	l, _ := libByID(f.libraries(), "lib")
	if l.Backup == nil || l.Backup.Failure == nil || !strings.Contains(l.Backup.Failure.Error, "busy") || l.Backup.Failure.Label != "Red WD" {
		t.Fatalf("libraries listing %+v", l.Backup)
	}
	release()
	if j := f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`); j.State != stateDone {
		t.Fatalf("backup after the reader let go %+v", j)
	}
	if got := f.listing().Targets[0]; got.LastAttempt == nil || got.LastAttempt.Error != "" || got.LastAttempt.SetFiles < 1 {
		t.Fatalf("attempt after a finished backup %+v", got.LastAttempt)
	}
	if l, _ := libByID(f.libraries(), "lib"); l.Backup == nil || l.Backup.Failure != nil {
		t.Fatalf("failure kept after a finished backup %+v", l.Backup)
	}
}

// A target that holds files of a backup that never finished is reported
// as incomplete in the Libraries listing, with the counts.
func TestLibrariesListingIncompleteBackup(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	if err := f.userDB().RecordBackupAttempt(t.Context(), tg.ID, userdata.BackupAttempt{At: time.Now(),
		Error: "the target is full", SetFiles: 3, SetBytes: 30, LibraryFiles: 7}); err != nil {
		t.Fatal(err)
	}
	l, _ := libByID(f.libraries(), "lib")
	if l.Backup == nil || l.Backup.LastBackupAt != nil || l.Backup.Incomplete == nil || l.Backup.Incomplete.Files != 3 ||
		l.Backup.Incomplete.LibraryFiles != 7 || l.Backup.Failure == nil || l.Backup.Failure.Error != "the target is full" {
		t.Fatalf("libraries listing %+v", l.Backup)
	}
	got := f.listing().Targets[0]
	if got.LastAttempt == nil || got.LastAttempt.SetFiles != 3 || got.LastAttempt.Library != 7 || got.Set == nil || got.Set.LibraryFiles < 6 {
		t.Fatalf("target %+v", got)
	}
}

// While a job holds the set the listing reads only its counts and does
// not plan; the plan endpoint is 409.
func TestBackupListingDoesNotPlanWhileHeld(t *testing.T) {
	f := newFixture(t)
	tg := f.addTarget("drive")
	f.startBackupJob(`{"kind":"backup","target":"` + tg.ID + `"}`)
	f.get("/api/l/lib/backups") // fills the plan cache
	writeFile(t, f.lib.Abs("notes-2.txt"), []byte("more"))
	unlock, err := library.LockFile(filepath.Join(f.base, "drive", "lib", library.MetaDir, library.LockName), "backup")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	w := f.get("/api/l/lib/backups?plan=1")
	wantStatus(t, w, http.StatusOK)
	got := decode[backupsJSON](t, w).Targets[0]
	if got.Busy == "" || got.Set == nil || got.Set.Files == 0 {
		t.Fatalf("while held %+v %+v", got, got.Set)
	}
	// The cached plan is served (made before the new file): not planned again.
	if got.Set.Behind == nil || got.Set.Behind.New != 0 {
		t.Fatalf("behind while held %+v", got.Set.Behind)
	}
	unlock()
	got = decode[backupsJSON](t, f.get("/api/l/lib/backups?plan=1")).Targets[0]
	if got.Busy != "" || got.Set.Behind == nil || got.Set.Behind.New != 1 {
		t.Fatalf("after the lock %+v %+v", got, got.Set.Behind)
	}
}
