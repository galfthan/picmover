package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// CatalogFileSuffixes are the files that make up a catalog: the database,
// its WAL and shared memory, and a rollback journal. They are renamed
// together so a catalog is never separated from them.
var CatalogFileSuffixes = []string{"", "-wal", "-shm", "-journal"}

// BackupCatalog renames dbPath and its -wal/-shm/-journal files to
// <dbPath>.bak-<YYYYMMDD-HHMMSS> (plus the same suffixes), so SQLite still
// finds the WAL or journal of the backup. It never overwrites: an existing
// backup name is an error. If a rename fails, the renames already done are
// undone, so a catalog is never separated from its WAL. It returns the
// backup path of the main file.
func BackupCatalog(dbPath string, now time.Time) (string, error) {
	backup := dbPath + ".bak-" + now.Format(BackupStampLayout)
	suffixes := CatalogFileSuffixes
	for _, s := range suffixes {
		if _, err := os.Lstat(backup + s); err == nil {
			return "", fmt.Errorf("backup %s already exists; try again in a second", backup+s)
		}
	}
	var done []string
	for _, s := range suffixes {
		err := os.Rename(dbPath+s, backup+s)
		if err == nil {
			done = append(done, s)
			continue
		}
		if s != "" && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		for i := len(done) - 1; i >= 0; i-- {
			if uerr := os.Rename(backup+done[i], dbPath+done[i]); uerr != nil {
				return "", fmt.Errorf("back up catalog: %w; undo failed, move %s back to %s by hand: %v",
					err, backup+done[i], dbPath+done[i], uerr)
			}
		}
		return "", fmt.Errorf("back up catalog: %w", err)
	}
	return backup, nil
}

// RestoreCatalogBackup undoes BackupCatalog after a rebuild that failed or
// was cancelled before its new catalog was complete: it removes the new
// catalog at dbPath with its -wal/-shm/-journal files and renames the
// backup's files back (never replacing anything). With backup "" (the
// folder had no catalog) it only removes the new catalog. The new catalog
// must be closed first.
func RestoreCatalogBackup(dbPath, backup string) error {
	for _, s := range CatalogFileSuffixes {
		if err := os.Remove(dbPath + s); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the incomplete catalog: %w", err)
		}
	}
	if backup == "" {
		return nil
	}
	for _, s := range CatalogFileSuffixes {
		err := RenameNoReplace(backup+s, dbPath+s)
		if err == nil || (s != "" && errors.Is(err, fs.ErrNotExist)) {
			continue
		}
		return fmt.Errorf("restore the catalog backup: %w; rename %s back to %s by hand", err, backup+s, dbPath+s)
	}
	return nil
}
