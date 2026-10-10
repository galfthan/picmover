package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/backup"
)

// pickSet returns the set to work on: name when given, else the target's
// only set.
func pickSet(t *backup.Target, name string) (string, error) {
	if name != "" {
		if !backup.SetExists(t, name) {
			return "", fmt.Errorf("the target has no backup set %q", name)
		}
		return name, nil
	}
	sets, err := backup.Sets(t)
	if err != nil {
		return "", err
	}
	switch len(sets) {
	case 0:
		return "", errors.New("the target holds no backup sets yet")
	case 1:
		return sets[0], nil
	}
	return "", fmt.Errorf("the target holds %d backup sets (%v); name one", len(sets), sets)
}

func newScrubCmd() *cobra.Command {
	var (
		budgetFlag, libDir string
		allFlag, noRepair  bool
		takeOver           bool
		quiet              bool
	)
	c := &cobra.Command{
		Use:   "scrub <target-folder> [<set>]",
		Short: "Re-read a backup set and check it against what was backed up",
		Long: `Re-read the files of a backup set and compare them with the hashes recorded
when they were copied, least recently verified first. With --budget the
scrub stops after that long and the next one continues where it stopped,
so a large target is checked over several runs. Files whose content no
longer matches are damaged; files gone from the target are missing; files
on the target that the set does not know are listed as unknown.

With --library the damaged and missing files are copied again from the
library when it still has the right content (the damaged copy is kept
under <set>/.picmover/previous/); --no-repair only reports. The set is
the one given, the one belonging to --library, or the target's only set.

Exit status: 0 clean; 1 damaged or missing files found (repaired ones
included); 2 the target or the library could not be used; 130 cancelled.`,
		Args: rangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if budgetFlag != "" && allFlag {
				return unusable(errors.New("--budget and --all exclude each other"))
			}
			budget, err := parseBudget(budgetFlag)
			if err != nil {
				return unusable(err)
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			out := cmd.OutOrStdout()
			t, err := backup.OpenTarget(args[0])
			if err != nil {
				return unusable(fmt.Errorf("target %s: %w", args[0], err))
			}
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			var b *backupLib
			if libDir != "" {
				lockName := ""
				if !noRepair {
					lockName = "scrub"
				}
				if b, err = openBackupLib(ctx, libDir, lockName, out, cmd.ErrOrStderr()); err != nil {
					return unusable(err)
				}
				defer b.close()
				if err := backup.CheckSeparate(t.Root, b.lib.Root()); err != nil {
					return unusable(err)
				}
				if name == "" {
					if bt, err := b.user.BackupTarget(ctx, t.ID()); err == nil && bt.SetName != "" {
						name = bt.SetName
					} else if found, err := backup.FindSet(ctx, t, b.libID); err == nil {
						name = found
					}
				}
			}
			if name, err = pickSet(t, name); err != nil {
				return unusable(err)
			}
			set, err := backup.OpenSet(ctx, t, name, false)
			if err != nil {
				return unusable(err)
			}
			defer set.Close()
			opts := backup.ScrubOptions{Budget: budget, Library: t.Root}
			var logger *slog.Logger
			logPath := ""
			if b != nil {
				if err := set.CheckOwner(ctx, b.id, false); err != nil && !((takeOver || noRepair) && errors.Is(err, backup.ErrForeignInstance)) {
					if errors.Is(err, backup.ErrForeignInstance) {
						return unusable(fmt.Errorf("%w\n  to repair it from this library anyway, run again with --take-over", err))
					}
					return unusable(fmt.Errorf("the set %s belongs to another library; it cannot be repaired from %s", name, b.dir))
				}
				l, p, closeLog, err := libraryLog(b.lib, "scrub")
				if err != nil {
					return unusable(err)
				}
				defer closeLog()
				logger, logPath = l, p
				opts.Logger, opts.Library = logger, b.lib.Root()
				if !noRepair {
					src := b.source()
					opts.Repair, opts.Source, opts.Owner, opts.TakeOver = true, &src, b.id, takeOver
				}
			}
			progress, done := progressFunc(out, quiet)
			opts.OnProgress = progress
			fmt.Fprintf(out, "Scrubbing set %s on %s\n", name, t.Root)
			st, err := backup.Scrub(ctx, set, opts)
			done()
			if err != nil {
				return unusable(err)
			}
			if b != nil {
				if err := b.user.MarkScrub(ctx, t.ID(), time.Now()); err != nil && logger != nil {
					logger.Info("the target is not attached to the library; scrub time not recorded")
				}
			}
			printScrubSummary(out, &st)
			if logPath != "" {
				fmt.Fprintf(out, "Log: %s\n", logPath)
			}
			if st.ExitCode() != 0 {
				return &exitError{code: exitFiles, msg: fmt.Sprintf("%d damaged and %d missing files (%d repaired)",
					st.DamagedCount, st.MissingCount, st.Repaired)}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&budgetFlag, "budget", "", "stop after this long (e.g. 30m, 2h); the next scrub continues")
	f.BoolVar(&allFlag, "all", false, "read the whole set (the default)")
	f.BoolVar(&noRepair, "no-repair", false, "only report; do not copy damaged files again")
	f.StringVar(&libDir, "library", "", "the library the set backs up, to repair from")
	f.BoolVar(&takeOver, "take-over", false, "with --library: repair a set last written by another copy of this library, making this library its owner")
	f.BoolVar(&quiet, "quiet", false, "do not show the live status line")
	return backupCommand(c)
}

func printScrubSummary(w io.Writer, st *backup.ScrubStats) {
	fmt.Fprintf(w, "Scrub summary:\n")
	fmt.Fprintf(w, "Checked:   %d files (%s)\n", st.Checked, humanSize(st.Bytes))
	fmt.Fprintf(w, "Damaged:   %d\n", st.DamagedCount)
	fmt.Fprintf(w, "Missing:   %d\n", st.MissingCount)
	fmt.Fprintf(w, "Repaired:  %d\n", st.Repaired)
	fmt.Fprintf(w, "Unknown:   %d\n", st.UnknownCount)
	if st.PassComplete {
		fmt.Fprintf(w, "The whole set has been read in this pass.\n")
	} else {
		fmt.Fprintf(w, "Still to read in this pass: %d files (the next scrub continues)\n", st.Remaining)
	}
	var dam []string
	for _, d := range st.Damaged {
		s := d.Path
		if d.Repaired {
			s += " (repaired)"
		}
		dam = append(dam, s)
	}
	printNonEmpty(w, "Damaged files", dam)
	printNonEmpty(w, "Missing files", st.Missing)
	printNonEmpty(w, "Unknown files (not in the set)", st.Unknown)
}

func newPruneCmd() *cobra.Command {
	var (
		keepFlag string
		yes      bool
		fl       pruneFlags
	)
	c := &cobra.Command{
		Use:   "prune <library> <target>",
		Short: "Remove from a backup what the library no longer has",
		Long: `List, and with --yes remove, the files of the library's backup set on a
target (a folder, or a remembered target's id) whose path and content are
both gone from the library (live or in its trash), and the earlier
versions under <set>/.picmover/previous/ older than --keep (default 30d).
--keep 0d keeps no earlier versions. The user.db and media.db snapshots
and files the set does not know are never removed; nothing in the
library is touched.

When the library could not be read completely (an unreadable folder) or
catalog files are missing in it, files it still has could look deleted:
the list is shown with a warning and --yes refuses unless --force. A set
last written by another copy of this library is refused unless
--take-over.

Exit status: 0; 1 when files could not be removed; 2 the target or the
library could not be used.`,
		Args: rangeArgs(2, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			keep := backup.DefaultKeep
			if keepFlag != "" {
				var err error
				if keep, err = parseDays(keepFlag); err != nil {
					return unusable(err)
				}
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			out := cmd.OutOrStdout()
			b, err := openBackupLib(ctx, args[0], "prune", out, cmd.ErrOrStderr())
			if err != nil {
				return unusable(err)
			}
			defer b.close()
			folder, id := args[1], ""
			if backup.ValidID(args[1]) {
				folder, id = "", args[1]
			}
			targets, err := resolveTargets(ctx, b, folder, id, false, "prune", out)
			if err != nil {
				return unusable(err)
			}
			rt, err := reach(ctx, b, targets[0], "prune", fl.takeOver)
			if err != nil {
				return unusable(err)
			}
			if !backup.SetExists(rt.t, rt.setName) {
				return unusable(errors.New("the target has no backup of this library yet"))
			}
			set, err := backup.OpenSet(ctx, rt.t, rt.setName, false)
			if err != nil {
				return unusable(err)
			}
			defer set.Close()
			logger, logPath, closeLog, err := libraryLog(b.lib, "prune")
			if err != nil {
				return unusable(err)
			}
			defer closeLog()
			fl.yes = yes
			code, err := pruneSet(ctx, cmd, b, set, keep, fl, logger)
			if err != nil {
				return unusable(err)
			}
			if yes {
				fmt.Fprintf(out, "Log: %s\n", logPath)
			}
			if code != 0 {
				return &exitError{code: code, msg: "some files could not be removed; see the log"}
			}
			return nil
		},
	}
	c.Flags().StringVar(&keepFlag, "keep", "30d", "keep earlier versions in previous/ for this long (e.g. 30d, 72h)")
	c.Flags().BoolVar(&yes, "yes", false, "remove the listed files")
	c.Flags().BoolVar(&fl.force, "force", false, "prune even when the library could not be read completely or catalog files are missing")
	c.Flags().BoolVar(&fl.takeOver, "take-over", false, "prune a set last written by another copy of this library, making this library its owner")
	return backupCommand(c)
}

// pruneFlags are the options of pruneSet.
type pruneFlags struct {
	yes      bool // remove the listed files
	force    bool // even when the library scan was incomplete
	takeOver bool // adopt a set last written by another copy of the library
}

// pruneSet lists what a prune removes and, with yes, removes it. While
// the library could not be read completely (or catalog files are
// missing) the list may name files the library still has: it is shown
// with a warning, and removing needs force.
func pruneSet(ctx context.Context, cmd *cobra.Command, b *backupLib, set *backup.Set, keep time.Duration, fl pruneFlags, logger *slog.Logger) (int, error) {
	out := cmd.OutOrStdout()
	yes := fl.yes
	plan, err := backup.PlanFor(ctx, b.source(), set)
	if err != nil {
		return exitUnusable, err
	}
	incomplete := plan.CheckComplete()
	if incomplete != nil {
		printNonEmpty(out, "Could not be read in the library", failureLines(plan.Errors))
		printNonEmpty(out, "In the catalog but missing in the library", plan.Missing)
		fmt.Fprintf(out, "Warning: %v\n", incomplete)
	}
	pp, err := backup.PlanPrune(set, plan, keep, time.Now())
	if err != nil {
		return exitUnusable, err
	}
	if pp.Count == 0 {
		fmt.Fprintln(out, "Nothing to prune.")
		return 0, nil
	}
	list := make([]string, 0, len(pp.Items))
	for _, it := range pp.Items {
		list = append(list, it.Path)
	}
	printNonEmpty(out, "To remove from the target", list)
	fmt.Fprintf(out, "%d files, %s.\n", pp.Count, humanSize(pp.Bytes))
	if !yes {
		fmt.Fprintf(out, "Nothing was removed; run again with --yes to remove these %d files.\n", pp.Count)
		return 0, nil
	}
	if incomplete != nil && !fl.force {
		return exitUnusable, fmt.Errorf("%w (--force)", incomplete)
	}
	st, err := backup.Prune(ctx, b.source(), set, backup.PruneOptions{Keep: keep, Expected: pp.Count,
		Owner: b.id, TakeOver: fl.takeOver, Force: fl.force, Library: b.lib.Root(), Logger: logger})
	if err != nil {
		return exitUnusable, err
	}
	fmt.Fprintf(out, "Removed %d files (%s).\n", st.Removed, humanSize(st.Bytes))
	var failed []string
	for _, f := range st.Failures {
		failed = append(failed, f.Path+": "+f.Error)
	}
	printNonEmpty(out, "Could not remove", failed)
	if len(st.Failures) > 0 {
		return exitFiles, nil
	}
	return 0, nil
}
