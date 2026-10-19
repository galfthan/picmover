package userdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// attemptPrefix starts the meta keys of the backup attempts:
// backup_attempt:<target id>. They are bookkeeping a backup writes, like
// backup_targets.last_backup_at, so ContentHash leaves them out.
const attemptPrefix = "backup_attempt:"

// BackupAttempt is the outcome of the newest backup job to a target,
// kept in user.db (not on the target): a run whose end could not be
// recorded in the target's backup.db is still known here, and the
// Libraries page can tell an incomplete backup from none without
// reaching the target.
type BackupAttempt struct {
	At time.Time `json:"-"`
	// Error is why the run stopped (client text, no paths); "" when it
	// finished.
	Error     string `json:"error,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
	// SetFiles and SetBytes are the files of the set on the target after
	// the run (-1: unknown).
	SetFiles int64 `json:"setFiles"`
	SetBytes int64 `json:"setBytes"`
	// LibraryFiles is the number of files of the library the run planned
	// to back up (0: unknown).
	LibraryFiles int64 `json:"libraryFiles"`
}

type attemptJSON struct {
	BackupAttempt
	At int64 `json:"at"`
}

// Failed reports whether the run stopped before it was done (an error or
// a cancel).
func (a BackupAttempt) Failed() bool { return a.Error != "" || a.Cancelled }

// RecordBackupAttempt stores the outcome of a backup job to the target
// id (replacing the previous one). An unknown target is ErrNotFound.
func (d *DB) RecordBackupAttempt(ctx context.Context, id string, a BackupAttempt) error {
	b, err := json.Marshal(attemptJSON{BackupAttempt: a, At: a.At.Unix()})
	if err != nil {
		return err
	}
	return d.write(ctx, "record backup attempt", func(tx *sql.Tx, now int64) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_targets WHERE id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("backup target %s: %w", id, ErrNotFound)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, attemptPrefix+id, string(b))
		return err
	})
}

// BackupAttempts returns the recorded attempts by target id (targets
// without one are left out).
func (d *DB) BackupAttempts(ctx context.Context) (map[string]BackupAttempt, error) {
	return backupAttempts(ctx, d.db)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func backupAttempts(ctx context.Context, q querier) (map[string]BackupAttempt, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM meta WHERE key LIKE ? ESCAPE '\'`,
		strings.ReplaceAll(attemptPrefix, "_", `\_`)+"%")
	if err != nil {
		return nil, fmt.Errorf("userdata: backup attempts: %w", err)
	}
	defer rows.Close()
	out := map[string]BackupAttempt{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("userdata: backup attempts: %w", err)
		}
		var a attemptJSON
		if json.Unmarshal([]byte(v), &a) != nil {
			continue
		}
		a.BackupAttempt.At = time.Unix(a.At, 0)
		out[strings.TrimPrefix(k, attemptPrefix)] = a.BackupAttempt
	}
	return out, rows.Err()
}
