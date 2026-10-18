package cmd

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// writeV3Catalog leaves a catalog of schema version 3 (enough of it for
// the version check) in a new library folder.
func writeV3Catalog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, library.DBName))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{
		`CREATE TABLE media (id INTEGER PRIMARY KEY, hash INTEGER NOT NULL UNIQUE, rel_path TEXT NOT NULL,
			taken_at INTEGER, taken_at_offset INTEGER, taken_at_source TEXT NOT NULL)`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Commands that only read never upgrade a catalog of the previous format
// (they do not hold the library lock): they say which command does.
func TestReadOnlyCommandsDoNotUpgrade(t *testing.T) {
	dir := writeV3Catalog(t)
	p := filepath.Join(dir, library.DBName)
	before, _ := os.ReadFile(p)
	for _, args := range [][]string{{"db", dir}, {"home-zone", dir}, {"update-metadata", "--dry-run", dir}} {
		_, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "previous format") || !strings.Contains(err.Error(), "picmover thumbs") {
			t.Errorf("%v: %v", args, err)
		}
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Error("catalog changed")
	}
	if baks, _ := filepath.Glob(p + ".bak-*"); len(baks) != 0 {
		t.Errorf("backups %v", baks)
	}
}

func TestFormatCaptureRow(t *testing.T) {
	paris := time.FixedZone("", 2*3600)
	m := catalog.Media{TakenAt: time.Date(2024, 7, 14, 14, 30, 0, 0, paris), OffsetKnown: true,
		TakenAtHome: time.Date(2024, 7, 14, 15, 30, 0, 0, time.UTC)}
	if got := formatCaptureRow(m); got != "2024-07-14 15:30:00 (local 2024-07-14 14:30:00 +02:00)" {
		t.Errorf("trip row %q", got)
	}
	m.TakenAtHome = time.Date(2024, 7, 14, 14, 30, 0, 0, time.UTC)
	if got := formatCaptureRow(m); got != "2024-07-14 14:30:00 +02:00" {
		t.Errorf("home row %q", got)
	}
	if got := formatCaptureRow(catalog.Media{}); got != "-" {
		t.Errorf("undated %q", got)
	}
}
