package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// humanSize formats a byte count with binary units, e.g. "1.5 GiB".
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatTakenAt formats a catalog capture time as wall-clock
// "2006-01-02 15:04:05", followed by the offset when it is known.
// The zero time formats as "-".
func formatTakenAt(t time.Time, offsetKnown bool) string {
	if t.IsZero() {
		return "-"
	}
	if offsetKnown {
		return t.Format("2006-01-02 15:04:05 -07:00")
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatDate formats a wall-clock time as a date, or "-" for the zero time.
func formatDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02")
}

// stderrLogger logs catalog notices (creation) to stderr.
func stderrLogger() *slog.Logger { return slog.New(stderrHandler()) }

// stderrHandler is a text handler on stderr without timestamps.
func stderrHandler() slog.Handler {
	return slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
}

// openExisting attaches to the library at dir and opens its catalog without
// creating directories or a catalog: db, verify, update-metadata and thumbs
// never create a library, and a mistyped path does not leave an empty
// media.db behind. A catalog of another format is refused, unmodified, with
// an error wrapping catalog.ErrNeedsRebuild that names the rebuild command.
func openExisting(ctx context.Context, dir string) (*library.Library, *catalog.DB, error) {
	lib, err := library.Attach(dir)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(lib.DBPath()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("no %s in %s: not a picmover library", library.DBName, lib.Root())
		}
		return nil, nil, err
	}
	db, err := catalog.OpenExisting(ctx, lib.DBPath(), stderrLogger())
	if err != nil {
		return nil, nil, catalogError(lib.Root(), err)
	}
	return lib, db, nil
}

// rebuildError explains how to recover from a catalog of another format.
type rebuildError struct {
	root string
	err  error
}

func (e *rebuildError) Error() string {
	return fmt.Sprintf("%v\nRecreate the catalog from the files with:\n  picmover rebuild %s\n"+
		"(the old catalog is kept as %s.bak-<timestamp>; thumbnails are reused)", e.err, quoteArg(e.root), library.DBName)
}

func (e *rebuildError) Unwrap() error { return e.err }

// catalogError turns catalog.ErrNeedsRebuild into a message naming
// "picmover rebuild <library>"; other errors are returned unchanged.
func catalogError(root string, err error) error {
	if errors.Is(err, catalog.ErrNeedsRebuild) {
		return &rebuildError{root: root, err: err}
	}
	return err
}

// quoteArg quotes a path for a shell command line when it needs it.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$&|;<>()*?[]#~`") {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}
