package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate <library>",
		Short: "Upgrade the library catalog to the current schema",
		Long: `Upgrade the catalog (media.db) of an existing library to the schema of this
version of picmover. A catalog from the previous version is backed up first as
media.db.v1-<timestamp>.bak next to it. db, verify and update-metadata refuse
to work on a catalog that needs migration; import migrates automatically.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			// Probe first: a current catalog is left alone, and a missing one
			// is an error rather than a new library.
			_, db, err := openExisting(ctx, args[0])
			if err == nil {
				db.Close()
				fmt.Fprintln(out, "Catalog is already up to date.")
				return nil
			}
			if !errors.Is(err, catalog.ErrNeedsMigration) {
				return err
			}

			lib, err := library.Open(args[0])
			if err != nil {
				return err
			}
			before, err := backups(lib)
			if err != nil {
				return err
			}
			db, err = catalog.Open(ctx, lib.DBPath(), stderrLogger())
			if err != nil {
				return err
			}
			defer db.Close()
			var rows int64
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&rows); err != nil {
				return fmt.Errorf("count rows: %w", err)
			}
			after, err := backups(lib)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Catalog migrated to schema version %d: %d rows.\n", catalog.SchemaVersion, rows)
			for _, b := range after {
				if !slices.Contains(before, b) {
					fmt.Fprintf(out, "Backup of the old catalog: %s\n", b)
				}
			}
			return nil
		},
	}
}

// backups lists the v1 catalog backups in the library root.
func backups(lib *library.Library) ([]string, error) {
	ents, err := os.ReadDir(lib.Root())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, library.DBName+".v1-") && strings.HasSuffix(n, ".bak") {
			out = append(out, filepath.Join(lib.Root(), n))
		}
	}
	return out, nil
}
