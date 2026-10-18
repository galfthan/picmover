package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"picmover/internal/library"
)

// backupForMigration writes a consistent copy of the version 3 catalog at
// path (VACUUM INTO) to <path>.bak-<YYYYMMDD-HHMMSS>, the name a rebuild
// gives its backups, before it is upgraded in place. The copy goes to a
// temporary name first and is renamed without replacing anything (the
// next second's name is tried when that one is taken). Restoring it with
// an older picmover undoes the upgrade.
func backupForMigration(ctx context.Context, path string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	sqldb, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return fmt.Errorf("catalog: back up %s before upgrading: %w", path, err)
	}
	defer sqldb.Close()
	now := time.Now()
	tmp := fmt.Sprintf("%s.bak-%s.%d.tmp", path, now.Format(library.BackupStampLayout), os.Getpid())
	os.Remove(tmp) // a leftover of this pid's earlier crash
	if _, err := sqldb.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("catalog: back up %s before upgrading (the file is left untouched): %w", path, err)
	}
	for i := range 60 {
		dst := path + ".bak-" + now.Add(time.Duration(i)*time.Second).Format(library.BackupStampLayout)
		err := library.RenameNoReplace(tmp, dst)
		if err == nil {
			logger.Info("catalog backed up before upgrading", "path", path, "backup", dst)
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	os.Remove(tmp)
	return fmt.Errorf("catalog: back up %s before upgrading: no free backup name", path)
}

// expressTime returns the stored time columns of a dated row: its wall
// clock and offset, re-expressed in the home zone when the offset source
// is 'home' (such a row shows its instant in the home zone), and its home
// time.
func expressTime(wall int64, off sql.NullInt64, src string, loc *time.Location) (newWall int64, newOff sql.NullInt64, home int64) {
	if !off.Valid || loc == nil {
		return wall, off, wall
	}
	inst := time.Unix(wall-off.Int64, 0).In(loc)
	home = wallSeconds(inst)
	if src == OffsetSourceHome {
		_, o := inst.Zone()
		return home, sql.NullInt64{Int64: int64(o), Valid: true}, home
	}
	return wall, off, home
}

// healHome fills taken_at_home (and the offset source) of dated rows that
// have none: rows written after the upgrade by an older picmover, which
// does not know these columns. Cheap when there is none: one lookup in
// the taken_at_home index.
func (d *DB) healHome(ctx context.Context) error {
	var one int
	err := d.QueryRowContext(ctx, `SELECT 1 FROM media WHERE taken_at_home IS NULL AND taken_at IS NOT NULL LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("catalog: check home times: %w", err)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: fill home times: %w", err)
	}
	defer tx.Rollback()
	_, loc, err := homeZone(ctx, tx)
	if err != nil {
		return err
	}
	type row struct {
		id, wall int64
		off      sql.NullInt64
		src      sql.NullString
		source   string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, taken_at, taken_at_offset, taken_at_offset_source, taken_at_source
		FROM media WHERE taken_at_home IS NULL AND taken_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("catalog: fill home times: %w", err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.wall, &r.off, &r.src, &r.source); err != nil {
			rows.Close()
			return fmt.Errorf("catalog: fill home times: %w", err)
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("catalog: fill home times: %w", err)
	}
	up, err := tx.PrepareContext(ctx, `UPDATE media SET taken_at = ?, taken_at_offset = ?,
		taken_at_offset_source = ?, taken_at_home = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("catalog: fill home times: %w", err)
	}
	defer up.Close()
	for _, r := range all {
		src := r.src.String
		if !r.off.Valid {
			src = ""
		} else if src == "" {
			src = DefaultOffsetSource(r.source, true)
		}
		wall, off, home := expressTime(r.wall, r.off, src, loc)
		var srcArg any
		if src != "" {
			srcArg = src
		}
		if _, err := up.ExecContext(ctx, wall, off, srcArg, home, r.id); err != nil {
			return fmt.Errorf("catalog: fill home times: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: fill home times: %w", err)
	}
	d.logger.Info("home times filled", "path", d.path, "rows", len(all))
	return nil
}
