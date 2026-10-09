package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"picmover/internal/geo"
)

// legacyRow is one row of the v1 media table.
type legacyRow struct {
	id          int64
	hash        sql.NullInt64
	origPath    sql.NullString
	newPath     sql.NullString
	dateTaken   any
	fileType    sql.NullString
	location    sql.NullString
	cameraModel sql.NullString
	cameraMake  sql.NullString
	cameraType  sql.NullString
	resolution  sql.NullString
}

// migrateLegacy converts a v1 database to schema v2 after writing a backup
// next to it with VACUUM INTO.
func (d *DB) migrateLegacy(ctx context.Context) error {
	absPath, err := filepath.Abs(d.path)
	if err != nil {
		return fmt.Errorf("catalog: resolve %s: %w", d.path, err)
	}
	backup, err := d.writeBackup(ctx, absPath)
	if err != nil {
		return err
	}
	d.logger.Info("v1 catalog backed up", "backup", backup)

	root := normSlashes(filepath.Dir(absPath))
	now := time.Now().Unix()

	// The transaction is IMMEDIATE (_txlock=immediate), so it holds the write
	// lock from here on. Another process may have migrated the database
	// between the pre-check and now; if so, there is nothing left to do.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: migrate v1: begin: %w", err)
	}
	defer tx.Rollback()

	still, err := stillLegacy(ctx, tx)
	if err != nil {
		return err
	}
	if !still {
		tx.Rollback()
		if err := os.Remove(backup); err != nil {
			d.logger.Warn("could not remove redundant backup", "backup", backup, "err", err)
		}
		d.logger.Info("v1 catalog already migrated by another process", "path", d.path)
		return nil
	}

	if _, err := tx.ExecContext(ctx, "ALTER TABLE media RENAME TO media_v1"); err != nil {
		return fmt.Errorf("catalog: migrate v1: rename: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schemaV2); err != nil {
		return fmt.Errorf("catalog: migrate v1: create schema: %w", err)
	}

	legacy, err := readLegacyRows(ctx, tx)
	if err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO media (
		id, hash, rel_path, file_type, ext, taken_at, taken_at_offset, taken_at_source,
		width, height, lat, lon, altitude,
		camera_make, camera_model, camera_type, original_path, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("catalog: migrate v1: prepare: %w", err)
	}
	defer stmt.Close()

	var migrated, unresolved, skipped int
	for _, r := range legacy {
		if !r.hash.Valid {
			d.logger.Warn("v1 row without hash skipped", "id", r.id, "path", r.newPath.String)
			skipped++
			continue
		}
		rel, ok := legacyRelPath(r.newPath.String, root)
		if !ok {
			d.logger.Warn("v1 path not under library root, kept as is", "id", r.id, "path", rel)
			unresolved++
		}
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(rel), "."))

		takenAt, takenOff, source := legacyTakenAt(r.dateTaken, r.fileType.String)

		var lat, lon, alt any
		if la, lo, al, hasAlt, ok := legacyLocation(r.location.String); ok {
			lat, lon = la, lo
			if hasAlt {
				alt = al
			}
		}
		var width, height any
		if w, h, ok := legacyResolution(r.resolution.String); ok {
			width, height = w, h
		}

		res, err := stmt.ExecContext(ctx, r.id, r.hash.Int64, rel, r.fileType.String, ext, takenAt, takenOff, source,
			width, height, lat, lon, alt,
			r.cameraMake.String, r.cameraModel.String, r.cameraType.String, r.origPath.String, now)
		if err != nil {
			return fmt.Errorf("catalog: migrate v1 row %d: %w", r.id, err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			migrated++
		} else {
			skipped++
		}
	}

	if _, err := tx.ExecContext(ctx, "DROP TABLE media_v1"); err != nil {
		return fmt.Errorf("catalog: migrate v1: drop media_v1: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("catalog: migrate v1: set user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: migrate v1: commit: %w", err)
	}
	d.logger.Info("v1 catalog migrated", "rows", migrated, "unresolved_paths", unresolved, "skipped", skipped)
	return nil
}

// stillLegacy re-checks inside tx that the database is still an unmigrated
// v1 catalog.
func stillLegacy(ctx context.Context, tx *sql.Tx) (bool, error) {
	var v int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return false, fmt.Errorf("catalog: migrate v1: read user_version: %w", err)
	}
	if v != 0 {
		return false, nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('media') WHERE name = 'new_path'").Scan(&n); err != nil {
		return false, fmt.Errorf("catalog: migrate v1: inspect media table: %w", err)
	}
	return n > 0, nil
}

// writeBackup copies the database with VACUUM INTO to a unique temporary
// file and then moves it to a backup name that does not overwrite an
// existing file. On error no file is left behind. It returns the final
// backup path.
func (d *DB) writeBackup(ctx context.Context, absPath string) (string, error) {
	backup, err := backupPath(absPath)
	if err != nil {
		return "", err
	}
	// VACUUM INTO accepts an existing empty file; creating it with a unique
	// name keeps concurrent migrations from writing to the same temp file.
	f, err := os.CreateTemp(filepath.Dir(backup), filepath.Base(backup)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("catalog: backup v1 database: %w", err)
	}
	tmp := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("catalog: backup v1 database: %w", err)
	}
	if _, err := d.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("catalog: backup v1 database to %s: %w", tmp, err)
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
		os.Remove(tmp)
		if err == nil {
			err = errors.New("backup is empty")
		}
		return "", fmt.Errorf("catalog: backup v1 database: %w", err)
	}
	for {
		// Link fails if the target exists, so a concurrent backup with the
		// same timestamp is never overwritten.
		err := os.Link(tmp, backup)
		if err == nil {
			os.Remove(tmp)
			return backup, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			// Hard links unsupported: fall back to a plain rename.
			if err := os.Rename(tmp, backup); err != nil {
				os.Remove(tmp)
				return "", fmt.Errorf("catalog: backup v1 database: %w", err)
			}
			return backup, nil
		}
		if backup, err = backupPath(absPath); err != nil {
			os.Remove(tmp)
			return "", err
		}
	}
}

// backupPath returns <dir>/<base>.v1-<YYYYMMDD-HHMMSS>.bak, adding a counter
// if that file already exists.
func backupPath(dbPath string) (string, error) {
	base := fmt.Sprintf("%s.v1-%s", dbPath, time.Now().Format("20060102-150405"))
	p := base + ".bak"
	for i := 1; ; i++ {
		_, err := os.Stat(p)
		if os.IsNotExist(err) {
			return p, nil
		}
		if err != nil {
			return "", fmt.Errorf("catalog: backup path %s: %w", p, err)
		}
		p = fmt.Sprintf("%s-%d.bak", base, i)
	}
}

// readLegacyRows reads every v1 row into memory and closes the cursor.
func readLegacyRows(ctx context.Context, tx *sql.Tx) ([]legacyRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, hash, original_path, new_path, date_taken,
		file_type, location, camera_model, camera_make, camera_type, resolution
		FROM media_v1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("catalog: migrate v1: read: %w", err)
	}
	defer rows.Close()
	var out []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.hash, &r.origPath, &r.newPath, &r.dateTaken,
			&r.fileType, &r.location, &r.cameraModel, &r.cameraMake, &r.cameraType, &r.resolution); err != nil {
			return nil, fmt.Errorf("catalog: migrate v1: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: migrate v1: read: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("catalog: migrate v1: close cursor: %w", err)
	}
	return out, nil
}

func normSlashes(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), `\`, "/")
}

// legacyRelPath converts a v1 absolute new_path to a library-relative path.
// ok is false when the path could not be resolved and is returned normalised
// but otherwise unchanged.
func legacyRelPath(newPath, root string) (rel string, ok bool) {
	p := strings.ReplaceAll(newPath, `\`, "/")
	r := strings.TrimRight(root, "/")
	if r != "" && len(p) > len(r) && p[len(r)] == '/' && strings.EqualFold(p[:len(r)], r) {
		return strings.TrimLeft(p[len(r):], "/"), true
	}
	segs := strings.Split(p, "/")
	if n := len(segs); n >= 4 {
		switch segs[n-4] {
		case "image", "image_raw", "video":
			return strings.Join(segs[n-4:], "/"), true
		}
	}
	return p, false
}

// legacyTimeLayouts lists the layouts with a zone first; the first
// legacyZonedLayouts entries carry one.
const legacyZonedLayouts = 6

var legacyTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02",
}

// legacyTakenAt converts a v1 date_taken value to the taken_at,
// taken_at_offset and taken_at_source column values:
//   - video at +00:00: v1 stored a true UTC instant from ffprobe; keep it
//     with offset 0 (known), source "legacy_utc".
//   - any type at a non-zero offset: v1 fell back to the file mtime in the
//     machine's local zone; keep the wall clock and that offset, "legacy".
//   - image/image_raw at +00:00, or no zone at all: the offset is unknown,
//     "legacy".
//   - zero/unparseable: NULL, "none".
func legacyTakenAt(v any, fileType string) (wall, offset any, source string) {
	t, off, hasZone, ok := legacyTime(v)
	if !ok {
		return nil, nil, "none"
	}
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.FixedZone("", off))
	switch {
	case !hasZone:
		wall, offset = encodeTakenAt(t, false)
		return wall, offset, "legacy"
	case off != 0:
		wall, offset = encodeTakenAt(t, true)
		return wall, offset, "legacy"
	case fileType == "video":
		wall, offset = encodeTakenAt(t, true)
		return wall, offset, "legacy_utc"
	default:
		wall, offset = encodeTakenAt(t, false)
		return wall, offset, "legacy"
	}
}

// legacyTime interprets a v1 date_taken value. offset is the zone offset in
// seconds east of UTC of the parsed value and hasZone reports whether the
// value carried a zone at all. ok is false for NULL, zero or unparseable
// values.
func legacyTime(v any) (t time.Time, offset int, hasZone, ok bool) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, 0, false, false
	case time.Time:
		t, hasZone = x, true
	case string:
		t, hasZone = parseLegacyTime(x)
	case []byte:
		t, hasZone = parseLegacyTime(string(x))
	case int64:
		if x == 0 {
			return time.Time{}, 0, false, false
		}
		t, hasZone = time.Unix(x, 0).UTC(), true
	default:
		return time.Time{}, 0, false, false
	}
	if t.IsZero() || t.Year() <= 1 {
		return time.Time{}, 0, false, false
	}
	_, offset = t.Zone()
	return t, offset, hasZone, true
}

// parseLegacyTime parses s with the first matching layout; hasZone reports
// whether that layout carries a zone offset.
func parseLegacyTime(s string) (t time.Time, hasZone bool) {
	s = strings.TrimSpace(s)
	for i, layout := range legacyTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, i < legacyZonedLayouts
		}
	}
	return time.Time{}, false
}

// legacyLocation parses "lat,lon" or ISO6709.
func legacyLocation(s string) (lat, lon, alt float64, hasAlt, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, 0, false, false
	}
	if la, lo, llOK := geo.ParseLatLon(s); llOK {
		lat, lon, ok = la, lo, true
	} else {
		lat, lon, alt, hasAlt, ok = geo.ParseISO6709(s)
	}
	if ok && lat == 0 && lon == 0 {
		// Null Island: v1 wrote 0,0 when there was no GPS fix.
		return 0, 0, 0, false, false
	}
	return lat, lon, alt, hasAlt, ok
}

// legacyResolution parses "WxH".
func legacyResolution(s string) (w, h int, ok bool) {
	ws, hs, found := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	if !found {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(ws))
	h, err2 := strconv.Atoi(strings.TrimSpace(hs))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
