package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/importrec"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

func newImportsCmd() *cobra.Command {
	var (
		rename, del int64
	)
	c := &cobra.Command{
		Use:   "imports <library> [--rename <id> <name> | --delete <id>]",
		Short: "List, rename and delete the library's import records",
		Long: `List the library's import history, newest first: id, start time, mode,
outcome, source folder name, files imported, already in place,
duplicates and errors, bytes copied, and the record's name.

Every import and rebuild is recorded in user.db; the first time a
library is opened by this version (import, rebuild, serve, or a rename
or delete here) its catalog's earlier import sessions are copied there
too. Listing reads user.db only and changes nothing, so it also works on
a read-only library, except that the record of an import whose process
died (outcome running) is closed as failed when the library is not in
use.

  picmover imports <library> --rename <id> <name>   (an empty name clears it)
  picmover imports <library> --delete <id>

Deleting a record forgets it; no file is touched.`,
		Args: func(c *cobra.Command, args []string) error {
			want := 1
			if c.Flags().Changed("rename") {
				want = 2
			}
			if len(args) != want {
				return usageError(c, fmt.Errorf("accepts %d arg(s), received %d", want, len(args)))
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError(c, errors.New("empty library argument"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			renaming, deleting := cmd.Flags().Changed("rename"), cmd.Flags().Changed("delete")
			switch {
			case renaming && deleting:
				return unusable(errors.New("--rename and --delete exclude each other"))
			case renaming && rename <= 0, deleting && del <= 0:
				return unusable(errors.New("invalid import id: want a positive number"))
			case !renaming && !deleting:
				return listImports(ctx, out, args[0])
			}
			lib, db, unlock, err := openExistingLocked(ctx, args[0], "", cmd.ErrOrStderr())
			if err != nil {
				return unusable(err)
			}
			defer unlock()
			defer db.Close()
			fmt.Fprintf(out, "Library: %s\n", lib.Root())
			user, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
			if err != nil {
				return unusable(err)
			}
			defer user.Close()
			if from, bak := user.Migrated(); from != 0 {
				fmt.Fprintln(out, migrationNotice(from, bak))
			}
			if _, err := importrec.Backfill(ctx, db, user); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: the catalog's import sessions could not be copied into the history: %v\n", err)
			}
			if renaming {
				rec, err := user.RenameImport(ctx, rename, args[1])
				if errors.Is(err, userdata.ErrNotFound) {
					return unusable(fmt.Errorf("the library has no import record %d", rename))
				} else if err != nil {
					return unusable(err)
				}
				if rec.Name == "" {
					fmt.Fprintf(out, "Import record #%d: name cleared\n", rec.ID)
				} else {
					fmt.Fprintf(out, "Import record #%d renamed to %q\n", rec.ID, rec.Name)
				}
				return nil
			}
			if err := user.DeleteImport(ctx, del); errors.Is(err, userdata.ErrNotFound) {
				return unusable(fmt.Errorf("the library has no import record %d", del))
			} else if err != nil {
				return unusable(err)
			}
			fmt.Fprintf(out, "Deleted import record #%d; its files were left as they are.\n", del)
			return nil
		},
	}
	c.Flags().Int64Var(&rename, "rename", 0, "rename the record with this id to the second argument")
	c.Flags().Int64Var(&del, "delete", 0, "delete the record with this id (the files stay)")
	return backupCommand(c)
}

// listImports prints the import history of the library at dir. It opens
// user.db read-only and does not need the catalog (which may be of an
// older format, or missing): a user.db without history (missing, or a
// version before 3, which the next import or serve upgrades) lists
// nothing. Before that, records left running by a dead process are
// closed when that is possible quietly (closeCrashedQuietly).
func listImports(ctx context.Context, out io.Writer, dir string) error {
	lib, err := library.Attach(dir)
	if err != nil {
		return unusable(err)
	}
	userPath := filepath.Join(lib.Root(), userdata.FileName)
	if !exists(lib.DBPath()) && !exists(userPath) {
		return unusable(fmt.Errorf("no %s in %s: not a picmover library", library.DBName, lib.Root()))
	}
	fmt.Fprintf(out, "Library: %s\n", lib.Root())
	closeCrashedQuietly(ctx, lib, userPath)
	user, err := userdata.OpenReadOnly(ctx, userPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(out, "No import records.")
		return nil
	case errors.Is(err, userdata.ErrNeedsMigration):
		fmt.Fprintln(out, "No import records (user.db has schema version 1; the next import or serve upgrades it and copies the catalog's import sessions).")
		return nil
	case err != nil:
		return unusable(err)
	}
	defer user.Close()
	v, err := user.Version(ctx)
	if err != nil {
		return unusable(err)
	}
	if v < 3 {
		fmt.Fprintf(out, "No import records (user.db has schema version %d; the next import or serve upgrades it and copies the catalog's import sessions).\n", v)
		return nil
	}
	recs, err := user.ListImports(ctx)
	if err != nil {
		return unusable(err)
	}
	if len(recs) == 0 {
		fmt.Fprintln(out, "No import records.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTARTED\tMODE\tOUTCOME\tSOURCE\tIMPORTED\tEXISTING\tDUPLICATES\tERRORS\tBYTES\tNAME")
	for _, r := range recs {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n", r.ID, formatWhen(r.StartedAt), r.Mode,
			r.Outcome, r.SourceName, r.Imported, r.Existing, r.Duplicates, r.Errors, humanSize(r.Bytes), r.Name)
	}
	return tw.Flush()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// closeCrashedQuietly closes the records left running by a process that
// died (importrec.CloseCrashed), with the rows of their catalog session
// as items, and skips that silently whenever it cannot be done safely or
// at all: no running record, a user.db not at the current version (no
// migration here), the library lock held by another process (whose run
// may own the record) or not available, a catalog that cannot be read,
// or a user.db that cannot be written.
func closeCrashedQuietly(ctx context.Context, lib *library.Library, userPath string) {
	ro, err := userdata.OpenReadOnly(ctx, userPath)
	if err != nil {
		return
	}
	v, err := ro.Version(ctx)
	var running []userdata.ImportRecord
	if err == nil && v == userdata.SchemaVersion {
		running, err = ro.RunningImports(ctx)
	}
	ro.Close()
	if err != nil || len(running) == 0 {
		return
	}
	unlock, err := lib.Lock(ctx, "imports")
	if err != nil {
		if unlock != nil {
			unlock()
		}
		return
	}
	defer unlock()
	// The rows of every session first: a catalog that cannot be read
	// skips the whole step rather than closing records without items.
	rows := map[int64][]uint64{}
	newest := map[int64]uint64{}
	for _, r := range running {
		if r.CatalogSession == 0 || !r.KeepsItems() {
			continue
		}
		hs, n, err := catalog.SessionHashesFile(ctx, lib.DBPath(), r.CatalogSession)
		if err != nil {
			return
		}
		rows[r.CatalogSession], newest[r.CatalogSession] = hs, n
	}
	u, err := userdata.Open(ctx, userPath)
	if err != nil {
		return
	}
	defer u.Close()
	importrec.CloseCrashed(ctx, u, func(_ context.Context, s int64) ([]uint64, uint64, error) {
		return rows[s], newest[s], nil
	}, nil, nil)
}
