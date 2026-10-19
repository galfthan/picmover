package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/backup"
	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// Exit codes of the backup commands (backup, scrub, prune, restore,
// targets): 0 everything verified; 1 some files failed (listed), a scrub
// found damage, or a partial backup left files; 2 the target or the
// library could not be used (and usage errors); 130 cancelled.
const (
	exitFiles    = 1
	exitUnusable = 2
)

// unusable wraps err as exit status 2.
func unusable(err error) error {
	if err == nil {
		return nil
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &exitError{code: 130, msg: "cancelled"}
	}
	return &exitError{code: exitUnusable, msg: err.Error()}
}

// backupCommand marks c as one of the backup commands: usage errors exit
// with status 2, so a scheduled job never mistakes them for "some files
// failed" (1).
func backupCommand(c *cobra.Command) *cobra.Command {
	c.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &exitError{code: exitUnusable, msg: usageError(c, err).Error()}
	})
	if c.Args != nil {
		args := c.Args
		c.Args = func(c *cobra.Command, a []string) error {
			if err := args(c, a); err != nil {
				return &exitError{code: exitUnusable, msg: err.Error()}
			}
			return nil
		}
	}
	return c
}

// rangeArgs accepts min to max positional arguments, none empty.
func rangeArgs(min, max int) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if err := cobra.RangeArgs(min, max)(c, args); err != nil {
			return usageError(c, err)
		}
		for i, a := range args {
			if strings.TrimSpace(a) == "" {
				return usageError(c, fmt.Errorf("empty argument %d", i+1))
			}
		}
		return nil
	}
}

// backupLib is an opened library for the backup commands.
type backupLib struct {
	lib   *library.Library
	dir   string // the library folder as given, absolute (the default set name comes from it)
	db    *catalog.DB
	user  *userdata.DB
	libID string
	id    backup.Identity
	// readOnly: opened for a dry run (openBackupLibReadOnly); nothing is
	// written, user may be nil.
	readOnly bool
	unlock   func()
}

func (b *backupLib) close() {
	if b.user != nil {
		b.user.Close()
	}
	if b.db != nil {
		b.db.Close()
	}
	if b.unlock != nil {
		b.unlock()
	}
}

func (b *backupLib) source() backup.Source {
	return backup.Source{Lib: b.lib, Catalog: b.db, User: b.user}
}

// openBackupLib opens an existing library, its catalog and user.db
// (created, or migrated to version 2, as needed: it holds the library's
// identity and its targets), taking the library lock unless lockName is
// "".
func openBackupLib(ctx context.Context, dir, lockName string, out, errOut io.Writer) (*backupLib, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	lib, db, unlock, err := openExistingLocked(ctx, dir, lockName, errOut)
	if err != nil {
		return nil, err
	}
	b := &backupLib{lib: lib, dir: filepath.Clean(abs), db: db, unlock: unlock}
	if b.user, err = userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName)); err != nil {
		b.close()
		return nil, err
	}
	if from, bak := b.user.Migrated(); from != 0 {
		fmt.Fprintf(out, "user.db upgraded from schema version %d to %d (the old file is kept as %s)\n",
			from, userdata.SchemaVersion, filepath.Base(bak))
	}
	if b.id, err = b.user.Identity(ctx); err != nil {
		b.close()
		return nil, err
	}
	b.libID = b.id.LibraryID
	return b, nil
}

// openBackupLibReadOnly opens a library for a dry run, writing nothing:
// no lock, user.db read-only (never created or migrated) and the
// library's instance peeked at, not recorded. A user.db that is missing
// or would first be upgraded is reported on out; b.user is then nil (the
// library has no targets yet).
func openBackupLibReadOnly(ctx context.Context, dir string, out, errOut io.Writer) (*backupLib, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	lib, db, unlock, err := openExistingLocked(ctx, dir, "", errOut)
	if err != nil {
		return nil, err
	}
	b := &backupLib{lib: lib, dir: filepath.Clean(abs), db: db, unlock: unlock, readOnly: true}
	u, err := userdata.OpenReadOnly(ctx, filepath.Join(lib.Root(), userdata.FileName))
	switch {
	case errors.Is(err, userdata.ErrNeedsMigration):
		fmt.Fprintf(out, "Note: %v; it has no backup targets yet.\n", err)
		return b, nil
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(out, "Note: the library has no user.db yet (a run without --dry-run creates it); it has no backup targets yet.")
		return b, nil
	case err != nil:
		b.close()
		return nil, err
	}
	b.user = u
	if b.id, err = u.PeekIdentity(ctx); err != nil {
		b.close()
		return nil, err
	}
	b.libID = b.id.LibraryID
	return b, nil
}

// setError adds what the user can do to a set ownership error: another
// set name for another library's set, --take-over for a set last written
// by another copy of this library.
func setError(err error, b *backupLib, t *backup.Target, bt userdata.BackupTarget) error {
	switch {
	case errors.Is(err, backup.ErrForeignInstance):
		return fmt.Errorf("%w\n  if this library is the one to keep backing up, run again with --take-over", err)
	case errors.Is(err, backup.ErrOtherLibrary):
		return fmt.Errorf("%w (target %s); attach the target with another set name: picmover targets add --set <name> %s %s",
			err, bt.ID, library.QuoteArg(b.dir), library.QuoteArg(t.Root))
	}
	return err
}

// resolvedTarget is a target of the library, reachable, with its set name.
type resolvedTarget struct {
	bt      userdata.BackupTarget
	t       *backup.Target
	setName string
}

// targetNotFound explains a remembered target that is not at its last
// path.
func targetNotFound(bt userdata.BackupTarget, libDir, command string) error {
	host := ""
	if bt.LastHost != "" {
		host = " on " + bt.LastHost
	}
	return fmt.Errorf("target %s (%s) is not at %s (last seen there%s); connect it and pass its folder:\n  picmover %s %s <folder>",
		bt.ID, bt.Label, bt.LastPath, host, command, library.QuoteArg(libDir))
}

// attachTarget makes sure the library remembers the target t at its
// current path (attaching it with a label from the folder name when it
// is new) and returns the remembered row.
func attachTarget(ctx context.Context, b *backupLib, t *backup.Target, label string, out io.Writer) (userdata.BackupTarget, error) {
	host, _ := os.Hostname()
	bt, err := b.user.BackupTarget(ctx, t.ID())
	switch {
	case errors.Is(err, userdata.ErrNotFound):
		if label == "" {
			label = targetLabel(t.Root)
		}
		bt, _, err = b.user.AddBackupTarget(ctx, userdata.BackupTarget{ID: t.ID(), Label: label, LastPath: t.Root, LastHost: host})
		if err != nil {
			return bt, err
		}
		fmt.Fprintf(out, "Attached target %s (%s) at %s\n", bt.ID, bt.Label, bt.LastPath)
		return bt, nil
	case err != nil:
		return bt, err
	}
	if bt.LastPath != t.Root || bt.LastHost != host {
		root := t.Root
		u := userdata.TargetUpdate{LastPath: &root, LastHost: &host}
		if label != "" {
			u.Label = &label
		}
		if bt, err = b.user.UpdateBackupTarget(ctx, bt.ID, u); err != nil {
			return bt, err
		}
		fmt.Fprintf(out, "Target %s (%s) is now at %s\n", bt.ID, bt.Label, bt.LastPath)
	}
	return bt, nil
}

func targetLabel(root string) string {
	l := filepath.Base(root)
	if l == "" || l == "." || l == string(filepath.Separator) || strings.HasSuffix(l, ":\\") {
		l = root
	}
	if c, err := userdata.CleanLabel(l); err == nil {
		return c
	}
	return "Backup"
}

// resolveTargets picks the targets of a command: the folder given (a
// target with a marker, attached when new), --target <id> (at its last
// path), every target with all, or the library's only one.
func resolveTargets(ctx context.Context, b *backupLib, folder, id string, all bool, command string, out io.Writer) ([]userdata.BackupTarget, error) {
	switch {
	case folder != "":
		t, err := backup.OpenTarget(folder)
		if errors.Is(err, backup.ErrNoMarker) {
			return nil, fmt.Errorf("%s is not a backup target (it has no %s); attach it first:\n  picmover targets add %s %s",
				folder, backup.MarkerName, library.QuoteArg(b.dir), library.QuoteArg(folder))
		}
		if err != nil {
			return nil, fmt.Errorf("target %s: %w", folder, err)
		}
		if err := backup.CheckSeparate(t.Root, b.lib.Root()); err != nil {
			return nil, err
		}
		if b.readOnly {
			// A dry run does not attach the target.
			if b.user != nil {
				if bt, err := b.user.BackupTarget(ctx, t.ID()); err == nil {
					bt.LastPath = t.Root
					return []userdata.BackupTarget{bt}, nil
				}
			}
			return []userdata.BackupTarget{{ID: t.ID(), Label: targetLabel(t.Root), LastPath: t.Root}}, nil
		}
		bt, err := attachTarget(ctx, b, t, "", out)
		if err != nil {
			return nil, err
		}
		return []userdata.BackupTarget{bt}, nil
	case id != "":
		if !backup.ValidID(id) {
			return nil, fmt.Errorf("invalid target id %q: want 16 lower-case hex digits ('picmover targets %s' lists them)", id, library.QuoteArg(b.dir))
		}
		if b.user == nil {
			return nil, fmt.Errorf("the library has no target %s", id)
		}
		bt, err := b.user.BackupTarget(ctx, id)
		if errors.Is(err, userdata.ErrNotFound) {
			return nil, fmt.Errorf("the library has no target %s; 'picmover targets %s' lists them", id, library.QuoteArg(b.dir))
		}
		if err != nil {
			return nil, err
		}
		return []userdata.BackupTarget{bt}, nil
	}
	var ts []userdata.BackupTarget
	if b.user != nil {
		var err error
		if ts, err = b.user.BackupTargets(ctx); err != nil {
			return nil, err
		}
	}
	switch {
	case len(ts) == 0:
		return nil, fmt.Errorf("the library has no backup targets; add one with:\n  picmover targets add %s <folder>", library.QuoteArg(b.dir))
	case all || len(ts) == 1:
		return ts, nil
	}
	return nil, fmt.Errorf("the library has %d backup targets; choose one with --target <id> (see 'picmover targets %s') or use --all-targets",
		len(ts), library.QuoteArg(b.dir))
}

// reach opens a remembered target at its last path and resolves the set,
// which must belong to the library (also a remembered set name) and have
// been last written by this copy of it unless takeOver. Nothing is
// written: rememberSetName stores the name once a backup claimed it.
func reach(ctx context.Context, b *backupLib, bt userdata.BackupTarget, command string, takeOver bool) (*resolvedTarget, error) {
	t, err := backup.OpenTarget(bt.LastPath)
	if err != nil || t.ID() != bt.ID {
		return nil, targetNotFound(bt, b.dir, command)
	}
	if err := backup.CheckSeparate(t.Root, b.lib.Root()); err != nil {
		return nil, err
	}
	name, err := backup.ResolveSetName(ctx, t, bt.SetName, b.libID, b.dir)
	if err != nil {
		return nil, setError(err, b, t, bt)
	}
	if err := backup.CheckSetOwner(ctx, t, name, b.id); err != nil && !(takeOver && errors.Is(err, backup.ErrForeignInstance)) {
		return nil, setError(err, b, t, bt)
	}
	return &resolvedTarget{bt: bt, t: t, setName: name}, nil
}

// rememberSetName stores the set name once a backup claimed the set.
func (rt *resolvedTarget) rememberSetName(ctx context.Context, b *backupLib) error {
	if rt.setName == rt.bt.SetName {
		return nil
	}
	name := rt.setName
	if _, err := b.user.UpdateBackupTarget(ctx, rt.bt.ID, userdata.TargetUpdate{SetName: &name}); err != nil {
		return err
	}
	rt.bt.SetName = name
	return nil
}

// parseBudget parses a scrub budget: a Go duration ("30m", "2h") or days
// ("1d"); "all" or "" is 0 (the whole set).
func parseBudget(s string) (time.Duration, error) {
	if s == "" || s == "all" {
		return 0, nil
	}
	return parseDays(s)
}

// parseDays parses a duration that may also be given in days ("30d").
func parseDays(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q: want e.g. 30m, 2h or 30d", s)
	}
	return d, nil
}

// progressLine formats a backup.Progress for the status line.
func progressLine(p backup.Progress) string {
	phase := strings.ToUpper(p.Phase[:1]) + p.Phase[1:]
	line := fmt.Sprintf("%s %d/%d files | %s of %s", phase, p.Files, p.TotalFiles, humanSize(p.Bytes), humanSize(p.TotalBytes))
	if p.Rate > 0 {
		line += fmt.Sprintf(" | %s/s", humanSize(int64(p.Rate)))
	}
	if p.ETA >= 0 {
		line += " | ETA " + (time.Duration(p.ETA) * time.Second).String()
	}
	if p.Failed > 0 {
		line += fmt.Sprintf(" | failed %d", p.Failed)
	}
	if p.Current != "" {
		line += " | " + p.Current
	}
	return line
}

// progressFunc returns a status-line progress callback, or nil when quiet
// or out is not a terminal; done ends the line.
func progressFunc(out io.Writer, quiet bool) (fn func(backup.Progress), done func()) {
	if quiet {
		return nil, func() {}
	}
	st := newStatusLine(out)
	if st == nil {
		return nil, func() {}
	}
	return func(p backup.Progress) { st.show(progressLine(p)) }, st.done
}

// logger returns a logger on the library's log <prefix>_<time>.log, its
// path and a close function.
func libraryLog(lib *library.Library, prefix string) (*slog.Logger, string, func(), error) {
	if err := os.MkdirAll(lib.LogDir(), 0o755); err != nil {
		return nil, "", nil, err
	}
	f, p, err := lib.OpenLog(prefix)
	if err != nil {
		return nil, "", nil, err
	}
	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})), p, func() { f.Close() }, nil
}

// printNonEmpty prints a titled list unless it is empty.
func printNonEmpty(w io.Writer, title string, list []string) {
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(w, "%s (%d):\n", title, len(list))
	for i, s := range list {
		if i == verifyListMax {
			fmt.Fprintf(w, "  ... and %d more (see the log)\n", len(list)-i)
			break
		}
		fmt.Fprintf(w, "  %s\n", s)
	}
}

func newBackupCmd() *cobra.Command {
	var (
		targetID, scrubBudget string
		all, dryRun, partial  bool
		doPrune, yes, quiet   bool
		takeOver, force       bool
		workers               int
	)
	c := &cobra.Command{
		Use:   "backup <library> [<target-folder>]",
		Short: "Back the library up to a backup target",
		Long: `Copy the library to a backup target: a folder on another drive or a network
share holding a picmover-backup.json marker (create one with 'picmover
targets add'). The library's copy is the set <target>/<library-name>/, a
plain mirror of the library tree plus user.db and media.db snapshots.

Files are keyed by content: a photo moved in the library is renamed on the
target, a changed file keeps its earlier content under
<set>/.picmover/previous/, and only new content is copied, newest first,
each copy verified by reading it back. Nothing on the target is removed
(use --prune) and nothing in the library is changed. An interrupted run is
continued by the next one.

The target is the folder given, --target <id>, every target with
--all-targets, or the library's only one. --dry-run prints the plan.
Logs go to <library>/.picmover/logs/ and <set>/.picmover/logs/.

A set last written by another copy of this library (the library folder
was copied, or moved, or is used from another host) is refused with the
folder and host that wrote it; --take-over makes this library its owner.

Exit status: 0 everything verified; 1 some files failed (listed),
--partial left files, a library folder could not be read, or catalog
files are missing in the library; 2 the target or the library could not
be used; 130 cancelled.`,
		Args: rangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if workers < 0 || workers > backup.MaxWorkers {
				return unusable(fmt.Errorf("--workers must be 0-%d", backup.MaxWorkers))
			}
			if force && !doPrune {
				return unusable(errors.New("--force needs --prune"))
			}
			if yes && !doPrune {
				return unusable(errors.New("--yes needs --prune"))
			}
			if len(args) == 2 && (targetID != "" || all) {
				return unusable(errors.New("give a target folder, --target or --all-targets, not several"))
			}
			if targetID != "" && all {
				return unusable(errors.New("--target and --all-targets exclude each other"))
			}
			var budget time.Duration
			if scrubBudget != "" {
				var err error
				if budget, err = parseBudget(scrubBudget); err != nil {
					return unusable(err)
				}
			}
			ctx, stop := interruptContext(cmd.Context())
			defer stop()
			out := cmd.OutOrStdout()
			lockName := "backup"
			if dryRun {
				lockName = ""
			}
			var b *backupLib
			var err error
			if dryRun {
				b, err = openBackupLibReadOnly(ctx, args[0], out, cmd.ErrOrStderr())
			} else {
				b, err = openBackupLib(ctx, args[0], lockName, out, cmd.ErrOrStderr())
			}
			if err != nil {
				return unusable(err)
			}
			defer b.close()
			folder := ""
			if len(args) == 2 {
				folder = args[1]
			}
			targets, err := resolveTargets(ctx, b, folder, targetID, all, "backup", out)
			if err != nil {
				return unusable(err)
			}
			worst := 0
			for _, bt := range targets {
				code, err := backupOne(ctx, cmd, b, bt, backupFlags{dryRun: dryRun, partial: partial, workers: workers,
					prune: doPrune, yes: yes, scrub: scrubBudget != "", budget: budget, quiet: quiet, takeOver: takeOver, force: force})
				if err != nil {
					var ee *exitError
					if errors.As(unusable(err), &ee) {
						if ee.code == 130 {
							return ee
						}
						code = max(code, ee.code)
						fmt.Fprintf(cmd.ErrOrStderr(), "Error: %s\n", ee.msg)
					}
				}
				worst = max(worst, code)
			}
			if worst != 0 {
				msg := ""
				if worst == exitFiles {
					msg = "some files were not backed up or verified; see the list above and the log"
				} else if len(targets) > 1 {
					msg = "a target could not be used"
				}
				return &exitError{code: worst, msg: msg}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&targetID, "target", "", "back up to the remembered target with this id")
	f.BoolVar(&all, "all-targets", false, "back up to every remembered target")
	f.BoolVar(&dryRun, "dry-run", false, "print the plan; copy nothing")
	f.BoolVar(&partial, "partial", false, "when the target is too small, copy the newest files until it is full")
	f.IntVar(&workers, "workers", 0, fmt.Sprintf("parallel copies (0 = %d)", backup.DefaultWorkers))
	f.BoolVar(&doPrune, "prune", false, "afterwards, list what prune would remove (and remove it with --yes)")
	f.BoolVar(&yes, "yes", false, "with --prune: remove the listed files")
	f.StringVar(&scrubBudget, "scrub", "", "afterwards, scrub the set for this long (e.g. 30m, or all)")
	f.BoolVar(&quiet, "quiet", false, "do not show the live status line")
	f.BoolVar(&takeOver, "take-over", false, "back up into a set last written by another copy of this library (a copied or moved library folder), making this library its owner")
	f.BoolVar(&force, "force", false, "with --prune: prune even when the library could not be read completely or catalog files are missing")
	return backupCommand(c)
}

type backupFlags struct {
	dryRun, partial, prune, yes, scrub, quiet bool
	takeOver, force                           bool
	workers                                   int
	budget                                    time.Duration
}

// backupOne backs the library up to one target and returns the exit code
// of what it did (0 or 1); an error means the target could not be used.
func backupOne(ctx context.Context, cmd *cobra.Command, b *backupLib, bt userdata.BackupTarget, fl backupFlags) (int, error) {
	out := cmd.OutOrStdout()
	rt, err := reach(ctx, b, bt, "backup", fl.takeOver)
	if err != nil {
		return exitUnusable, err
	}
	fmt.Fprintf(out, "Target %s (%s) at %s, set %s\n", rt.bt.ID, rt.bt.Label, rt.t.Root, rt.setName)
	if err := backup.CheckSetFolder(rt.t, rt.setName); err != nil {
		return exitUnusable, err
	}
	if fl.dryRun {
		// reach checked the owner; a dry run writes nothing.
		var set *backup.Set
		if backup.SetExists(rt.t, rt.setName) {
			if set, err = backup.OpenSetReadOnly(ctx, rt.t, rt.setName); err != nil {
				return exitUnusable, err
			}
			defer set.Close()
		}
		plan, err := backup.PlanFor(ctx, b.source(), set)
		if err != nil {
			return exitUnusable, err
		}
		printPlan(out, plan)
		if set != nil {
			pp, err := backup.PlanPrune(set, plan, backup.DefaultKeep, time.Now())
			if err == nil && pp.Count > 0 {
				fmt.Fprintf(out, "Prune would remove %d files (%s).\n", pp.Count, humanSize(pp.Bytes))
			}
		}
		return 0, nil
	}
	set, err := backup.OpenSet(ctx, rt.t, rt.setName, true)
	if err != nil {
		return exitUnusable, err
	}
	defer set.Close()
	logger, logPath, closeLog, err := libraryLog(b.lib, "backup")
	if err != nil {
		return exitUnusable, err
	}
	defer closeLog()
	progress, done := progressFunc(out, fl.quiet)
	st, err := backup.Run(ctx, b.source(), set, backup.Options{Owner: b.id, TakeOver: fl.takeOver, Library: b.lib.Root(),
		Workers: fl.workers, Partial: fl.partial, OnProgress: progress, Logger: logger})
	done()
	// The outcome is kept in user.db too: the target's backup.db may not
	// have recorded it.
	// A run refused because another one holds the set did nothing.
	if backup.RecordsAttempt(err) {
		if aerr := b.user.RecordBackupAttempt(context.WithoutCancel(ctx), rt.bt.ID, backup.Attempt(ctx, set, &st, err)); aerr != nil {
			logger.Error("record the backup attempt", "err", aerr)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(out, "Backup cancelled: %d files copied; the next run continues.\n", st.Copied)
		}
		if msg := backup.KnownRunError(err); msg != "" && backup.IsBusy(err) {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", msg)
		}
		return exitUnusable, setError(err, b, rt.t, rt.bt)
	}
	if err := rt.rememberSetName(ctx, b); err != nil {
		logger.Error("remember the set name", "err", err)
	}
	if err := b.user.MarkBackup(ctx, rt.bt.ID, time.Now()); err != nil {
		logger.Error("record the backup time", "err", err)
	}
	printBackupSummary(out, &st, logPath)
	code := st.ExitCode()
	if fl.prune {
		c, err := pruneSet(ctx, cmd, b, set, backup.DefaultKeep, pruneFlags{yes: fl.yes, force: fl.force}, logger)
		if err != nil {
			return max(code, exitUnusable), err
		}
		code = max(code, c)
	}
	if fl.scrub {
		src := b.source()
		sst, err := backup.Scrub(ctx, set, backup.ScrubOptions{Budget: fl.budget, Repair: true, Source: &src,
			Owner: b.id, Library: b.lib.Root(), Logger: logger})
		if err != nil {
			return max(code, exitUnusable), err
		}
		if err := b.user.MarkScrub(ctx, rt.bt.ID, time.Now()); err != nil {
			logger.Error("record the scrub time", "err", err)
		}
		printScrubSummary(out, &sst)
		code = max(code, sst.ExitCode())
	}
	return code, nil
}

func printPlan(w io.Writer, p *backup.Plan) {
	fmt.Fprintf(w, "Plan:\n")
	fmt.Fprintf(w, "Current:  %7d files  %s\n", p.Current.Files, humanSize(p.Current.Bytes))
	fmt.Fprintf(w, "New:      %7d files  %s\n", p.New.Files, humanSize(p.New.Bytes))
	fmt.Fprintf(w, "Changed:  %7d files  %s\n", p.Changed.Files, humanSize(p.Changed.Bytes))
	fmt.Fprintf(w, "Moved:    %7d files  %s\n", p.Moved.Files, humanSize(p.Moved.Bytes))
	fmt.Fprintf(w, "Extra:    %7d files  %s (on the target only; prune removes them)\n", p.Extra.Files, humanSize(p.Extra.Bytes))
	fmt.Fprintf(w, "To copy:  %s\n", humanSize(p.CopyBytes()))
	var news []string
	for _, it := range p.Items {
		switch it.Class {
		case backup.ClassMoved:
			news = append(news, "moved   "+it.From+" -> "+it.Rel)
		default:
			news = append(news, fmt.Sprintf("%-7s %s", it.Class, it.Rel))
		}
	}
	printNonEmpty(w, "Actions", news)
	printNonEmpty(w, "Symbolic links (skipped)", p.Symlinks)
	printNonEmpty(w, "In the catalog but missing in the library", p.Missing)
	printNonEmpty(w, "Could not be read (their files are not backed up)", failureLines(p.Errors))
	var coll []string
	for _, c := range p.Collisions {
		coll = append(coll, c.Path+" (same name as "+c.Other+" but for case or Unicode form; not backed up)")
	}
	printNonEmpty(w, "Name collisions", coll)
}

func failureLines(list []backup.Failure) []string {
	out := make([]string, 0, len(list))
	for _, f := range list {
		out = append(out, f.Path+": "+f.Error)
	}
	return out
}

func printBackupSummary(w io.Writer, st *backup.Stats, logPath string) {
	fmt.Fprintf(w, "Backup summary:\n")
	fmt.Fprintf(w, "Copied:            %d (%s)\n", st.Copied, humanSize(st.BytesCopied))
	fmt.Fprintf(w, "Renamed (moved):   %d\n", st.Renamed)
	fmt.Fprintf(w, "Already there:     %d\n", st.Current+st.Adopted+st.Refreshed)
	fmt.Fprintf(w, "Failed:            %d\n", st.FailedCount)
	if st.Partial {
		fmt.Fprintf(w, "Left (no space):   %d (%s)\n", st.LeftFiles, humanSize(st.LeftBytes))
	}
	fmt.Fprintf(w, "Extra on target:   %d\n", st.Extra)
	snaps := "user.db and media.db"
	if st.SnapshotError != "" {
		snaps = "failed: " + st.SnapshotError
	}
	fmt.Fprintf(w, "Snapshots:         %s\n", snaps)
	var failed []string
	for _, f := range st.Failures {
		failed = append(failed, f.Path+": "+f.Error)
	}
	printNonEmpty(w, "Failed files", failed)
	printNonEmpty(w, "Symbolic links (skipped)", st.Symlinks)
	printNonEmpty(w, "In the catalog but missing in the library", st.Missing)
	printNonEmpty(w, "Could not be read in the library (their files are not backed up)", failureLines(st.ScanErrors))
	fmt.Fprintf(w, "Log: %s\n", logPath)
}
