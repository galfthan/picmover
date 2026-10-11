package userdata

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// MaxTargetLabel is the length limit of a backup target's label, in runes.
const MaxTargetLabel = 64

// BackupTarget is a backup target the library has been backed up to,
// remembered by the id of the target's marker file.
type BackupTarget struct {
	ID       string // marker id, 16 hex digits
	Label    string // user-given
	LastPath string // where it was last seen on this machine
	LastHost string // the host name it was last seen on
	// SetName is the folder of this library's backup set on the target;
	// "" means the default (the library's folder name).
	SetName      string
	AddedAt      time.Time
	LastBackupAt time.Time // zero: never
	LastScrubAt  time.Time // zero: never
}

// TargetUpdate changes a target; nil fields are left alone.
type TargetUpdate struct {
	Label    *string
	LastPath *string
	LastHost *string
	SetName  *string
}

// newLibraryID returns 16 random hex digits.
func newLibraryID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// LibraryID returns the library's identity, created with the schema (or
// the migration to version 2): 16 hex digits that never change, so a
// backup target can tell this library from another one with the same
// folder name.
func (d *DB) LibraryID(ctx context.Context) (string, error) {
	var id string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'library_id'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Written by create and migrate; repair a deleted row.
		if err := d.write(ctx, "library id", func(tx *sql.Tx, now int64) error { return d.writeIdentity(ctx, tx) }); err != nil {
			return "", err
		}
		return d.LibraryID(ctx)
	}
	if err != nil {
		return "", fmt.Errorf("userdata: library id: %w", err)
	}
	return id, nil
}

// CreatedAt returns when the library's identity was created.
func (d *DB) CreatedAt(ctx context.Context) (time.Time, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'created_at'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("userdata: created_at: %w", err)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, nil
	}
	return time.Unix(n, 0), nil
}

const targetColumns = `id, label, last_path, last_host, set_name, added_at, last_backup_at, last_scrub_at`

func scanTarget(r interface{ Scan(...any) error }) (BackupTarget, error) {
	var (
		t              BackupTarget
		added          int64
		backup, scrubb sql.NullInt64
	)
	if err := r.Scan(&t.ID, &t.Label, &t.LastPath, &t.LastHost, &t.SetName, &added, &backup, &scrubb); err != nil {
		return BackupTarget{}, err
	}
	t.AddedAt = time.Unix(added, 0)
	if backup.Valid {
		t.LastBackupAt = time.Unix(backup.Int64, 0)
	}
	if scrubb.Valid {
		t.LastScrubAt = time.Unix(scrubb.Int64, 0)
	}
	return t, nil
}

// BackupTargets returns the library's targets, in the order they were
// added.
func (d *DB) BackupTargets(ctx context.Context) ([]BackupTarget, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+targetColumns+` FROM backup_targets ORDER BY added_at, rowid`)
	if err != nil {
		return nil, fmt.Errorf("userdata: backup targets: %w", err)
	}
	defer rows.Close()
	out := []BackupTarget{}
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, fmt.Errorf("userdata: backup targets: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("userdata: backup targets: %w", err)
	}
	return out, nil
}

// BackupTarget returns the target with id, or an error wrapping
// ErrNotFound.
func (d *DB) BackupTarget(ctx context.Context, id string) (BackupTarget, error) {
	t, err := scanTarget(d.db.QueryRowContext(ctx, `SELECT `+targetColumns+` FROM backup_targets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BackupTarget{}, fmt.Errorf("backup target %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return BackupTarget{}, fmt.Errorf("userdata: backup target: %w", err)
	}
	return t, nil
}

// CleanLabel trims a target label and checks it like a tag name (1-64
// runes, no control characters).
func CleanLabel(label string) (string, error) { return CleanName(label, MaxTargetLabel) }

// AddBackupTarget attaches the target t (ID, Label, LastPath, LastHost,
// SetName; AddedAt is now). When the id is already attached, its label
// (when t.Label is not empty), path and host are updated instead and
// created is false.
func (d *DB) AddBackupTarget(ctx context.Context, t BackupTarget) (out BackupTarget, created bool, err error) {
	label, err := CleanLabel(t.Label)
	if err != nil {
		return BackupTarget{}, false, err
	}
	err = d.write(ctx, "add backup target", func(tx *sql.Tx, now int64) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO backup_targets (id, label, last_path, last_host, set_name, added_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, t.ID, label, t.LastPath, t.LastHost, t.SetName, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			created = true
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE backup_targets SET label = ?, last_path = ?, last_host = ? WHERE id = ?`,
			label, t.LastPath, t.LastHost, t.ID)
		return err
	})
	if err != nil {
		return BackupTarget{}, false, err
	}
	out, err = d.BackupTarget(ctx, t.ID)
	return out, created, err
}

// UpdateBackupTarget changes the fields of u that are set; ErrNotFound
// for an unknown id, ErrInvalidName for a bad label.
func (d *DB) UpdateBackupTarget(ctx context.Context, id string, u TargetUpdate) (BackupTarget, error) {
	if u.Label != nil {
		l, err := CleanLabel(*u.Label)
		if err != nil {
			return BackupTarget{}, err
		}
		u.Label = &l
	}
	err := d.write(ctx, "update backup target", func(tx *sql.Tx, now int64) error {
		res, err := tx.ExecContext(ctx, `UPDATE backup_targets SET label = coalesce(?, label),
			last_path = coalesce(?, last_path), last_host = coalesce(?, last_host), set_name = coalesce(?, set_name)
			WHERE id = ?`, nullString(u.Label), nullString(u.LastPath), nullString(u.LastHost), nullString(u.SetName), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("backup target %s: %w", id, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return BackupTarget{}, err
	}
	return d.BackupTarget(ctx, id)
}

func nullString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ForgetBackupTarget detaches a target (the target itself is untouched);
// ErrNotFound for an unknown id.
func (d *DB) ForgetBackupTarget(ctx context.Context, id string) error {
	return d.write(ctx, "forget backup target", func(tx *sql.Tx, now int64) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM backup_targets WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("backup target %s: %w", id, ErrNotFound)
		}
		return nil
	})
}

// MarkBackup records a finished backup run to the target at at.
func (d *DB) MarkBackup(ctx context.Context, id string, at time.Time) error {
	return d.mark(ctx, id, "last_backup_at", at)
}

// MarkScrub records a finished scrub of the target at at.
func (d *DB) MarkScrub(ctx context.Context, id string, at time.Time) error {
	return d.mark(ctx, id, "last_scrub_at", at)
}

func (d *DB) mark(ctx context.Context, id, column string, at time.Time) error {
	return d.write(ctx, "mark backup target", func(tx *sql.Tx, now int64) error {
		// column is one of two constants above, never user input.
		res, err := tx.ExecContext(ctx, `UPDATE backup_targets SET `+column+` = ? WHERE id = ?`, at.Unix(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("backup target %s: %w", id, ErrNotFound)
		}
		return nil
	})
}

// BackupSummary is what the Libraries page shows of a library's backups.
type BackupSummary struct {
	Targets      int       // attached targets
	LastBackupAt time.Time // newest last_backup_at; zero: never
	LastLabel    string    // the label of that target
	LastID       string    // and its id
}

// BackupSummary returns the backup summary from an open database.
func (d *DB) BackupSummary(ctx context.Context) (BackupSummary, error) {
	return backupSummary(ctx, d.db)
}

func backupSummary(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (BackupSummary, error) {
	var (
		s     BackupSummary
		last  sql.NullInt64
		label sql.NullString
		id    sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM backup_targets),
		(SELECT last_backup_at FROM backup_targets WHERE last_backup_at IS NOT NULL ORDER BY last_backup_at DESC LIMIT 1),
		(SELECT label FROM backup_targets WHERE last_backup_at IS NOT NULL ORDER BY last_backup_at DESC LIMIT 1),
		(SELECT id FROM backup_targets WHERE last_backup_at IS NOT NULL ORDER BY last_backup_at DESC LIMIT 1)`).
		Scan(&s.Targets, &last, &label, &id)
	if err != nil {
		return BackupSummary{}, fmt.Errorf("userdata: backup summary: %w", err)
	}
	if last.Valid {
		s.LastBackupAt = time.Unix(last.Int64, 0)
		s.LastLabel, s.LastID = label.String, id.String
	}
	return s, nil
}
