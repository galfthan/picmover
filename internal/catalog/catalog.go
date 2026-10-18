// Package catalog is the SQLite catalog (media.db) of a picmover library:
// schema v4 and queries. A version 3 catalog is migrated in place when it
// is opened for writing (Open, OpenExisting); a catalog of any other
// schema version is refused with ErrNeedsRebuild.
//
// Capture times are stored as the camera's wall-clock time encoded as Unix
// seconds "as if UTC" in taken_at, with the real zone offset (seconds east of
// UTC) in taken_at_offset only when it is known. Decoded Media.TakenAt values
// therefore always format as the wall-clock; their zone is the real offset
// when OffsetKnown is true and UTC as a placeholder otherwise. The same
// instant as a wall clock in the library's home zone is in taken_at_home
// (Media.TakenAtHome), which orders and groups the timeline; see
// docs/design/home-time.md.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SchemaVersion is the schema version this package reads and writes.
const SchemaVersion = 4

// migratableVersion is the older schema version that Open and
// OpenExisting upgrade in place (migrateFromV3).
const migratableVersion = 3

const dsnParams = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate"

// DB is an open catalog.
type DB struct {
	*sql.DB
	path   string
	logger *slog.Logger
	// v3 is set for a read-only look at a version 3 catalog (PeekFile),
	// which has no taken_at_home: its queries order by taken_at.
	v3 bool
}

// timeCol is the column the timeline orders by: taken_at_home, or
// taken_at in a version 3 catalog read without migration.
func (d *DB) timeCol() string {
	if d.v3 {
		return "taken_at"
	}
	return "taken_at_home"
}

// ErrNeedsRebuild is returned (wrapped) by Open and OpenExisting when the
// catalog has a schema other than SchemaVersion that cannot be migrated
// (only version 3 is): the catalog is derived from the files and is
// rebuilt instead.
var ErrNeedsRebuild = errors.New("catalog format changed; run 'picmover rebuild <library>'")

// ErrNewerSchema is wrapped, next to ErrNeedsRebuild, when the catalog was
// written by a newer picmover.
var ErrNewerSchema = errors.New("catalog written by a newer picmover")

// ErrNotCatalog is returned (wrapped) when the file is not a picmover
// catalog at all.
var ErrNotCatalog = errors.New("not a picmover catalog")

// Open opens the catalog at path, creating it with schema SchemaVersion when
// the file is missing, empty, or has user_version 0 and no media table. Any
// other schema version is refused with an error wrapping ErrNeedsRebuild,
// without modifying the file. logger may be nil.
func Open(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		// Check an existing file without enabling WAL, so a database of
		// another format is left untouched.
		v, hasMedia, err := probe(ctx, path)
		if err != nil {
			return nil, err
		}
		if err := checkSchema(path, v, hasMedia, true); err != nil {
			return nil, err
		}
		if v == migratableVersion {
			if err := backupForMigration(ctx, path, logger); err != nil {
				return nil, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	d, err := openDB(ctx, path, logger)
	if err != nil {
		return nil, err
	}
	if err := retryBusy(ctx, migrationBusyFor, func() error { return d.ensureSchema(ctx) }); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.ensureDerived(ctx); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.healHome(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// ErrNeedsUpgrade is returned (wrapped) by OpenExistingNoUpgrade for a
// catalog of the previous schema version, which only a writer (Open,
// OpenExisting) upgrades in place.
var ErrNeedsUpgrade = errors.New("catalog has the previous format and is upgraded by the next command that changes the library")

// migrationBusyFor bounds how long an opener waits for another process's
// migration of the same catalog (each attempt already waits busy_timeout).
const migrationBusyFor = 5 * time.Minute

// retryBusy runs fn again while it fails with SQLITE_BUSY, until limit.
func retryBusy(ctx context.Context, limit time.Duration, fn func() error) error {
	deadline := time.Now().Add(limit)
	for {
		err := fn()
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ensureDerived creates the derived tables (similar_pairs, meta) when
// absent. Cheap when they exist: no write happens.
func (d *DB) ensureDerived(ctx context.Context) error {
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('similar_pairs', 'meta')`).Scan(&n); err != nil {
		return fmt.Errorf("catalog: inspect schema: %w", err)
	}
	if n == 2 {
		return nil
	}
	if _, err := d.ExecContext(ctx, derivedTables); err != nil {
		return fmt.Errorf("catalog: create derived tables: %w", err)
	}
	return nil
}

// OpenExisting opens the catalog at path like Open but never creates it. It
// returns an error if the file does not exist or has no schema, and an error
// wrapping ErrNeedsRebuild if its schema version is neither SchemaVersion
// nor the migratable version 3, which is upgraded in place (callers hold
// the library lock or are the server). The schema is probed without
// enabling WAL, so a database of another format is not written. logger
// may be nil.
func OpenExisting(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	return openExisting(ctx, path, logger, true)
}

// OpenExistingNoUpgrade is OpenExisting for readers that do not hold the
// library lock: a version 3 catalog is refused, untouched, with an error
// wrapping ErrNeedsUpgrade.
func OpenExistingNoUpgrade(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	return openExisting(ctx, path, logger, false)
}

func openExisting(ctx context.Context, path string, logger *slog.Logger, upgrade bool) (*DB, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("catalog: %s is not a regular file", path)
	}
	v, hasMedia, err := probe(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := checkSchema(path, v, hasMedia, false); err != nil {
		return nil, err
	}
	if v == migratableVersion {
		if !upgrade {
			return nil, fmt.Errorf("catalog: %s: %w", path, ErrNeedsUpgrade)
		}
		if err := backupForMigration(ctx, path, logger); err != nil {
			return nil, err
		}
	}
	d, err := openDB(ctx, path, logger)
	if err != nil {
		return nil, err
	}
	// Re-check on the real connection in case the file changed meanwhile.
	v, hasMedia, err = schemaState(ctx, d.DB)
	if err == nil {
		err = checkSchema(path, v, hasMedia, false)
	}
	if err == nil && v == migratableVersion && !upgrade {
		err = fmt.Errorf("catalog: %s: %w", path, ErrNeedsUpgrade)
	}
	if err == nil {
		err = d.ensureDerived(ctx)
	}
	if err == nil && v == migratableVersion {
		err = retryBusy(ctx, migrationBusyFor, func() error { return d.ensureSchema(ctx) })
	}
	if err == nil && upgrade {
		err = d.healHome(ctx)
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// checkSchema accepts version SchemaVersion and the migratable version 3,
// and also an empty database (user_version 0, no media table) when
// allowFresh is set.
func checkSchema(path string, v int, hasMedia, allowFresh bool) error {
	switch {
	case v == SchemaVersion, v == migratableVersion && hasMedia:
		return nil
	case v == 0 && !hasMedia:
		if allowFresh {
			return nil
		}
		return fmt.Errorf("catalog: %s is not a picmover catalog (no schema): %w", path, ErrNotCatalog)
	case v > SchemaVersion:
		return fmt.Errorf("catalog: %s has schema version %d, newer than the supported version %d (upgrade picmover): %w (%w)",
			path, v, SchemaVersion, ErrNeedsRebuild, ErrNewerSchema)
	}
	return fmt.Errorf("catalog: %s has schema version %d, want %d: %w", path, v, SchemaVersion, ErrNeedsRebuild)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// schemaState returns user_version and whether a media table exists. Both
// are read by one statement, so they come from the same snapshot: read
// separately, a concurrent schema creation committing in between would make
// a new catalog look like a v1 catalog (user_version 0 with a media table).
func schemaState(ctx context.Context, q queryRower) (version int, hasMedia bool, err error) {
	var n int
	err = q.QueryRowContext(ctx, `SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'media')`).Scan(&version, &n)
	if err != nil {
		return 0, false, fmt.Errorf("catalog: inspect schema: %w", err)
	}
	return version, n > 0, nil
}

// probe reads the schema state through a connection that sets no
// journal_mode, so probing a rollback-journal file does not convert it.
func probe(ctx context.Context, path string) (version int, hasMedia bool, err error) {
	sqldb, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return 0, false, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	defer sqldb.Close()
	version, hasMedia, err = schemaState(ctx, sqldb)
	if err != nil {
		return 0, false, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	return version, hasMedia, nil
}

func checkPath(path string) error {
	if path == "" {
		return errors.New("catalog: empty database path")
	}
	if strings.Contains(path, "?") {
		// The driver splits the DSN at the first '?', so it would open a
		// different file and drop the pragmas.
		return fmt.Errorf("catalog: database path %q contains '?', which is not supported", path)
	}
	return nil
}

// openDB opens the database with the connection pragmas, without touching
// the schema.
func openDB(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	sqldb, err := sql.Open("sqlite", path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	if err := pingRetryBusy(ctx, sqldb); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	var mode string
	if err := sqldb.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("catalog: open %s: read journal_mode: %w", path, err)
	}
	if !strings.EqualFold(mode, "wal") {
		logger.Warn("filesystem does not support WAL journal mode; performance and crash safety are reduced",
			"path", path, "journal_mode", mode)
	}
	return &DB{DB: sqldb, path: path, logger: logger}, nil
}

// pingRetryBusy pings db, retrying while SQLITE_BUSY is returned. Switching
// a rollback-journal file to WAL (the journal_mode pragma run on each new
// connection) needs an exclusive lock, and when two connections attempt it
// at once SQLite returns SQLITE_BUSY at once instead of invoking the busy
// handler. Once the file is in WAL mode the pragma takes no lock.
func pingRetryBusy(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyRetryFor)
	for {
		err := db.PingContext(ctx)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

const busyRetryFor = 10 * time.Second

func isBusy(err error) bool {
	var ce interface{ Code() int }
	return errors.As(err, &ce) && ce.Code()&0xff == 5 // SQLITE_BUSY
}

// ensureSchema creates the schema if the database is empty, or migrates a
// version 3 catalog, inside an IMMEDIATE transaction so concurrent openers
// do it once.
func (d *DB) ensureSchema(ctx context.Context) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: begin: %w", err)
	}
	defer tx.Rollback()
	v, hasMedia, err := schemaState(ctx, tx)
	if err != nil {
		return err
	}
	if err := checkSchema(d.path, v, hasMedia, true); err != nil {
		return err
	}
	if v == SchemaVersion {
		return nil
	}
	if v == migratableVersion {
		return d.migrateFromV3(ctx, tx)
	}
	if _, err := tx.ExecContext(ctx, schemaV4); err != nil {
		return fmt.Errorf("catalog: create schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("catalog: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: commit schema: %w", err)
	}
	d.logger.Info("catalog created", "path", d.path, "version", SchemaVersion)
	return nil
}
