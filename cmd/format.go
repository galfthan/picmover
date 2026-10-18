package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/server"
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

// formatCaptureRow formats a row's home time, which orders the timeline
// and the listings, followed by the local time where it was taken when
// that differs: "2024-07-14 15:30:00 (local 2024-07-14 14:30:00 +02:00)".
func formatCaptureRow(m catalog.Media) string {
	local := formatTakenAt(m.TakenAt, m.OffsetKnown)
	if m.TakenAtHome.IsZero() {
		return local
	}
	home := m.TakenAtHome.Format("2006-01-02 15:04:05")
	if m.TakenAt.Format("2006-01-02 15:04:05") == home {
		return local
	}
	return home + " (local " + local + ")"
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
	lib, db, _, err := openExistingLocked(ctx, dir, "", nil)
	return lib, db, err
}

// openExistingLocked is openExisting for a command that writes: unless
// lockName is "", it takes the library lock (see lockLibrary) before the
// command starts writing. unlock releases it; it is a no-op without a lock.
// A catalog of the previous format is upgraded in place once the lock is
// held.
func openExistingLocked(ctx context.Context, dir, lockName string, errOut io.Writer) (lib *library.Library, db *catalog.DB, unlock func(), err error) {
	return openExistingFor(ctx, dir, lockName, lockName != "", errOut)
}

// openExistingFor is openExistingLocked; upgrade (which needs a lockName)
// allows the in-place upgrade of a catalog of the previous format, which
// is otherwise refused with a message naming a command that upgrades it.
func openExistingFor(ctx context.Context, dir, lockName string, upgrade bool, errOut io.Writer) (lib *library.Library, db *catalog.DB, unlock func(), err error) {
	lib, err = library.Attach(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := os.Stat(lib.DBPath()); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil, fmt.Errorf("no %s in %s: not a picmover library", library.DBName, lib.Root())
		}
		return nil, nil, nil, err
	}
	upgrade = upgrade && lockName != ""
	// The catalog is opened (and a catalog of another format refused,
	// untouched) before the lock is taken, so a refused library gets no
	// .picmover directory. Opening writes nothing; a catalog of the
	// previous format is upgraded only after the lock is held.
	db, err = catalog.OpenExistingNoUpgrade(ctx, lib.DBPath(), stderrLogger())
	needsUpgrade := errors.Is(err, catalog.ErrNeedsUpgrade)
	if err != nil && !(needsUpgrade && upgrade) {
		return nil, nil, nil, upgradeError(lib.Root(), catalogError(lib.Root(), err))
	}
	unlock = func() {}
	if lockName != "" {
		if unlock, err = lockLibrary(ctx, lib, lockName, errOut); err != nil {
			if db != nil {
				db.Close()
			}
			return nil, nil, nil, err
		}
	}
	if needsUpgrade {
		if db, err = catalog.OpenExisting(ctx, lib.DBPath(), stderrLogger()); err != nil {
			unlock()
			return nil, nil, nil, catalogError(lib.Root(), err)
		}
	}
	return lib, db, unlock, nil
}

// upgradeError turns catalog.ErrNeedsUpgrade into a message naming a
// command that upgrades the catalog; other errors are returned unchanged.
func upgradeError(root string, err error) error {
	if !errors.Is(err, catalog.ErrNeedsUpgrade) {
		return err
	}
	return fmt.Errorf("the catalog of %s has the previous format; a command that changes the library upgrades it in place "+
		"(for example 'picmover thumbs %s', or opening it in 'picmover serve'), then run this again", root, quoteArg(root))
}

// lockLibrary takes the library lock for the command name. When the file
// system cannot lock files it warns on errOut and goes on without the lock;
// when another process holds it, the error names that process ("library
// is in use by another picmover process (pid N, serve, since ...)").
func lockLibrary(ctx context.Context, lib *library.Library, name string, errOut io.Writer) (func(), error) {
	return server.LockLibrary(ctx, lib, name, errOut)
}

// catalogError turns catalog.ErrNeedsRebuild into a message naming
// "picmover rebuild <library>"; other errors are returned unchanged.
func catalogError(root string, err error) error { return server.CatalogError(root, err) }

// quoteArg quotes a path for a shell command line when it needs it.
func quoteArg(s string) string { return library.QuoteArg(s) }
