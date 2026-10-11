package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Identity is who a library is, as a backup set records it: the
// library's id (copied along with user.db when the library folder is
// copied) and its instance, which is new whenever the library is found
// at another folder or on another host than it last was, so a copy of a
// library (same id) can be told from the original.
type Identity struct {
	LibraryID string
	Instance  string // 16 hex digits
	Root      string // the library folder, absolute, links resolved
	Host      string // the host name
}

// hostName is os.Hostname; tests replace it.
var hostName = func() string {
	h, _ := os.Hostname()
	return h
}

// canonicalRoot is the folder holding the database, absolute and with
// links resolved when they can be.
func (d *DB) canonicalRoot() string {
	dir, err := filepath.Abs(filepath.Dir(d.path))
	if err != nil {
		dir = filepath.Dir(d.path)
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return filepath.Clean(dir)
}

func sameRoot(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readInstance(ctx context.Context, q rowQuerier) (inst, root, host string, err error) {
	err = q.QueryRowContext(ctx, `SELECT
		coalesce((SELECT value FROM meta WHERE key = 'instance'), ''),
		coalesce((SELECT value FROM meta WHERE key = 'root'), ''),
		coalesce((SELECT value FROM meta WHERE key = 'host'), '')`).Scan(&inst, &root, &host)
	return inst, root, host, err
}

// Identity returns the library's identity. The instance, root and host
// are kept in meta (instance, root, host); when none is stored yet, or
// the stored root or host differ from where the database is now (the
// library was moved or copied), a new instance id is created and the
// current root and host are stored, in one transaction.
func (d *DB) Identity(ctx context.Context) (Identity, error) {
	libID, err := d.LibraryID(ctx)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{LibraryID: libID, Root: d.canonicalRoot(), Host: hostName()}
	same := func(inst, root, host string) bool {
		return inst != "" && sameRoot(root, id.Root) && strings.EqualFold(host, id.Host)
	}
	inst, root, host, err := readInstance(ctx, d.db)
	if err != nil {
		return Identity{}, fmt.Errorf("userdata: identity: %w", err)
	}
	if same(inst, root, host) {
		id.Instance = inst
		return id, nil
	}
	err = d.write(ctx, "identity", func(tx *sql.Tx, now int64) error {
		// Read again inside the write transaction: another process may
		// have written it in between.
		inst, root, host, err := readInstance(ctx, tx)
		if err != nil {
			return err
		}
		if same(inst, root, host) {
			id.Instance = inst
			return nil
		}
		if id.Instance, err = newLibraryID(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('instance', ?), ('root', ?), ('host', ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, id.Instance, id.Root, id.Host)
		return err
	})
	if err != nil {
		return Identity{}, err
	}
	return id, nil
}

// PeekIdentity is Identity without writing: when this database has no
// instance yet, or its stored root or host differ from where it is now,
// Instance is "" (the instance a write would create is new, so no set
// can have recorded it).
func (d *DB) PeekIdentity(ctx context.Context) (Identity, error) {
	var libID string
	if err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'library_id'`).Scan(&libID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Identity{}, errors.New("userdata: the library has no identity yet")
		}
		return Identity{}, fmt.Errorf("userdata: library id: %w", err)
	}
	id := Identity{LibraryID: libID, Root: d.canonicalRoot(), Host: hostName()}
	inst, root, host, err := readInstance(ctx, d.db)
	if err != nil {
		return Identity{}, fmt.Errorf("userdata: identity: %w", err)
	}
	if inst != "" && sameRoot(root, id.Root) && strings.EqualFold(host, id.Host) {
		id.Instance = inst
	}
	return id, nil
}

// ErrNeedsMigration: OpenReadOnly found a user.db of schema version 1,
// which a command that writes would upgrade first.
var ErrNeedsMigration = fmt.Errorf("user.db has schema version 1 and would be upgraded to version %d first (a run without --dry-run does that)", SchemaVersion)

// OpenReadOnly opens an existing user.db read-only (SQLite mode=ro; its
// WAL is read, nothing is written, nothing is created or migrated). A
// missing file is fs.ErrNotExist, version 1 ErrNeedsMigration, another
// version a *VersionError. A version-2 file is opened as it is: it has
// the identity and the backup targets (all a read-only caller, the
// backup dry run, reads), but not the import history, whose methods fail
// on it.
func OpenReadOnly(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("userdata: database path %q contains '?', which is not supported", path)
	}
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	sqldb, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, fmt.Errorf("userdata: open %s: %w", path, err)
	}
	d := &DB{db: sqldb, path: path, now: time.Now}
	v, tables, err := d.version(ctx)
	switch {
	case err != nil:
		err = fmt.Errorf("userdata: open %s: %w", path, err)
	case v == SchemaVersion, v == 2:
		return d, nil
	case v == 1:
		err = ErrNeedsMigration
	case v == 0 && tables == 0:
		err = fmt.Errorf("userdata: %s is empty: %w", path, os.ErrNotExist)
	case v == 0:
		err = fmt.Errorf("userdata: %s: %w", path, ErrNotUserDB)
	default:
		err = fmt.Errorf("userdata: %s: %w", path, &VersionError{Version: v})
	}
	sqldb.Close()
	return nil, err
}

// IntegrityCheck checks the user.db file at path read-only: PRAGMA
// integrity_check reports ok and its schema version is one this picmover
// reads (an older one, which Open migrates, or SchemaVersion).
func IntegrityCheck(ctx context.Context, path string) error {
	sqldb, err := openReadOnly(path)
	if err != nil {
		return err
	}
	defer sqldb.Close()
	var v int
	if err := sqldb.QueryRowContext(ctx, `SELECT user_version FROM pragma_user_version`).Scan(&v); err != nil {
		return fmt.Errorf("userdata: check %s: %w", path, err)
	}
	if !migratable(v) && v != SchemaVersion {
		return fmt.Errorf("userdata: %s: %w", path, &VersionError{Version: v})
	}
	rows, err := sqldb.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("userdata: check %s: %w", path, err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return fmt.Errorf("userdata: check %s: %w", path, err)
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("userdata: check %s: %w", path, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("userdata: %s fails the integrity check: %s", path, strings.Join(problems, "; "))
	}
	return nil
}

// PeekLibraryID reads the library id of the user.db at path read-only;
// "" when the file is missing or empty or has no identity (version 1).
func PeekLibraryID(ctx context.Context, path string) (string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("userdata: %w", err)
	}
	sqldb, err := openReadOnly(path)
	if err != nil {
		return "", err
	}
	defer sqldb.Close()
	var id string
	if err := sqldb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'library_id'`).Scan(&id); err != nil {
		// No meta table (version 1) or no row.
		return "", nil
	}
	return id, nil
}
