package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/library"
)

func newRebuildCmd() *cobra.Command {
	var (
		p        importParams
		zoneName string
	)
	c := &cobra.Command{
		Use:   "rebuild <library>",
		Short: "Recreate the catalog from the files in the library",
		Long: `Recreate the catalog (media.db) of a library from its files. This is the
upgrade path whenever the catalog format changes.

The current media.db (and its -wal, -shm and -journal files) is renamed to
media.db.bak-<YYYYMMDD-HHMMSS> first. Then every media file under the
library root is recorded where it is, whatever folder it is in: nothing is
copied, moved, renamed or deleted. A file whose content is already recorded
(an identical copy elsewhere in the library) is reported as a duplicate and
left in place. Zip archives are not extracted. To move files into the
<type>/YYYY/MM layout use 'picmover import <library> <library>' instead.
Finally the thumbnail stage runs; thumbnails and previews are cached by
content hash in <library>/.picmover and are reused.

To undo a rebuild, delete the new media.db (and its -wal/-shm) and rename
the newest media.db.bak-<timestamp> files back to media.db.

Exit status as for import.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if p.workers < 0 {
				return fmt.Errorf("--workers must be >= 0")
			}
			zone, err := resolveZone(zoneName)
			if err != nil {
				return err
			}
			root, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			dbPath := filepath.Join(root, library.DBName)
			if fi, err := os.Stat(dbPath); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return missingCatalogError(root)
				}
				return err
			} else if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", dbPath)
			}
			// From here on Ctrl+C must not interrupt between the backup and
			// the import without the summary that names the backup.
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			backup, err := backupCatalog(dbPath, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Old catalog saved as %s\n", backup)
			p.source, p.libRoot, p.zone = root, root, zone
			p.recordInPlace = true
			p.logPrefix, p.what = "rebuild", "Rebuild"
			return runImport(cmd, ctx, p)
		},
	}
	f := c.Flags()
	f.IntVar(&p.workers, "workers", 0, "number of parallel workers (0 = auto)")
	f.StringVar(&zoneName, "timezone", "", "IANA zone for times stored as instants (video UTC, file mtime); default the system zone")
	f.BoolVar(&p.quiet, "quiet", false, "do not show the live status line")
	return c
}

// missingCatalogError explains a rebuild of a library without media.db:
// when backups of an earlier catalog exist, how to restore the newest one,
// otherwise how to create a library.
func missingCatalogError(root string) error {
	baks, _ := filepath.Glob(filepath.Join(root, library.DBName+".bak-*"))
	var mains []string
	for _, b := range baks {
		name := filepath.Base(b)
		if library.IsCatalogBackup(name) && !strings.HasSuffix(name, "-wal") &&
			!strings.HasSuffix(name, "-shm") && !strings.HasSuffix(name, "-journal") {
			mains = append(mains, b)
		}
	}
	if len(mains) == 0 {
		return fmt.Errorf("no %s in %s: nothing to rebuild; to create a library use 'picmover import <source> %s'",
			library.DBName, root, quoteArg(root))
	}
	sort.Strings(mains) // the timestamp sorts chronologically
	newest := mains[len(mains)-1]
	return fmt.Errorf("no %s in %s, but %d catalog backup(s) exist, the newest %s.\n"+
		"To restore it, rename it (and its -wal/-shm/-journal files, if any) back to %s.\n"+
		"To start a new catalog instead, run 'picmover import %s %s' (files outside the <type>/YYYY/MM layout are copied into it)",
		library.DBName, root, len(mains), filepath.Base(newest), library.DBName, quoteArg(root), quoteArg(root))
}

// catalogFileSuffixes are the files that make up a catalog: the database,
// its WAL and shared memory, and a rollback journal. They are renamed
// together so a catalog is never separated from them.
var catalogFileSuffixes = []string{"", "-wal", "-shm", "-journal"}

// backupCatalog renames dbPath and its -wal/-shm/-journal files to
// <dbPath>.bak-<YYYYMMDD-HHMMSS> (plus the same suffixes), so SQLite still
// finds the WAL or journal of the backup. It never overwrites: an existing
// backup name is an error. If a rename fails, the renames already done are
// undone, so a catalog is never separated from its WAL. It returns the
// backup path of the main file.
func backupCatalog(dbPath string, now time.Time) (string, error) {
	backup := dbPath + ".bak-" + now.Format(library.BackupStampLayout)
	suffixes := catalogFileSuffixes
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
