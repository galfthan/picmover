package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"picmover/internal/backup"
	"picmover/internal/library"
)

func newRestoreCmd() *cobra.Command {
	var (
		mode                       string
		force, thumbs, dryRun, qui bool
	)
	c := &cobra.Command{
		Use:   "restore <target-folder> <set> <library>",
		Short: "Copy files back from a backup set into a library",
		Long: `Copy files from a backup set back into a library, which may be empty (a
new disk) or partial.

--mode missing (the default) copies every backed-up file whose path the
library does not have and whose content it does not have under another
path, verifying each copy; existing files are never overwritten. user.db
comes back too when the library has none. --mode user-data copies only
user.db, and replaces an existing one only with --force (the current file
is kept as user.db.bak-<time>).

When the library has no media.db afterwards, the files are recorded into
a new catalog (an in-place rebuild; thumbnails only with --thumbs).
Otherwise the catalog is left alone: run 'picmover verify <library>'.

Exit status: 0 everything restored; 1 some files failed (listed); 2 the
target or the library could not be used; 130 cancelled.`,
		Args: rangeArgs(3, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if mode != backup.ModeMissing && mode != backup.ModeUserData {
				return unusable(fmt.Errorf("--mode must be %s or %s", backup.ModeMissing, backup.ModeUserData))
			}
			if force && mode != backup.ModeUserData {
				return unusable(errors.New("--force only applies to --mode user-data"))
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			out := cmd.OutOrStdout()
			t, err := backup.OpenTarget(args[0])
			if err != nil {
				return unusable(fmt.Errorf("target %s: %w", args[0], err))
			}
			if !backup.SetExists(t, args[1]) {
				return unusable(fmt.Errorf("the target has no backup set %q", args[1]))
			}
			if err := backup.CheckSeparate(t.Root, args[2]); err != nil {
				return unusable(err)
			}
			set, err := backup.OpenSet(ctx, t, args[1], false)
			if err != nil {
				return unusable(err)
			}
			defer set.Close()
			var lib *library.Library
			if dryRun {
				if lib, err = library.Attach(args[2]); errors.Is(err, fs.ErrNotExist) {
					lib, err = libraryAt(args[2])
				}
			} else {
				lib, err = library.Open(args[2])
			}
			if err != nil {
				return unusable(err)
			}
			if !dryRun {
				unlock, err := lockLibrary(ctx, lib, "restore", cmd.ErrOrStderr())
				if err != nil {
					return unusable(err)
				}
				defer unlock()
			}
			logger, logPath, closeLog := slogDiscard(), "", func() {}
			if !dryRun {
				if logger, logPath, closeLog, err = libraryLog(lib, "restore"); err != nil {
					return unusable(err)
				}
			}
			defer closeLog()
			progress, done := progressFunc(out, qui || dryRun)
			st, err := backup.Restore(ctx, set, lib, backup.RestoreOptions{Mode: mode, Force: force, DryRun: dryRun,
				Library: lib.Root(), OnProgress: progress, Logger: logger})
			done()
			if err != nil {
				return unusable(err)
			}
			if dryRun {
				printNonEmpty(out, "Would restore", st.Paths)
				fmt.Fprintf(out, "Would restore %d files (%s); %d are in the library already, %d under another path. user.db: %s\n",
					st.Restored, humanSize(st.Bytes), st.Present, st.Elsewhere, st.UserDB)
				return nil
			}
			fmt.Fprintf(out, "Restore summary:\n")
			fmt.Fprintf(out, "Restored:          %d (%s)\n", st.Restored, humanSize(st.Bytes))
			fmt.Fprintf(out, "Already present:   %d\n", st.Present)
			fmt.Fprintf(out, "Under another path: %d\n", st.Elsewhere)
			fmt.Fprintf(out, "Failed:            %d\n", st.FailedCount)
			fmt.Fprintf(out, "user.db:           %s\n", st.UserDB)
			var failed []string
			for _, f := range st.Failures {
				failed = append(failed, f.Path+": "+f.Error)
			}
			printNonEmpty(out, "Failed files", failed)
			fmt.Fprintf(out, "Log: %s\n", logPath)
			code := st.ExitCode()
			if st.NeedsRebuild && mode == backup.ModeMissing {
				fmt.Fprintf(out, "The library has no catalog; recording the restored files.\n")
				if c, err := restoreRebuild(ctx, cmd, lib, !thumbs); err != nil {
					return err
				} else {
					code = max(code, c)
				}
			} else if st.Restored > 0 {
				fmt.Fprintf(out, "The catalog was left alone; check it with 'picmover verify %s' "+
					"(restored files it does not list show as orphans; 'picmover rebuild' records them).\n", library.QuoteArg(lib.Root()))
			}
			if code != 0 {
				return &exitError{code: code, msg: "some files were not restored; see the list above and the log"}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&mode, "mode", backup.ModeMissing, "missing (files the library lacks) or user-data (user.db only)")
	f.BoolVar(&force, "force", false, "user-data: replace an existing user.db (it is kept as user.db.bak-<time>)")
	f.BoolVar(&thumbs, "thumbs", false, "when the catalog is rebuilt, also generate thumbnails")
	f.BoolVar(&dryRun, "dry-run", false, "print what would be restored; copy nothing")
	f.BoolVar(&qui, "quiet", false, "do not show the live status line")
	return backupCommand(c)
}

// libraryAt is a library value for a folder that does not exist (a dry
// run into a new folder): nothing is created.
func libraryAt(dir string) (*library.Library, error) {
	parent := filepath.Dir(dir)
	if _, err := os.Stat(parent); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "picmover-restore-dry-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// An empty library: Restore only needs its root to list nothing.
	return library.Attach(tmp)
}

// restoreRebuild records the restored files into a new catalog, as
// "picmover rebuild" does; the library lock is held by the caller. It
// returns 1 when files could not be recorded.
func restoreRebuild(ctx context.Context, cmd *cobra.Command, lib *library.Library, noThumbs bool) (int, error) {
	times, err := trashTimes(ctx, lib.Root())
	if err != nil {
		return exitUnusable, unusable(err)
	}
	p := importParams{source: lib.Root(), libRoot: lib.Root(), recordInPlace: true, trashTimes: times,
		locked: true, isNew: true, noThumbs: noThumbs, quiet: true, logPrefix: "rebuild", what: "Rebuild"}
	err = runImport(cmd, ctx, p)
	var ee *exitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee) && ee.code == 130:
		return 130, err
	case errors.As(err, &ee) && ee.code == 2:
		// Per-file or thumbnail problems: the catalog exists.
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", ee.msg)
		return exitFiles, nil
	}
	return exitUnusable, unusable(err)
}
