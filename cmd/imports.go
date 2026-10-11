package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"picmover/internal/importrec"
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
library is opened by this version its catalog's earlier import sessions
are copied there too.

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
			}
			lib, db, unlock, err := openExistingLocked(ctx, args[0], "", cmd.ErrOrStderr())
			if err != nil {
				return unusable(err)
			}
			defer unlock()
			defer db.Close()
			user, err := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
			if err != nil {
				return unusable(err)
			}
			defer user.Close()
			if from, bak := user.Migrated(); from != 0 {
				fmt.Fprintf(out, "user.db upgraded from schema version %d to %d (the old file is kept as %s)\n",
					from, userdata.SchemaVersion, filepath.Base(bak))
			}
			if _, err := importrec.Backfill(ctx, db, user); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: the catalog's import sessions could not be copied into the history: %v\n", err)
			}
			switch {
			case renaming:
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
			case deleting:
				if err := user.DeleteImport(ctx, del); errors.Is(err, userdata.ErrNotFound) {
					return unusable(fmt.Errorf("the library has no import record %d", del))
				} else if err != nil {
					return unusable(err)
				}
				fmt.Fprintf(out, "Deleted import record #%d; its files were left as they are.\n", del)
				return nil
			}
			recs, err := user.ListImports(ctx)
			if err != nil {
				return unusable(err)
			}
			fmt.Fprintf(out, "Library: %s\n", lib.Root())
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
		},
	}
	c.Flags().Int64Var(&rename, "rename", 0, "rename the record with this id to the second argument")
	c.Flags().Int64Var(&del, "delete", 0, "delete the record with this id (the files stay)")
	return backupCommand(c)
}
