package userdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
