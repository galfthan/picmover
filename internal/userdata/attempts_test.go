package userdata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestBackupAttempts: an attempt is kept per target, makes a target
// without a finished backup incomplete and a stopped run a failure in
// the summary (also through the read-only peek), is left out of the
// content hash, and goes with Forget.
func TestBackupAttempts(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), FileName)
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d.Close() }()
	for _, id := range []string{"0123456789abcdef", "fedcba9876543210"} {
		if _, _, err := d.AddBackupTarget(ctx, BackupTarget{ID: id, Label: "T" + id[:2], LastPath: "/x/" + id, LastHost: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.RecordBackupAttempt(ctx, "0000000000000000", BackupAttempt{At: time.Now()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
	h0, err := d.ContentHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0)
	if err := d.RecordBackupAttempt(ctx, "0123456789abcdef", BackupAttempt{At: at, Error: "the backup database on the target stayed busy",
		SetFiles: 98543, SetBytes: 1 << 40, LibraryFiles: 193158}); err != nil {
		t.Fatal(err)
	}
	if h1, _ := d.ContentHash(ctx); h1 != h0 {
		t.Error("the attempt changed the content hash")
	}
	check := func(s BackupSummary) {
		t.Helper()
		if s.Incomplete == nil || s.Incomplete.Files != 98543 || s.Incomplete.LibraryFiles != 193158 || s.Incomplete.Label != "T01" {
			t.Fatalf("incomplete %+v", s.Incomplete)
		}
		if s.Failure == nil || s.Failure.Error == "" || !s.Failure.At.Equal(at) {
			t.Fatalf("failure %+v", s.Failure)
		}
	}
	s, err := d.BackupSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(s)
	d.Close() // checkpoints the WAL: the peek reads the file
	s, ok, err := PeekBackupSummary(ctx, p)
	if err != nil || !ok {
		t.Fatalf("peek %v %v", ok, err)
	}
	check(s)
	if d, err = Open(ctx, p); err != nil {
		t.Fatal(err)
	}

	// A finished backup: complete, no failure.
	if err := d.MarkBackup(ctx, "0123456789abcdef", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordBackupAttempt(ctx, "0123456789abcdef", BackupAttempt{At: at.Add(time.Hour), SetFiles: 193158, LibraryFiles: 193158}); err != nil {
		t.Fatal(err)
	}
	if s, _ := d.BackupSummary(ctx); s.Incomplete != nil || s.Failure != nil {
		t.Fatalf("after a finished backup: %+v %+v", s.Incomplete, s.Failure)
	}
	all, err := d.BackupAttempts(ctx)
	if err != nil || len(all) != 1 || all["0123456789abcdef"].SetFiles != 193158 {
		t.Fatalf("attempts %+v %v", all, err)
	}
	if err := d.ForgetBackupTarget(ctx, "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if all, _ := d.BackupAttempts(ctx); len(all) != 0 {
		t.Fatalf("forget kept the attempt: %+v", all)
	}
}
