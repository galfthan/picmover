// Package userdata is the user's own data about a library's media,
// <library>/user.db: ratings, tags, albums, the trash timestamps and the
// near-duplicate pairs the user chose to ignore.
//
// Unlike the catalog (media.db), which is derived from the files and may be
// rebuilt at any time, user.db cannot be regenerated: it is never touched by
// "picmover rebuild", and its schema will get real migrations. Every row is
// keyed by the catalog's content hash (stored as int64, the same conversion
// as media.hash), so ratings, tags and album memberships survive a rebuild
// and come back when the same content is imported again.
//
// The package exposes typed functions only; no SQL leaks to callers. Every
// write is a single transaction.
package userdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SchemaVersion is the user.db schema version this package reads and
// writes (PRAGMA user_version).
const SchemaVersion = 1

// FileName is the name of the user data database at the library root.
const FileName = "user.db"

const dsnParams = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)&_txlock=immediate"

const schemaV1 = `
CREATE TABLE ratings (
    hash       INTEGER PRIMARY KEY,
    rating     INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
    updated_at INTEGER NOT NULL
);
CREATE TABLE tags (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at INTEGER NOT NULL
);
CREATE TABLE media_tags (
    hash     INTEGER NOT NULL,
    tag_id   INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (hash, tag_id)
);
CREATE INDEX media_tags_tag ON media_tags(tag_id);
CREATE TABLE albums (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    cover_hash INTEGER
);
CREATE TABLE album_items (
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    hash     INTEGER NOT NULL,
    position INTEGER NOT NULL,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (album_id, hash)
);
CREATE INDEX album_items_hash ON album_items(hash);
CREATE TABLE trash (
    hash       INTEGER PRIMARY KEY,
    deleted_at INTEGER NOT NULL
);
CREATE TABLE similar_ignored (
    hash_a   INTEGER NOT NULL,
    hash_b   INTEGER NOT NULL,
    added_at INTEGER NOT NULL,
    PRIMARY KEY (hash_a, hash_b)
);
`

// Errors returned (wrapped) by the API.
var (
	// ErrNotFound: no tag or album has the given id.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken: another tag has this name (case-insensitively).
	ErrNameTaken = errors.New("name is already taken")
	// ErrInvalidName: a tag or album name fails the length or character rules.
	ErrInvalidName = errors.New("invalid name")
	// ErrInvalidRating: a rating outside 0-5.
	ErrInvalidRating = errors.New("rating must be 0-5")
)

// Name limits, in runes after trimming.
const (
	MaxTagName   = 64
	MaxAlbumName = 128
)

// DB is an open user.db. It is safe for concurrent use.
type DB struct {
	db   *sql.DB
	path string
	now  func() time.Time // tests override it
}

// Open opens the user data database at path, creating it with schema
// SchemaVersion when it is missing or empty. A database with another
// user_version (other than 0 for a fresh file), or a version-0 file that
// already has tables, is refused with an error naming the file: user.db
// is never silently ignored or overwritten.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("userdata: empty database path")
	}
	if strings.Contains(path, "?") {
		// The driver splits the DSN at the first '?'.
		return nil, fmt.Errorf("userdata: database path %q contains '?', which is not supported", path)
	}
	sqldb, err := sql.Open("sqlite", path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("userdata: open %s: %w", path, err)
	}
	d := &DB{db: sqldb, path: path, now: time.Now}
	if err := d.ensureSchema(ctx); err != nil {
		sqldb.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) ensureSchema(ctx context.Context) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("userdata: open %s: %w", d.path, err)
	}
	defer tx.Rollback()
	var v, tables int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table')`).Scan(&v, &tables); err != nil {
		return fmt.Errorf("userdata: open %s: %w", d.path, err)
	}
	switch {
	case v == SchemaVersion:
		return nil
	case v == 0 && tables == 0:
	case v == 0:
		return fmt.Errorf("userdata: %s is not a picmover user database (it has tables but no schema version)", d.path)
	default:
		return fmt.Errorf("userdata: %s has schema version %d, this picmover supports %d (upgrade picmover; the file is left untouched)",
			d.path, v, SchemaVersion)
	}
	if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("userdata: create schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("userdata: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("userdata: create schema: %w", err)
	}
	return nil
}

// Close closes the database. It may be called more than once.
func (d *DB) Close() error { return d.db.Close() }

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// hashArray encodes hashes as a JSON array of int64 for json_each.
func hashArray(hashes []uint64) string {
	ints := make([]int64, len(hashes))
	for i, h := range hashes {
		ints[i] = int64(h)
	}
	b, _ := json.Marshal(ints)
	return string(b)
}

// write runs fn in one IMMEDIATE transaction. Errors wrapping the
// package's sentinel errors are returned as they are.
func (d *DB) write(ctx context.Context, what string, fn func(tx *sql.Tx, now int64) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("userdata: %s: %w", what, err)
	}
	defer tx.Rollback()
	if err := fn(tx, d.now().Unix()); err != nil {
		for _, s := range []error{ErrNotFound, ErrNameTaken, ErrInvalidName, ErrInvalidRating} {
			if errors.Is(err, s) {
				return err
			}
		}
		return fmt.Errorf("userdata: %s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("userdata: %s: %w", what, err)
	}
	return nil
}

// queryHashes runs q and returns the first column as hashes (never nil).
func (d *DB) queryHashes(ctx context.Context, what, q string, args ...any) ([]uint64, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("userdata: %s: %w", what, err)
	}
	defer rows.Close()
	out := []uint64{}
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("userdata: %s: %w", what, err)
		}
		out = append(out, uint64(h))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: %s: %w", what, err)
	}
	return out, nil
}

// CleanName trims name and checks it: 1 to limit runes, no control or
// line/paragraph separator characters. It returns the trimmed name or an
// error wrapping ErrInvalidName.
func CleanName(name string, limit int) (string, error) {
	name = strings.TrimSpace(name)
	n := 0
	for _, r := range name {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || r == '�' {
			return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidName)
		}
		n++
	}
	if n == 0 || n > limit {
		return "", fmt.Errorf("%w: want 1-%d characters", ErrInvalidName, limit)
	}
	return name, nil
}

// Counts are the row counts reported by the library endpoint.
type Counts struct {
	Ratings, Tags, Albums, Trashed, IgnoredPairs int64
}

// Counts returns the number of rated items, tags, albums, trash rows and
// ignored pairs.
func (d *DB) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := d.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM ratings), (SELECT COUNT(*) FROM tags),
		(SELECT COUNT(*) FROM albums), (SELECT COUNT(*) FROM trash), (SELECT COUNT(*) FROM similar_ignored)`).
		Scan(&c.Ratings, &c.Tags, &c.Albums, &c.Trashed, &c.IgnoredPairs)
	if err != nil {
		return Counts{}, fmt.Errorf("userdata: counts: %w", err)
	}
	return c, nil
}
