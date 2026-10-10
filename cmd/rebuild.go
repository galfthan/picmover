package cmd

import (
	"context"
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
	"picmover/internal/userdata"
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
Files in the trash (<library>/.picmover/trash) are recorded as trashed, with
the time they were trashed from user.db (else their modification time);
user.db itself (ratings, tags, albums) is never changed by a rebuild.
Finally the thumbnail stage runs (skip it with --no-thumbs and run
'picmover thumbs' later); thumbnails and previews are cached by content
hash in <library>/.picmover and are reused.

A folder that has no media.db yet (for example a library laid out by an
earlier version) is handled the same way, without a backup (the folder
must exist): this records an
existing folder tree without copying or moving anything, which 'import'
would do for files outside the layout.

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
			// The library directory must exist: a mistyped path must not
			// become a new library.
			lib, err := library.Attach(args[0])
			if err != nil {
				return err
			}
			root, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			// Hold the library lock from before the backup until the
			// import has finished: a running 'serve' or import must never
			// see its catalog renamed away.
			unlock, err := lockLibrary(cmd.Context(), lib, "rebuild", cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer unlock()
			dbPath := filepath.Join(root, library.DBName)
			hasCatalog := true
			if fi, err := os.Stat(dbPath); err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				hasCatalog = false
			} else if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", dbPath)
			}
			// From here on Ctrl+C must not interrupt between the backup and
			// the import without the summary that names the backup.
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			out := cmd.OutOrStdout()
			// user.db is read before the backup: an unreadable one must
			// never leave the library without its catalog.
			if p.trashTimes, err = trashTimes(ctx, root); err != nil {
				return err
			}
			if hasCatalog {
				backup, err := backupCatalog(dbPath, time.Now())
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "Old catalog saved as %s\n", backup)
				p.backup = backup
			} else {
				fmt.Fprintln(out, missingCatalogNote(root))
				p.isNew = true
			}
			p.source, p.libRoot, p.zone = root, root, zone
			p.recordInPlace = true
			p.locked = true
			p.logPrefix, p.what = "rebuild", "Rebuild"
			return runImport(cmd, ctx, p)
		},
	}
	f := c.Flags()
	f.IntVar(&p.workers, "workers", 0, "number of parallel workers (0 = auto)")
	f.StringVar(&zoneName, "timezone", "", "IANA zone for times stored as instants (video UTC, file mtime); default the system zone")
	f.BoolVar(&p.quiet, "quiet", false, "do not show the live status line")
	f.BoolVar(&p.noThumbs, "no-thumbs", false, "do not generate thumbnails and previews (run 'picmover thumbs' later)")
	return c
}

// trashTimes reads the trash timestamps from the library's user.db, or
// returns nil when there is none (it is not created).
func trashTimes(ctx context.Context, root string) (map[uint64]time.Time, error) {
	p := filepath.Join(root, userdata.FileName)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	u, err := userdata.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	defer u.Close()
	return u.TrashedAt(ctx, nil)
}

// missingCatalogNote describes a rebuild of a library that has no media.db
// (the files are recorded into a new catalog) and names the newest catalog
// backup when any exist, so a user who meant to restore one notices.
func missingCatalogNote(root string) string {
	baks, _ := filepath.Glob(filepath.Join(root, library.DBName+".bak-*"))
	var mains []string
	for _, b := range baks {
		name := filepath.Base(b)
		if library.IsCatalogBackup(name) && !strings.HasSuffix(name, "-wal") &&
			!strings.HasSuffix(name, "-shm") && !strings.HasSuffix(name, "-journal") {
			mains = append(mains, b)
		}
	}
	note := fmt.Sprintf("No %s in %s; recording the files into a new catalog.", library.DBName, root)
	if len(mains) == 0 {
		return note
	}
	sort.Strings(mains) // the timestamp sorts chronologically
	newest := mains[len(mains)-1]
	return note + fmt.Sprintf("\n%d catalog backup(s) exist, the newest %s; they are left alone. "+
		"To use one instead, delete the new %s (and its -wal/-shm files) afterwards and rename the backup "+
		"(and its -wal/-shm/-journal files, if any) back to %s.",
		len(mains), filepath.Base(newest), library.DBName, library.DBName)
}

// backupCatalog is library.BackupCatalog.
func backupCatalog(dbPath string, now time.Time) (string, error) {
	return library.BackupCatalog(dbPath, now)
}
