package userdata

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	if err := CheckFile(ctx, p); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("CheckFile created the file")
	}
	d, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := CheckFile(ctx, p); err != nil {
		t.Fatalf("current version: %v", err)
	}
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 5"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	err = CheckFile(ctx, p)
	var ve *VersionError
	if !errors.Is(err, ErrUnsupportedVersion) || !errors.As(err, &ve) || ve.Version != 5 {
		t.Fatalf("version 4: %v", err)
	}
	if got, want := ve.Error(), "user.db has schema version 5; this picmover supports 4: upgrade picmover"; got != want {
		t.Errorf("message %q, want %q", got, want)
	}
	if _, err := Open(ctx, p); !errors.As(err, &ve) {
		t.Fatalf("Open: %v", err)
	}
}
