package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Media is one catalog row.
type Media struct {
	ID                                        int64
	Hash                                      uint64
	Size                                      int64 // 0 = unknown
	RelPath                                   string
	FileType                                  string
	Ext                                       string
	TakenAt                                   time.Time // zero = unknown; wall-clock, see package doc
	OffsetKnown                               bool
	TakenAtSource                             string
	Width, Height                             int // 0 = unknown
	Orientation                               int // 0 = unknown
	DurationMS                                int64
	Lat, Lon                                  float64
	HasGPS                                    bool
	Altitude                                  float64
	HasAltitude                               bool
	CameraMake, CameraModel, CameraType, Lens string
	OriginalPath                              string
	OriginalMTime                             time.Time // zero = unknown
	ImportedAt                                time.Time
	ImportSessionID                           int64 // 0 = none
	Rating                                    int
	DeletedAt                                 time.Time // zero = not deleted
}

// Summary holds aggregate statistics over the catalog.
type Summary struct {
	Total, Images, Raw, Videos, WithGPS, Deleted int64
	TotalBytes                                   int64
	Earliest, Latest                             time.Time // zero when no dated media
	UniqueModels                                 int64     // distinct non-empty camera_model
}

// ListOptions selects rows for List.
type ListOptions struct {
	FileType       string // "" = all
	Limit          int    // 0 = no limit
	Offset         int
	IncludeDeleted bool
	// OrderBy is "" (= "taken_at DESC, id DESC") or one of "taken_at",
	// "taken_at DESC", "id", "id DESC", "rel_path".
	OrderBy string
}

// SessionStats are the counters recorded when an import session finishes.
type SessionStats struct {
	Imported, Existing, Duplicates, SkippedSmall, NonMedia, Errors int64
}

// orderBy maps the accepted ListOptions.OrderBy values to SQL. User strings
// are never interpolated; only values from this table are.
func orderBy(s string) (string, error) {
	switch s {
	case "", "taken_at DESC":
		return "taken_at DESC, id DESC", nil
	case "taken_at":
		return "taken_at ASC, id ASC", nil
	case "id":
		return "id ASC", nil
	case "id DESC":
		return "id DESC", nil
	case "rel_path":
		return "rel_path ASC, id ASC", nil
	}
	return "", fmt.Errorf("catalog: unsupported OrderBy %q", s)
}

const mediaColumns = `id, hash, size, rel_path, file_type, ext, taken_at, taken_at_offset,
	taken_at_source, width, height, orientation, duration_ms, lat, lon, altitude,
	camera_make, camera_model, camera_type, lens, original_path, original_mtime,
	imported_at, import_session_id, rating, deleted_at`

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// encodeTakenAt converts a wall-clock time to the (taken_at, taken_at_offset)
// column values. A zero time yields (nil, nil).
func encodeTakenAt(t time.Time, offsetKnown bool) (wall, offset any) {
	if t.IsZero() {
		return nil, nil
	}
	wall = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC).Unix()
	if offsetKnown {
		_, off := t.Zone()
		offset = int64(off)
	}
	return wall, offset
}

// decodeTakenAt is the inverse of encodeTakenAt.
func decodeTakenAt(wall, offset sql.NullInt64) (time.Time, bool) {
	if !wall.Valid {
		return time.Time{}, false
	}
	t := time.Unix(wall.Int64, 0).UTC()
	if !offset.Valid {
		return t, false
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0,
		time.FixedZone("", int(offset.Int64))), true
}

func unixOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func fromUnix(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).UTC()
}

func nonZeroInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func takenAtSource(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// InsertMedia inserts m inside tx with ON CONFLICT(hash) DO NOTHING.
// Returns inserted=false when the hash already existed. Sets m.ID on success.
// Empty TakenAtSource is stored as "none". A zero ImportedAt is stored as the
// current time. tx may be nil to run outside a transaction.
func (d *DB) InsertMedia(ctx context.Context, tx *sql.Tx, m *Media) (inserted bool, err error) {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	wall, off := encodeTakenAt(m.TakenAt, m.OffsetKnown)
	var lat, lon, alt any
	if m.HasGPS {
		lat, lon = m.Lat, m.Lon
	}
	if m.HasAltitude {
		alt = m.Altitude
	}
	importedAt := m.ImportedAt
	if importedAt.IsZero() {
		importedAt = time.Now()
	}
	res, err := ex.ExecContext(ctx, `INSERT INTO media (
		hash, size, rel_path, file_type, ext, taken_at, taken_at_offset, taken_at_source,
		width, height, orientation, duration_ms, lat, lon, altitude,
		camera_make, camera_model, camera_type, lens, original_path, original_mtime,
		imported_at, import_session_id, rating, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO NOTHING`,
		int64(m.Hash), nonZeroInt(m.Size), m.RelPath, m.FileType, m.Ext, wall, off, takenAtSource(m.TakenAtSource),
		nonZeroInt(int64(m.Width)), nonZeroInt(int64(m.Height)), nonZeroInt(int64(m.Orientation)), nonZeroInt(m.DurationMS),
		lat, lon, alt,
		m.CameraMake, m.CameraModel, m.CameraType, m.Lens, m.OriginalPath, unixOrNil(m.OriginalMTime),
		importedAt.Unix(), nonZeroInt(m.ImportSessionID), m.Rating, unixOrNil(m.DeletedAt))
	if err != nil {
		return false, fmt.Errorf("catalog: insert %s: %w", m.RelPath, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("catalog: insert %s: %w", m.RelPath, err)
	}
	if n == 0 {
		return false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return true, fmt.Errorf("catalog: insert %s: %w", m.RelPath, err)
	}
	m.ID = id
	return true, nil
}

// LookupHash returns the rel_path of the media with this hash.
func (d *DB) LookupHash(ctx context.Context, hash uint64) (relPath string, found bool, err error) {
	err = d.QueryRowContext(ctx, "SELECT rel_path FROM media WHERE hash = ?", int64(hash)).Scan(&relPath)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("catalog: lookup hash: %w", err)
	}
	return relPath, true, nil
}

// RelPathTaken reports whether any row has this rel_path, compared case-insensitively.
func (d *DB) RelPathTaken(ctx context.Context, rel string) (bool, error) {
	var one int
	err := d.QueryRowContext(ctx, "SELECT 1 FROM media WHERE rel_path = ? COLLATE NOCASE LIMIT 1", rel).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("catalog: rel_path lookup: %w", err)
	}
	return true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMedia(r rowScanner) (Media, error) {
	var (
		m                                       Media
		hash                                    int64
		size, wall, off, w, h, orient, dur      sql.NullInt64
		lat, lon, alt                           sql.NullFloat64
		mtime, importedAt, sessionID, deletedAt sql.NullInt64
	)
	err := r.Scan(&m.ID, &hash, &size, &m.RelPath, &m.FileType, &m.Ext, &wall, &off,
		&m.TakenAtSource, &w, &h, &orient, &dur, &lat, &lon, &alt,
		&m.CameraMake, &m.CameraModel, &m.CameraType, &m.Lens, &m.OriginalPath, &mtime,
		&importedAt, &sessionID, &m.Rating, &deletedAt)
	if err != nil {
		return Media{}, err
	}
	m.Hash = uint64(hash)
	m.Size = size.Int64
	m.TakenAt, m.OffsetKnown = decodeTakenAt(wall, off)
	m.Width, m.Height, m.Orientation = int(w.Int64), int(h.Int64), int(orient.Int64)
	m.DurationMS = dur.Int64
	if lat.Valid && lon.Valid {
		m.Lat, m.Lon, m.HasGPS = lat.Float64, lon.Float64, true
	}
	if alt.Valid {
		m.Altitude, m.HasAltitude = alt.Float64, true
	}
	m.OriginalMTime = fromUnix(mtime)
	m.ImportedAt = fromUnix(importedAt)
	m.ImportSessionID = sessionID.Int64
	m.DeletedAt = fromUnix(deletedAt)
	return m, nil
}

// GetMedia returns one row by id (sql.ErrNoRows when missing).
func (d *DB) GetMedia(ctx context.Context, id int64) (Media, error) {
	row := d.QueryRowContext(ctx, "SELECT "+mediaColumns+" FROM media WHERE id = ?", id)
	m, err := scanMedia(row)
	if err == sql.ErrNoRows {
		return Media{}, err
	}
	if err != nil {
		return Media{}, fmt.Errorf("catalog: get media %d: %w", id, err)
	}
	return m, nil
}

// List returns rows matching opts.
func (d *DB) List(ctx context.Context, opts ListOptions) ([]Media, error) {
	order, err := orderBy(opts.OrderBy)
	if err != nil {
		return nil, err
	}
	var (
		where []string
		args  []any
	)
	if !opts.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if opts.FileType != "" {
		where = append(where, "file_type = ?")
		args = append(args, opts.FileType)
	}
	q := "SELECT " + mediaColumns + " FROM media"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY " + order
	if opts.Limit > 0 || opts.Offset > 0 {
		limit := opts.Limit
		if limit <= 0 {
			limit = -1
		}
		q += " LIMIT ? OFFSET ?"
		args = append(args, limit, max(opts.Offset, 0))
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: list: %w", err)
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("catalog: list: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: list: %w", err)
	}
	return out, nil
}

// UpdateMetadata rewrites the metadata columns of row id from m: taken_at,
// taken_at_offset, taken_at_source, width, height, orientation, duration_ms,
// lat, lon, altitude, camera_make, camera_model, camera_type, lens.
// tx may be nil to run outside a transaction. Returns sql.ErrNoRows when no
// row has this id.
func (d *DB) UpdateMetadata(ctx context.Context, tx *sql.Tx, id int64, m *Media) error {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	wall, off := encodeTakenAt(m.TakenAt, m.OffsetKnown)
	var lat, lon, alt any
	if m.HasGPS {
		lat, lon = m.Lat, m.Lon
	}
	if m.HasAltitude {
		alt = m.Altitude
	}
	res, err := ex.ExecContext(ctx, `UPDATE media SET
		taken_at = ?, taken_at_offset = ?, taken_at_source = ?, width = ?, height = ?,
		orientation = ?, duration_ms = ?, lat = ?, lon = ?, altitude = ?,
		camera_make = ?, camera_model = ?, camera_type = ?, lens = ?
		WHERE id = ?`,
		wall, off, takenAtSource(m.TakenAtSource), nonZeroInt(int64(m.Width)), nonZeroInt(int64(m.Height)),
		nonZeroInt(int64(m.Orientation)), nonZeroInt(m.DurationMS), lat, lon, alt,
		m.CameraMake, m.CameraModel, m.CameraType, m.Lens, id)
	if err != nil {
		return fmt.Errorf("catalog: update metadata %d: %w", id, err)
	}
	return checkOneRow(res, id)
}

// UpdateSize sets size for row id. tx may be nil to run outside a
// transaction. Returns sql.ErrNoRows when no row has this id.
func (d *DB) UpdateSize(ctx context.Context, tx *sql.Tx, id, size int64) error {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	res, err := ex.ExecContext(ctx, "UPDATE media SET size = ? WHERE id = ?", size, id)
	if err != nil {
		return fmt.Errorf("catalog: update size %d: %w", id, err)
	}
	return checkOneRow(res, id)
}

func checkOneRow(res sql.Result, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("catalog: update %d: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Summary returns aggregate statistics. Counts exclude soft-deleted rows
// except Deleted.
func (d *DB) Summary(ctx context.Context) (Summary, error) {
	var (
		s                Summary
		earliest, latest sql.NullInt64
	)
	err := d.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(deleted_at IS NULL), 0),
		COALESCE(SUM(deleted_at IS NULL AND file_type = 'image'), 0),
		COALESCE(SUM(deleted_at IS NULL AND file_type = 'image_raw'), 0),
		COALESCE(SUM(deleted_at IS NULL AND file_type = 'video'), 0),
		COALESCE(SUM(deleted_at IS NULL AND lat IS NOT NULL), 0),
		COALESCE(SUM(deleted_at IS NOT NULL), 0),
		COALESCE(SUM(CASE WHEN deleted_at IS NULL THEN size END), 0),
		MIN(CASE WHEN deleted_at IS NULL THEN taken_at END),
		MAX(CASE WHEN deleted_at IS NULL THEN taken_at END),
		COUNT(DISTINCT CASE WHEN deleted_at IS NULL AND camera_model <> '' THEN camera_model END)
		FROM media`).Scan(&s.Total, &s.Images, &s.Raw, &s.Videos, &s.WithGPS, &s.Deleted,
		&s.TotalBytes, &earliest, &latest, &s.UniqueModels)
	if err != nil {
		return Summary{}, fmt.Errorf("catalog: summary: %w", err)
	}
	s.Earliest, _ = decodeTakenAt(earliest, sql.NullInt64{})
	s.Latest, _ = decodeTakenAt(latest, sql.NullInt64{})
	return s, nil
}

// BeginSession records the start of an import session and returns its id.
func (d *DB) BeginSession(ctx context.Context, source, mode string) (int64, error) {
	res, err := d.ExecContext(ctx,
		"INSERT INTO import_sessions (started_at, source, mode) VALUES (?, ?, ?)",
		time.Now().Unix(), source, mode)
	if err != nil {
		return 0, fmt.Errorf("catalog: begin session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("catalog: begin session: %w", err)
	}
	return id, nil
}

// FinishSession records the end time and counters of session id.
func (d *DB) FinishSession(ctx context.Context, id int64, s SessionStats) error {
	res, err := d.ExecContext(ctx, `UPDATE import_sessions SET finished_at = ?,
		imported = ?, existing = ?, duplicates = ?, skipped_small = ?, non_media = ?, errors = ?
		WHERE id = ?`,
		time.Now().Unix(), s.Imported, s.Existing, s.Duplicates, s.SkippedSmall, s.NonMedia, s.Errors, id)
	if err != nil {
		return fmt.Errorf("catalog: finish session %d: %w", id, err)
	}
	return checkOneRow(res, id)
}
