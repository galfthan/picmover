package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"picmover/internal/userdata"
)

// Identity is a library's identity as a set records it: its library id
// and the instance (with its folder and host) that last wrote the set.
type Identity = userdata.Identity

// OtherLibraryError: the set folder holds another library's backup. It
// matches ErrOtherLibrary; its text names the set folder only.
type OtherLibraryError struct {
	Name string
}

func (e *OtherLibraryError) Error() string {
	return fmt.Sprintf("the folder %s on this target holds another library's backup", e.Name)
}

func (e *OtherLibraryError) Is(target error) bool { return target == ErrOtherLibrary }

// ErrForeignInstance is matched by a *ForeignError.
var ErrForeignInstance = errors.New("this backup set was last written by another copy of this library")

// ForeignError: the set belongs to this library (same library id) but
// was last written by another instance of it: a copy of the library
// folder, or the library before it was moved or used from another host.
// Its text names that library's folder and host, the one place a client
// sees an absolute path: the user needs it to tell the copies apart.
type ForeignError struct {
	Root, Host string
}

func (e *ForeignError) Error() string {
	host := e.Host
	if host == "" {
		host = "an unknown host"
	}
	return fmt.Sprintf("this backup set was last written by the library at %s on %s (a copy of this library, "+
		"or this library before it was moved); take the set over to back up into it from here", e.Root, host)
}

func (e *ForeignError) Is(target error) bool { return target == ErrForeignInstance }

// Set meta keys of the last writer.
const (
	metaLibraryID = "library_id"
	metaInstance  = "instance"
	metaRoot      = "writer_root"
	metaHost      = "writer_host"
)

// Owner returns the set's owner: the library id and the instance, root
// and host of the library that last wrote it ("" when unknown).
func (s *Set) Owner(ctx context.Context) (Identity, error) {
	return readOwner(ctx, s.db)
}

func readOwner(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Identity, error) {
	var o Identity
	err := q.QueryRowContext(ctx, `SELECT
		coalesce((SELECT value FROM meta WHERE key = ?), ''),
		coalesce((SELECT value FROM meta WHERE key = ?), ''),
		coalesce((SELECT value FROM meta WHERE key = ?), ''),
		coalesce((SELECT value FROM meta WHERE key = ?), '')`, metaLibraryID, metaInstance, metaRoot, metaHost).
		Scan(&o.LibraryID, &o.Instance, &o.Root, &o.Host)
	if err != nil {
		return Identity{}, fmt.Errorf("backup: owner: %w", err)
	}
	return o, nil
}

// checkOwner compares the set's owner with the library id: another
// library is an *OtherLibraryError, another instance of this library a
// *ForeignError (unless takeOver), no owner ErrNotOwned when owned is
// required.
func (s *Set) checkOwner(owner, id Identity, owned, takeOver bool) error {
	switch {
	case owner.LibraryID == "":
		if owned {
			return ErrNotOwned
		}
		return nil
	case owner.LibraryID != id.LibraryID:
		return &OtherLibraryError{Name: s.Name}
	case owner.Instance != "" && owner.Instance != id.Instance && !takeOver:
		return &ForeignError{Root: owner.Root, Host: owner.Host}
	}
	return nil
}

// CheckOwner reports whether the library id may use the set without
// writing anything: nil when the set belongs to it (and was last written
// by its instance, or the instance is not recorded) or has no owner yet
// (unless owned).
func (s *Set) CheckOwner(ctx context.Context, id Identity, owned bool) error {
	if id.LibraryID == "" {
		return errors.New("backup: the library's identity is unknown")
	}
	owner, err := s.Owner(ctx)
	if err != nil {
		return err
	}
	return s.checkOwner(owner, id, owned, false)
}

// Claim makes the library id the set's owner and last writer, in one
// transaction: a set without an owner takes the library id (unless
// owned is required: ErrNotOwned); a set of another library is an
// *OtherLibraryError; a set last written by another instance of this
// library is a *ForeignError unless takeOver. On success the set
// records id's instance, root and host as its last writer.
func (s *Set) Claim(ctx context.Context, id Identity, owned, takeOver bool) error {
	if id.LibraryID == "" || id.Instance == "" {
		return errors.New("backup: the library's identity is unknown")
	}
	tx, err := s.db.BeginTx(ctx, nil) // immediate (setDSN)
	if err != nil {
		return fmt.Errorf("backup: claim: %w", err)
	}
	defer tx.Rollback()
	if !owned {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`,
			metaLibraryID, id.LibraryID); err != nil {
			return fmt.Errorf("backup: claim: %w", err)
		}
	}
	owner, err := readOwner(ctx, tx)
	if err != nil {
		return err
	}
	if err := s.checkOwner(owner, id, true, takeOver); err != nil {
		return err
	}
	if owner.Instance != id.Instance || owner.Root != id.Root || owner.Host != id.Host {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?), (?, ?), (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			metaInstance, id.Instance, metaRoot, id.Root, metaHost, id.Host); err != nil {
			return fmt.Errorf("backup: claim: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("backup: claim: %w", err)
	}
	return nil
}

// CheckSetOwner checks, without writing, that the set called name on t
// may be used by the library id: nil when there is no such set yet or
// CheckOwner allows it.
func CheckSetOwner(ctx context.Context, t *Target, name string, id Identity) error {
	if !SetExists(t, name) {
		return nil
	}
	s, err := OpenSet(ctx, t, name, false)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.CheckOwner(ctx, id, false)
}
