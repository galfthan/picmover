package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"picmover/internal/catalog"
	"picmover/internal/importer"
	"picmover/internal/importrec"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/userdata"
)

func newImportCmd() *cobra.Command {
	var (
		p        importParams
		zoneName string
	)
	c := &cobra.Command{
		Use:   "import <source> <library>",
		Short: "Import photos and videos into a library",
		Long: `Import media from a source directory (zip archives included) into the
library, laid out as <type>/YYYY/MM/<name> by capture date. Each file is
copied through a temp file and recorded in the catalog with its hash.
Files whose content is already in the catalog are skipped as duplicates.
With --move the sources are deleted once they are safely recorded.

After the copy stage a thumbnail (256 px) and a preview (1600 px) of every
imported file are generated into <library>/.picmover (skip with
--no-thumbs; 'picmover thumbs' generates them later).

Every import is recorded in the library's import history in user.db
(created when missing); the summary prints the record's id, and
'picmover imports <library>' lists them.

A detailed log is written to <library>/.picmover/logs/.`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if p.minDimension < 0 || p.workers < 0 || p.sourceReaders < 0 {
				return fmt.Errorf("--min-dimension, --workers and --source-readers must be >= 0")
			}
			zone, err := resolveZone(zoneName)
			if err != nil {
				return err
			}
			p.zone = zone
			// Check the source before anything is created in the library.
			if _, err := os.Stat(args[0]); err != nil {
				return fmt.Errorf("source: %w", err)
			}
			libRoot, err := filepath.Abs(args[1])
			if err != nil {
				return err
			}
			p.source, p.libRoot = args[0], libRoot
			if _, err := os.Stat(filepath.Join(libRoot, library.DBName)); errors.Is(err, fs.ErrNotExist) {
				p.isNew = true
			} else if err != nil {
				return err
			}
			if p.isNew {
				fmt.Fprintf(cmd.OutOrStdout(), "Creating new library at %s\n", libRoot)
			}
			return runImport(cmd, nil, p)
		},
	}
	f := c.Flags()
	f.BoolVar(&p.move, "move", false, "move files instead of copying (sources are deleted after they are recorded)")
	f.IntVar(&p.minDimension, "min-dimension", 0, "skip images whose width and height are both below N pixels (0 = no limit)")
	f.IntVar(&p.workers, "workers", 0, "number of parallel workers (0 = auto)")
	f.IntVar(&p.sourceReaders, "source-readers", 0, "workers reading the source at once (0 = auto: 1 on FUSE mounts such as phones, else --workers)")
	f.StringVar(&zoneName, "timezone", "", "IANA zone for times stored as instants (video UTC, file mtime); default the system zone")
	f.BoolVar(&p.quiet, "quiet", false, "do not show the live status line")
	f.BoolVar(&p.noThumbs, "no-thumbs", false, "do not generate thumbnails and previews (run 'picmover thumbs' later)")
	return c
}

// importParams configure runImport.
type importParams struct {
	source, libRoot string
	move            bool
	minDimension    int
	workers         int
	sourceReaders   int
	zone            *time.Location
	quiet           bool
	noThumbs        bool
	isNew           bool
	recordInPlace   bool                 // importer.Options.RecordInPlace (rebuild)
	trashTimes      map[uint64]time.Time // importer.Options.TrashTimes (rebuild)
	locked          bool                 // the caller holds the library lock (rebuild)
	// backup is the catalog backup of a rebuild ("" when the folder had
	// no catalog). When the record stage does not finish (an error, a
	// cancellation, a catalog that cannot be created), the incomplete new
	// catalog is removed and the backup renamed back.
	backup    string
	logPrefix string // log file name prefix; "" = "import"
	what      string // operation name in messages; "" = "Import"
}

// interruptContext returns a context cancelled by the first SIGINT or
// SIGTERM. After the first signal the default handling is restored, so a
// second Ctrl+C terminates the process at once.
func interruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

// runImport runs an import (with its thumbnail stage) and prints the
// summary. Exit status: 0; 2 when files could not be imported, thumbnails
// failed or the thumbnail stage could not run; 130 when cancelled; 1
// otherwise (never after a successful copy stage). ctx is the interrupt
// context; nil installs one.
func runImport(cmd *cobra.Command, ctx context.Context, p importParams) error {
	if p.logPrefix == "" {
		p.logPrefix = "import"
	}
	if p.what == "" {
		p.what = "Import"
	}
	out := cmd.OutOrStdout()
	if ctx == nil {
		var stop context.CancelFunc
		ctx, stop = interruptContext(cmd.Context())
		defer stop()
	}

	lib, err := library.Open(p.libRoot)
	if err != nil {
		return err
	}
	if !p.locked {
		unlock, err := lockLibrary(ctx, lib, strings.ToLower(p.what), cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		defer unlock()
	}
	logFile, logPath, err := lib.OpenLog(p.logPrefix)
	if err != nil {
		return err
	}
	defer logFile.Close()
	fileHandler := slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(fileHandler)
	sum := importSummary{libRoot: lib.Root(), isNew: p.isNew, logPath: logPath, thumbs: !p.noThumbs}

	var (
		stats    importer.Stats
		runErr   error
		opened   bool
		recorded bool
	)
	db, err := catalog.Open(ctx, lib.DBPath(), slog.New(slog.NewMultiHandler(fileHandler, stderrHandler())))
	if err != nil {
		runErr = catalogError(lib.Root(), err)
	} else {
		opened = true
		dbOpen := true
		closeDB := func() {
			if dbOpen {
				dbOpen = false
				db.Close()
			}
		}
		defer closeDB()
		opts := importer.Options{
			Source:        p.source,
			Move:          p.move,
			MinDimension:  p.minDimension,
			Workers:       p.workers,
			SourceReaders: p.sourceReaders,
			Extractor:     metadata.Default{DefaultZone: p.zone},
			Logger:        logger,
			NoThumbs:      p.noThumbs,
			RecordInPlace: p.recordInPlace,
			TrashTimes:    p.trashTimes,
		}
		var status *statusLine // nil: no live status line
		if !p.quiet {
			status = newStatusLine(out)
		}
		// The import history (user.db, created when missing). A user.db
		// that cannot be opened only costs the record.
		user, userErr := userdata.Open(ctx, filepath.Join(lib.Root(), userdata.FileName))
		if userErr != nil {
			logger.Error("open user.db", "err", userErr)
		} else {
			defer user.Close()
			if from, bak := user.Migrated(); from != 0 {
				fmt.Fprintln(out, migrationNotice(from, bak))
			}
			if !p.recordInPlace {
				// A rebuild backfills from the old catalog before its
				// backup (cmd/rebuild.go); this catalog is the live one.
				backfill(ctx, db, user, logger)
				// Holding the library lock, no other run can own a
				// running record: each is a crashed run's.
				closeCrashed(ctx, user, db.SessionHashes, logger)
			}
		}
		mode := userdata.ImportModeCopy
		switch {
		case p.recordInPlace:
			mode = userdata.ImportModeRebuild
		case p.move:
			mode = userdata.ImportModeMove
		}
		rec := importrec.Begin(ctx, user, userErr, p.source, mode, logger)
		opts.OnResult = rec.OnResult
		var im *importer.Importer
		opts.OnProgress = func(s importer.Stats) {
			if status != nil {
				status.show(formatStatus(s))
			}
			// The record links to its catalog session while it runs, so
			// a crash leaves it findable (FinishImport writes it too).
			rec.SetSession(ctx, im.SessionID())
		}
		if !p.recordInPlace {
			// Content found in the trash is restored; its user.db trash
			// row is removed when there is a user.db.
			opts.OnRestored = func(ctx context.Context, hash uint64) error {
				if user != nil {
					return user.Untrash(ctx, []uint64{hash})
				}
				return untrashUserDB(ctx, lib.Root(), hash)
			}
		}
		im = importer.New(lib, db, opts)
		stats, runErr = im.Run(ctx)
		recorded = im.RecordStageDone()
		if status != nil {
			status.done()
		}
		outcome := importrec.Outcome(runErr)
		if p.recordInPlace && !recorded && outcome == userdata.ImportDone {
			outcome = userdata.ImportFailed // rolled back below
		}
		rec.Finish(ctx, outcome, stats, im.SessionID(), db)
		sum.importID, sum.recordErr = rec.ID(), rec.Err()
		if p.recordInPlace && !recorded {
			closeDB()
		}
	}
	if p.recordInPlace && !recorded {
		// Never leave a partial catalog live, or none at all.
		if err := library.RestoreCatalogBackup(lib.DBPath(), p.backup); err != nil {
			logger.Error("restore the previous catalog", "err", err)
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: the rebuild did not finish and the previous catalog could not be restored: %v\n", err)
		} else if p.backup != "" {
			logger.Info("rebuild rolled back", "backup", p.backup)
			fmt.Fprintf(out, "The rebuild did not finish; the previous catalog was restored from %s.\n", filepath.Base(p.backup))
		} else {
			logger.Info("rebuild rolled back: incomplete catalog removed")
			fmt.Fprintln(out, "The rebuild did not finish; the incomplete catalog was removed.")
		}
	}

	var stageErr *importer.ThumbStageError
	switch {
	case errors.Is(runErr, context.Canceled):
		logger.Info(strings.ToLower(p.what) + " cancelled")
		fmt.Fprintf(out, "%s cancelled.\n", p.what)
		sum.print(out, stats)
		return &exitError{code: 130}
	case errors.As(runErr, &stageErr):
		// The copy stage succeeded: every file is recorded. Only the
		// thumbnails are missing; their rows stay pending.
		logger.Warn("thumbnail stage failed", "err", stageErr.Err)
		sum.print(out, stats)
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: thumbnails were not generated: %v\n", stageErr.Err)
		msg := fmt.Sprintf("%s finished, but the thumbnail stage failed; fix the cache and run 'picmover thumbs %s'",
			strings.ToLower(p.what), quoteArg(lib.Root()))
		if stats.Errors > 0 {
			msg = fmt.Sprintf("%d files could not be imported (see %s); %s", stats.Errors, logPath, msg)
		}
		return &exitError{code: 2, msg: msg}
	case runErr != nil:
		logger.Error(strings.ToLower(p.what)+" failed", "err", runErr)
		if opened {
			sum.print(out, stats)
		}
		return runErr
	}
	sum.print(out, stats)
	switch {
	case stats.Errors > 0:
		return &exitError{code: 2, msg: fmt.Sprintf("%d files could not be imported; see %s", stats.Errors, logPath)}
	case stats.ThumbsFailed > 0:
		return &exitError{code: 2, msg: fmt.Sprintf("thumbnails failed for %d files; see %s", stats.ThumbsFailed, logPath)}
	}
	return nil
}

// defaultTermWidth is used when the terminal width cannot be determined.
const defaultTermWidth = 80

// terminal returns w as a file when it is a character device (an
// interactive terminal).
func terminal(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return nil, false
	}
	fi, err := f.Stat()
	return f, err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// statusLine redraws a single status line on a terminal.
type statusLine struct {
	f  *os.File
	vt bool // ANSI escapes work; otherwise clear the line with spaces
}

// newStatusLine returns a status line on w, or nil when w is not a terminal.
func newStatusLine(w io.Writer) *statusLine {
	f, ok := terminal(w)
	if !ok {
		return nil
	}
	return &statusLine{f: f, vt: enableVT(f)}
}

func (s *statusLine) show(line string) {
	width := termWidth(s.f) - 1
	io.WriteString(s.f, redrawLine(line, width, s.vt))
}

func (s *statusLine) done() { io.WriteString(s.f, "\n") }

// redrawLine returns the text that replaces the current terminal line with
// line, truncated to width columns.
func redrawLine(line string, width int, vt bool) string {
	width = max(width, 1)
	if r := []rune(line); len(r) > width {
		line = string(r[:width])
	}
	if vt {
		return "\r\033[2K" + line
	}
	return "\r" + strings.Repeat(" ", width) + "\r" + line
}

// formatStatus is the live status line of an import; the transfer rate
// is shown only while bytes are moving.
func formatStatus(s importer.Stats) string {
	line := fmt.Sprintf("Scanned %d | Imported %d | Existing %d | Duplicates %d | Small %d | Non-media %d | Errors %d | Copied %s",
		s.Scanned, s.Imported, s.Existing, s.Duplicates, s.SkippedSmall, s.NonMedia, s.Errors, humanSize(s.BytesCopied))
	if s.Rate > 0 {
		line += " (" + humanSize(s.Rate) + "/s)"
	}
	if n := thumbsProcessed(s); n > 0 {
		line += fmt.Sprintf(" | Thumbs %d", n)
	}
	return line
}

// thumbsProcessed is the number of rows the thumbnail stage has finished.
func thumbsProcessed(s importer.Stats) int64 {
	return s.ThumbsDone + s.ThumbsUnsupported + s.ThumbsFailed
}

// formatThumbStatus is the status line of the thumbs command.
func formatThumbStatus(s importer.Stats) string {
	line := fmt.Sprintf("Thumbs %d | Done %d | Unsupported %d | Failed %d",
		thumbsProcessed(s), s.ThumbsDone, s.ThumbsUnsupported, s.ThumbsFailed)
	if s.LargeDone > 0 {
		line += fmt.Sprintf(" | Large filled %d", s.LargeDone)
	}
	return line
}

// printThumbSummary prints the thumbnail counters of a run.
func printThumbSummary(w io.Writer, s importer.Stats) {
	fmt.Fprintf(w, "Thumbnails:\n")
	fmt.Fprintf(w, "Done:              %d\n", s.ThumbsDone)
	fmt.Fprintf(w, "Unsupported:       %d\n", s.ThumbsUnsupported)
	fmt.Fprintf(w, "Failed:            %d\n", s.ThumbsFailed)
	fmt.Fprintf(w, "Large filled:      %d\n", s.LargeDone)
	fmt.Fprintf(w, "Written as:        WebP (thumbnail 256 px, large 640 px, preview 1600 px)\n")
}

// importSummary prints the end-of-import report.
type importSummary struct {
	libRoot, logPath string
	isNew            bool
	thumbs           bool // print the thumbnail counters
	importID         int64
	recordErr        error
}

func (is importSummary) print(w io.Writer, s importer.Stats) {
	fmt.Fprintf(w, "Import summary:\n")
	fmt.Fprintf(w, "Scanned:           %d\n", s.Scanned)
	fmt.Fprintf(w, "Imported:          %d\n", s.Imported)
	fmt.Fprintf(w, "Already in place:  %d\n", s.Existing)
	fmt.Fprintf(w, "Duplicates:        %d\n", s.Duplicates)
	fmt.Fprintf(w, "Skipped (small):   %d\n", s.SkippedSmall)
	fmt.Fprintf(w, "Non-media:         %d\n", s.NonMedia)
	fmt.Fprintf(w, "Errors:            %d\n", s.Errors)
	fmt.Fprintf(w, "Copied:            %s\n", humanSize(s.BytesCopied))
	if is.thumbs {
		printThumbSummary(w, s)
	}
	if is.isNew {
		fmt.Fprintf(w, "Library: %s (new)\n", is.libRoot)
	} else {
		fmt.Fprintf(w, "Library: %s\n", is.libRoot)
	}
	switch {
	case is.importID != 0 && is.recordErr == nil:
		fmt.Fprintf(w, "Import record: #%d\n", is.importID)
	case is.importID != 0:
		fmt.Fprintf(w, "Import record: #%d (incomplete: %v; see the log)\n", is.importID, is.recordErr)
	case is.recordErr != nil:
		fmt.Fprintf(w, "Import record: could not be written (%v; see the log)\n", is.recordErr)
	}
	fmt.Fprintf(w, "Log: %s\n", is.logPath)
}

// backfill copies the catalog's import sessions into the import history
// of user.db the first time (importrec.Backfill); a failure is logged.
func backfill(ctx context.Context, db *catalog.DB, user *userdata.DB, logger *slog.Logger) {
	n, err := importrec.Backfill(ctx, db, user)
	switch {
	case err != nil:
		logger.Error("import history backfill", "err", err)
	case n > 0:
		logger.Info("import history backfilled from the catalog", "records", n)
	}
}

// closeCrashed closes the import records left running by a process that
// died (importrec.CloseCrashed: outcome failed, items from the catalog
// rows of their session, rows nil: none); the caller holds the library
// lock, so no record is a live run's. A failure is logged.
func closeCrashed(ctx context.Context, user *userdata.DB, rows importrec.SessionRows, logger *slog.Logger) {
	n, err := importrec.CloseCrashed(ctx, user, rows, nil, logger)
	switch {
	case err != nil:
		logger.Error("close crashed import records", "err", err)
	case n > 0:
		logger.Info("crashed import records closed", "records", n)
	}
}

// untrashUserDB removes hash's trash row from the library's user.db, when
// the library has one (it is never created here).
func untrashUserDB(ctx context.Context, root string, hash uint64) error {
	p := filepath.Join(root, userdata.FileName)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	u, err := userdata.Open(ctx, p)
	if err != nil {
		return err
	}
	defer u.Close()
	return u.Untrash(ctx, []uint64{hash})
}
