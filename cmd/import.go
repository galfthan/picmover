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
		move         bool
		minDimension int
		workers      int
		zoneName     string
		quiet        bool
	)
	c := &cobra.Command{
		Use:   "import <source> <library>",
		Short: "Import photos and videos into a library",
		Long: `Import media from a source directory (zip archives included) into the
library, laid out as <type>/YYYY/MM/<name> by capture date. Each file is
copied through a temp file, verified by hash and recorded in the catalog.
Files whose content is already in the catalog are skipped as duplicates.
With --move the sources are deleted once they are safely recorded.

A detailed log is written to <library>/.picmover/logs/.`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if minDimension < 0 || workers < 0 {
				return fmt.Errorf("--min-dimension and --workers must be >= 0")
			}
			zone, err := resolveZone(zoneName)
			if err != nil {
				return err
			}
			// Check the source before anything is created in the library.
			if _, err := os.Stat(args[0]); err != nil {
				return fmt.Errorf("source: %w", err)
			}
			libRoot, err := filepath.Abs(args[1])
			if err != nil {
				return err
			}
			isNew := false
			if _, err := os.Stat(filepath.Join(libRoot, library.DBName)); errors.Is(err, fs.ErrNotExist) {
				isNew = true
			} else if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if isNew {
				fmt.Fprintf(out, "Creating new library at %s\n", libRoot)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// After the first signal restore the default handling, so a
			// second Ctrl+C terminates the process at once.
			go func() {
				<-ctx.Done()
				stop()
			}()

			lib, err := library.Open(libRoot)
			if err != nil {
				return err
			}
			logPath := filepath.Join(lib.LogDir(), "import_"+time.Now().Format("2006-01-02_15-04-05")+".log")
			logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return fmt.Errorf("create log: %w", err)
			}
			defer logFile.Close()
			fileHandler := slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})
			logger := slog.New(fileHandler)
			sum := importSummary{libRoot: lib.Root(), isNew: isNew, logPath: logPath}

			var (
				stats  importer.Stats
				runErr error
				opened bool
			)
			db, err := catalog.Open(ctx, lib.DBPath(), slog.New(slog.NewMultiHandler(fileHandler, stderrHandler())))
			if err != nil {
				runErr = err
			} else {
				opened = true
				defer db.Close()
				opts := importer.Options{
					Source:       args[0],
					Move:         move,
					MinDimension: minDimension,
					Workers:      workers,
					Extractor:    metadata.Default{DefaultZone: zone},
					Logger:       logger,
				}
				var status *statusLine // nil: no live status line
				if !quiet {
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

			switch {
			case errors.Is(runErr, context.Canceled):
				logger.Info("import cancelled")
				fmt.Fprintln(out, "Import cancelled.")
				sum.print(out, stats)
				return &exitError{code: 130}
			case runErr != nil:
				logger.Error("import failed", "err", runErr)
				if opened {
					sum.print(out, stats)
				}
				return runErr
			}
			sum.print(out, stats)
			if stats.Errors > 0 {
				return &exitError{code: 2, msg: fmt.Sprintf("%d files could not be imported; see %s", stats.Errors, logPath)}
			}
			return nil
		},
	}
	f := c.Flags()
	f.BoolVar(&move, "move", false, "move files instead of copying (sources are deleted after they are recorded)")
	f.IntVar(&minDimension, "min-dimension", 0, "skip images whose width and height are both below N pixels (0 = no limit)")
	f.IntVar(&workers, "workers", 0, "number of parallel workers (0 = auto)")
	f.StringVar(&zoneName, "timezone", "", "IANA zone for times stored as instants (video UTC, file mtime); default the system zone")
	f.BoolVar(&quiet, "quiet", false, "do not show the live status line")
	return c
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
	return fmt.Sprintf("Scanned %d | Imported %d | Existing %d | Duplicates %d | Small %d | Non-media %d | Errors %d | Copied %s",
		s.Scanned, s.Imported, s.Existing, s.Duplicates, s.SkippedSmall, s.NonMedia, s.Errors, humanSize(s.BytesCopied))
}

// importSummary prints the end-of-import report.
type importSummary struct {
	libRoot, logPath string
	isNew            bool
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
	if is.isNew {
		fmt.Fprintf(w, "Library: %s (new)\n", is.libRoot)
	} else {
		fmt.Fprintf(w, "Library: %s\n", is.libRoot)
	}
	fmt.Fprintf(w, "Log: %s\n", is.logPath)
}
