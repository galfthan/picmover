package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Read-only peeks at a user.db that is not open (picmover share create,
// the Share page for a closed library): no lock is taken and nothing is
// created, converted or repaired.

// ErrNoUserDB: the library has no user.db yet (it was never opened).
var ErrNoUserDB = errors.New("the library has no user.db yet; open it once in picmover")

// peekOpen opens the user.db at path read-only. It tries a plain
// read-only open first (it sees changes still in the -wal file of a
// process that has the library open) and falls back to an immutable
// open, which works in folders picmover cannot write. version is the
// file's user_version.
func peekOpen(ctx context.Context, path string) (db *sql.DB, version int, err error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && fi.Size() == 0) {
		return nil, 0, ErrNoUserDB
	}
	if err != nil {
		return nil, 0, fmt.Errorf("userdata: %w", err)
	}
	if strings.Contains(path, "?") {
		return nil, 0, fmt.Errorf("userdata: database path %q contains '?', which is not supported", path)
	}
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	readVersion := func(db *sql.DB) (v int, err error) {
		err = db.QueryRowContext(ctx, `SELECT user_version FROM pragma_user_version`).Scan(&v)
		return v, err
	}
	if db, err = sql.Open("sqlite", u.String()+"?mode=ro"); err == nil {
		if version, err = readVersion(db); err == nil {
			return db, version, nil
		}
		db.Close()
	}
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	if db, err = openReadOnly(path); err != nil {
		return nil, 0, err
	}
	if version, err = readVersion(db); err != nil {
		db.Close()
		return nil, 0, fmt.Errorf("userdata: open %s: %w", path, err)
	}
	return db, version, nil
}

// PeekAlbums reads the albums of the user.db at path read-only, most
// recently updated first, as DB.Albums.
func PeekAlbums(ctx context.Context, path string) ([]Album, error) {
	db, v, err := peekOpen(ctx, path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if v != SchemaVersion && !migratable(v) {
		return nil, fmt.Errorf("userdata: %s: %w", path, &VersionError{Version: v})
	}
	rows, err := db.QueryContext(ctx, albumSelect+" ORDER BY a.updated_at DESC, a.id DESC")
	if err != nil {
		return nil, fmt.Errorf("userdata: albums: %w", err)
	}
	defer rows.Close()
	out := []Album{}
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, fmt.Errorf("userdata: albums: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: albums: %w", err)
	}
	return out, nil
}
