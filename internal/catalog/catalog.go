// Package catalog is the SQLite catalog (media.db) of a picmover library:
// schema v2, migration from the legacy v1 schema, and queries.
//
// Capture times are stored as the camera's wall-clock time encoded as Unix
// seconds "as if UTC" in taken_at, with the real zone offset (seconds east of
// UTC) in taken_at_offset only when it is known. Decoded Media.TakenAt values
// therefore always format as the wall-clock; their zone is the real offset
// when OffsetKnown is true and UTC as a placeholder otherwise.
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
const SchemaVersion = 2

const dsnParams = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate"

// DB is an open catalog.
type DB struct {
	*sql.DB
	path   string
	logger *slog.Logger
}

// ErrNeedsMigration is returned (wrapped) by OpenExisting when the catalog
// has an older schema than SchemaVersion.
var ErrNeedsMigration = errors.New("catalog needs migration: run 'picmover migrate <library>' first")

// Open opens or creates the catalog at path and migrates it to SchemaVersion.
// logger may be nil.
func Open(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
	d, err := openDB(ctx, path, logger)
	if err != nil {
		return nil, err
	}
	if err := d.migrate(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// OpenExisting opens the catalog at path like Open but never creates or
// migrates it. It returns an error if the file does not exist, an error
// wrapping ErrNeedsMigration if its schema is older than SchemaVersion
// (including a legacy v1 database), and an error if it is newer. The schema
// version is probed without enabling WAL, so an old database is not written.
// logger may be nil.
func OpenExisting(ctx context.Context, path string, logger *slog.Logger) (*DB, error) {
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
	v, err := probeVersion(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(path, v); err != nil {
		return nil, err
	}
	d, err := openDB(ctx, path, logger)
	if err != nil {
		return nil, err
	}
	// Re-check on the real connection in case the file changed meanwhile.
	v, err = d.userVersion(ctx)
	if err == nil {
		err = checkVersion(path, v)
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func checkVersion(path string, v int) error {
	switch {
	case v < SchemaVersion:
		return fmt.Errorf("catalog: %s has schema version %d, older than %d: %w", path, v, SchemaVersion, ErrNeedsMigration)
	case v > SchemaVersion:
		return fmt.Errorf("catalog: %s has schema version %d, newer than the supported version %d; upgrade picmover", path, v, SchemaVersion)
	}
	return nil
}

// probeVersion reads user_version through a connection that sets no
// journal_mode, so probing a rollback-journal (v1) file does not convert it.
func probeVersion(ctx context.Context, path string) (int, error) {
	sqldb, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return 0, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	defer sqldb.Close()
	var v int
	if err := sqldb.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("catalog: open %s: read user_version: %w", path, err)
	}
	return v, nil
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

// openDB opens the database with the connection pragmas, without migrating.
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

func (d *DB) userVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("catalog: read user_version: %w", err)
	}
	return v, nil
}

// migrate brings the database to SchemaVersion, one step at a time.
func (d *DB) migrate(ctx context.Context) error {
	for {
		v, err := d.userVersion(ctx)
		if err != nil {
			return err
		}
		if v > SchemaVersion {
			return checkVersion(d.path, v)
		}
		if v == SchemaVersion {
			return nil
		}
		if err := d.migrateStep(ctx, v); err != nil {
			return err
		}
		nv, err := d.userVersion(ctx)
		if err != nil {
			return err
		}
		if nv <= v {
			return fmt.Errorf("catalog: migration from version %d did not advance the schema", v)
		}
	}
}

// migrateStep performs the migration away from version from. Future steps
// (2 -> 3, ...) are added as further cases.
func (d *DB) migrateStep(ctx context.Context, from int) error {
	switch from {
	case 0:
		legacy, hasMedia, err := d.isLegacy(ctx)
		if err != nil {
			return err
		}
		if legacy {
			return d.migrateLegacy(ctx)
		}
		if hasMedia {
			return fmt.Errorf("catalog: %s has a media table of unknown schema (user_version 0)", d.path)
		}
		return d.createFresh(ctx)
	default:
		return fmt.Errorf("catalog: no migration from schema version %d", from)
	}
}

func (d *DB) isLegacy(ctx context.Context) (legacy, hasMedia bool, err error) {
	var n int
	err = d.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'media'").Scan(&n)
	if err != nil {
		return false, false, fmt.Errorf("catalog: inspect schema: %w", err)
	}
	if n == 0 {
		return false, false, nil
	}
	err = d.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('media') WHERE name = 'new_path'").Scan(&n)
	if err != nil {
		return false, true, fmt.Errorf("catalog: inspect media table: %w", err)
	}
	return n > 0, true, nil
}

func (d *DB) createFresh(ctx context.Context) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaV2); err != nil {
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
