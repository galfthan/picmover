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
	"picmover/internal/library"
	"picmover/internal/metadata"
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
copied through a temp file, verified by hash and recorded in the catalog.
Files whose content is already in the catalog are skipped as duplicates.
With --move the sources are deleted once they are safely recorded.

After the copy stage a thumbnail (256 px) and a preview (1600 px) of every
imported file are generated into <library>/.picmover (skip with
--no-thumbs; 'picmover thumbs' generates them later).

A detailed log is written to <library>/.picmover/logs/.`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if p.minDimension < 0 || p.workers < 0 {
				return fmt.Errorf("--min-dimension and --workers must be >= 0")
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
	zone            *time.Location
	quiet           bool
	noThumbs        bool
	isNew           bool
	recordInPlace   bool   // importer.Options.RecordInPlace (rebuild)
	locked          bool   // the caller holds the library lock (rebuild)
	logPrefix       string // log file name prefix; "" = "import"
	what            string // operation name in messages; "" = "Import"
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
		stats  importer.Stats
		runErr error
		opened bool
	)
	db, err := catalog.Open(ctx, lib.DBPath(), slog.New(slog.NewMultiHandler(fileHandler, stderrHandler())))
	if err != nil {
		runErr = catalogError(lib.Root(), err)
	} else {
		opened = true
		defer db.Close()
		opts := importer.Options{
			Source:        p.source,
			Move:          p.move,
			MinDimension:  p.minDimension,
			Workers:       p.workers,
			Extractor:     metadata.Default{DefaultZone: p.zone},
			Logger:        logger,
			NoThumbs:      p.noThumbs,
			RecordInPlace: p.recordInPlace,
		}
		var status *statusLine // nil: no live status line
		if !p.quiet {
			status = newStatusLine(out)
		}
		if status != nil {
			opts.OnProgress = func(s importer.Stats) { status.show(formatStatus(s)) }
		}
		stats, runErr = importer.New(lib, db, opts).Run(ctx)
		if status != nil {
			status.done()
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

func formatStatus(s importer.Stats) string {
	line := fmt.Sprintf("Scanned %d | Imported %d | Existing %d | Duplicates %d | Small %d | Non-media %d | Errors %d | Copied %s",
		s.Scanned, s.Imported, s.Existing, s.Duplicates, s.SkippedSmall, s.NonMedia, s.Errors, humanSize(s.BytesCopied))
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
	return fmt.Sprintf("Thumbs %d | Done %d | Unsupported %d | Failed %d",
		thumbsProcessed(s), s.ThumbsDone, s.ThumbsUnsupported, s.ThumbsFailed)
}

// printThumbSummary prints the thumbnail counters of a run.
func printThumbSummary(w io.Writer, s importer.Stats) {
	fmt.Fprintf(w, "Thumbnails:\n")
	fmt.Fprintf(w, "Done:              %d\n", s.ThumbsDone)
	fmt.Fprintf(w, "Unsupported:       %d\n", s.ThumbsUnsupported)
	fmt.Fprintf(w, "Failed:            %d\n", s.ThumbsFailed)
}

// importSummary prints the end-of-import report.
type importSummary struct {
	libRoot, logPath string
	isNew            bool
	thumbs           bool // print the thumbnail counters
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
	fmt.Fprintf(w, "Log: %s\n", is.logPath)
}
