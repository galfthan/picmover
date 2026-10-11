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
	Size                                      int64 // bytes; required (> 0)
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
	PHash                                     uint64    // perceptual hash; 0 = none
	ThumbState                                string    // pending | ok | unsupported | error | stale
	ThumbError                                string
	ThumbAt                                   time.Time // zero = never attempted
}

// Thumbnail states stored in thumb_state.
const (
	ThumbStatePending     = "pending"
	ThumbStateOK          = "ok"
	ThumbStateUnsupported = "unsupported"
	ThumbStateError       = "error"
	// ThumbStateStale marks cached renditions that no longer match the
	// row (its orientation changed); they are regenerated with force.
	ThumbStateStale = "stale"
)

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
	imported_at, import_session_id, rating, deleted_at,
	phash, thumb_state, thumb_error, thumb_at`

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

func thumbState(s string) string {
	if s == "" {
		return ThumbStatePending
	}
	return s
}

func takenAtSource(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// InsertMedia inserts m inside tx with ON CONFLICT(hash) DO NOTHING.
// Returns inserted=false when the hash already existed. Sets m.ID on success.
// Size must be > 0. Empty TakenAtSource is stored as "none", empty ThumbState
// as "pending", and a zero ImportedAt as the current time. tx may be nil to
// run outside a transaction.
func (d *DB) InsertMedia(ctx context.Context, tx *sql.Tx, m *Media) (inserted bool, err error) {
	if m.Size <= 0 {
		return false, fmt.Errorf("catalog: insert %s: size %d, must be > 0", m.RelPath, m.Size)
	}
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
		imported_at, import_session_id, rating, deleted_at,
		phash, thumb_state, thumb_error, thumb_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO NOTHING`,
		int64(m.Hash), m.Size, m.RelPath, m.FileType, m.Ext, wall, off, takenAtSource(m.TakenAtSource),
		nonZeroInt(int64(m.Width)), nonZeroInt(int64(m.Height)), nonZeroInt(int64(m.Orientation)), nonZeroInt(m.DurationMS),
		lat, lon, alt,
		m.CameraMake, m.CameraModel, m.CameraType, m.Lens, m.OriginalPath, unixOrNil(m.OriginalMTime),
		importedAt.Unix(), nonZeroInt(m.ImportSessionID), m.Rating, unixOrNil(m.DeletedAt),
		nonZeroInt(int64(m.PHash)), thumbState(m.ThumbState), m.ThumbError, unixOrNil(m.ThumbAt))
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
		hash, size                              int64
		wall, off, w, h, orient, dur            sql.NullInt64
		phash, thumbAt                          sql.NullInt64
		lat, lon, alt                           sql.NullFloat64
		mtime, importedAt, sessionID, deletedAt sql.NullInt64
	)
	err := r.Scan(&m.ID, &hash, &size, &m.RelPath, &m.FileType, &m.Ext, &wall, &off,
		&m.TakenAtSource, &w, &h, &orient, &dur, &lat, &lon, &alt,
		&m.CameraMake, &m.CameraModel, &m.CameraType, &m.Lens, &m.OriginalPath, &mtime,
		&importedAt, &sessionID, &m.Rating, &deletedAt,
		&phash, &m.ThumbState, &m.ThumbError, &thumbAt)
	if err != nil {
		return Media{}, err
	}
	m.Hash = uint64(hash)
	m.Size = size
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
	m.PHash = uint64(phash.Int64)
	m.ThumbAt = fromUnix(thumbAt)
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
	out, err := d.queryMedia(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: list: %w", err)
	}
	return out, nil
}

func (d *DB) queryMedia(ctx context.Context, q string, args ...any) ([]Media, error) {
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListThumbPending returns up to limit rows (all when limit <= 0) with
// thumb_state 'pending' that are not soft-deleted, ordered by id. When
// sessionID > 0 only rows of that import session are returned.
func (d *DB) ListThumbPending(ctx context.Context, limit int, sessionID int64) ([]Media, error) {
	q := "SELECT " + mediaColumns + " FROM media WHERE thumb_state = 'pending' AND deleted_at IS NULL"
	var args []any
	if sessionID > 0 {
		q += " AND import_session_id = ?"
		args = append(args, sessionID)
	}
	q += " ORDER BY id"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	out, err := d.queryMedia(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: list thumb pending: %w", err)
	}
	return out, nil
}

// ListForThumbs returns up to limit rows (all when limit <= 0) with
// id > afterID that are not soft-deleted, ordered by id, for paging through
// the rows a thumbnail pass works on: rows with thumb_state 'pending',
// 'stale' or 'error' (failed rows are retried), or every row when all is
// set. When sessionID > 0 only rows of that import
// session are returned. Paging by id (pass the last id seen as afterID)
// stays correct while the pass changes the thumb state of earlier rows.
func (d *DB) ListForThumbs(ctx context.Context, afterID int64, limit int, sessionID int64, all bool) ([]Media, error) {
	q := "SELECT " + mediaColumns + " FROM media WHERE id > ? AND deleted_at IS NULL"
	args := []any{afterID}
	if !all {
		q += " AND thumb_state IN ('pending', 'stale', 'error')"
	}
	if sessionID > 0 {
		q += " AND import_session_id = ?"
		args = append(args, sessionID)
	}
	q += " ORDER BY id"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	out, err := d.queryMedia(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: list for thumbs: %w", err)
	}
	return out, nil
}

// SetThumb records the outcome of a thumbnail attempt for row id: thumb_state
// = state (one of the ThumbState* constants), thumb_error = msg, thumb_at = now and
// phash (NULL when 0). width and height are set from w and h only when both
// are > 0 and the row's value is NULL or 0. tx may be nil to run outside a
// transaction. Returns sql.ErrNoRows when no row has this id.
func (d *DB) SetThumb(ctx context.Context, tx *sql.Tx, id int64, state, msg string, phash uint64, w, h int) error {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	var nw, nh any
	if w > 0 && h > 0 {
		nw, nh = int64(w), int64(h)
	}
	res, err := ex.ExecContext(ctx, `UPDATE media SET
		thumb_state = ?, thumb_error = ?, thumb_at = ?, phash = ?,
		width = CASE WHEN COALESCE(width, 0) = 0 THEN COALESCE(?, width) ELSE width END,
		height = CASE WHEN COALESCE(height, 0) = 0 THEN COALESCE(?, height) ELSE height END
		WHERE id = ?`,
		state, msg, time.Now().Unix(), nonZeroInt(int64(phash)), nw, nh, id)
	if err != nil {
		return fmt.Errorf("catalog: set thumb %d: %w", id, err)
	}
	return checkOneRow(res, id)
}

// MarkThumbStale sets thumb_state = 'stale' for row id, so the next
// thumbnail pass regenerates its cached renditions (for example after its
// orientation changed). Every state becomes 'stale': even a 'pending' row
// may have renditions in the cache under its hash (after a rebuild), which
// a non-forced pass would reuse. tx may be nil to run outside a
// transaction. Returns sql.ErrNoRows when no row has this id.
func (d *DB) MarkThumbStale(ctx context.Context, tx *sql.Tx, id int64) error {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	res, err := ex.ExecContext(ctx, "UPDATE media SET thumb_state = 'stale' WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("catalog: mark thumb stale %d: %w", id, err)
	}
	return checkOneRow(res, id)
}

// ThumbStats counts non-deleted rows per thumb_state; pending counts
// 'pending' and 'stale' (both wait for a thumbnail pass), failed counts
// 'error'.
func (d *DB) ThumbStats(ctx context.Context) (pending, ok, unsupported, failed int64, err error) {
	err = d.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(thumb_state IN ('pending', 'stale')), 0),
		COALESCE(SUM(thumb_state = 'ok'), 0),
		COALESCE(SUM(thumb_state = 'unsupported'), 0),
		COALESCE(SUM(thumb_state = 'error'), 0)
		FROM media WHERE deleted_at IS NULL`).Scan(&pending, &ok, &unsupported, &failed)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("catalog: thumb stats: %w", err)
	}
	return pending, ok, unsupported, failed, nil
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

// UpdateSize sets size for row id (for example after a file was rewritten).
// size must be > 0. tx may be nil to run outside a transaction. Returns
// sql.ErrNoRows when no row has this id.
func (d *DB) UpdateSize(ctx context.Context, tx *sql.Tx, id, size int64) error {
	var ex execer = d.DB
	if tx != nil {
		ex = tx
	}
	if size <= 0 {
		return fmt.Errorf("catalog: update size %d: size %d, must be > 0", id, size)
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

// Session is a recorded import session with its counters and the content
// hashes of its rows.
type Session struct {
	ID                    int64
	StartedAt, FinishedAt time.Time // FinishedAt zero: never finished
	Source, Mode          string
	SessionStats
	// Hashes are the hashes of the rows recorded by the session (trashed
	// rows included), in row id order.
	Hashes []uint64
	// Newest is the newest live row among them (taken_at DESC, id DESC,
	// undated last, as NewestOf); 0 when none is live.
	Newest uint64
}

// tableColumns returns the column names of table (none when it does not
// exist).
func tableColumns(ctx context.Context, q *sql.DB, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[strings.ToLower(n)] = true
	}
	return out, rows.Err()
}

// colOr is the column name when cols has it, else fallback (an SQL
// expression).
func colOr(cols map[string]bool, name, fallback string) string {
	if cols[name] {
		return name
	}
	return fallback
}

// sessionRows are the rows of one session: their hashes in row id order
// and the newest live one.
type sessionRows struct {
	hashes []uint64
	newest uint64

	bestDated bool
	bestTaken int64
}

// add takes the next row (rows come in id order, so on a tie the later
// one is newer).
func (r *sessionRows) add(h int64, taken sql.NullInt64, live bool) {
	r.hashes = append(r.hashes, uint64(h))
	if !live {
		return
	}
	switch {
	case r.newest == 0,
		taken.Valid && (!r.bestDated || taken.Int64 >= r.bestTaken),
		!taken.Valid && !r.bestDated:
		r.newest, r.bestDated, r.bestTaken = uint64(h), taken.Valid, taken.Int64
	}
}

// readSessionRows returns the rows of the sessions (all of them when
// session is 0), by session id. The query uses only the columns every
// catalog version with sessions has (hash, import_session_id) and
// tolerates missing taken_at and deleted_at.
func readSessionRows(ctx context.Context, q *sql.DB, session int64) (map[int64]*sessionRows, error) {
	cols, err := tableColumns(ctx, q, "media")
	if err != nil {
		return nil, err
	}
	out := map[int64]*sessionRows{}
	if !cols["hash"] || !cols["import_session_id"] {
		return out, nil
	}
	where, args := "import_session_id IS NOT NULL", []any{}
	if session != 0 {
		where, args = "import_session_id = ?", []any{session}
	}
	rows, err := q.QueryContext(ctx, `SELECT import_session_id, hash, `+colOr(cols, "taken_at", "NULL")+`, `+
		colOr(cols, "deleted_at", "NULL")+` IS NULL FROM media WHERE `+where+` ORDER BY `+colOr(cols, "id", "rowid"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			sid, h int64
			taken  sql.NullInt64
			live   bool
		)
		if err := rows.Scan(&sid, &h, &taken, &live); err != nil {
			return nil, err
		}
		r := out[sid]
		if r == nil {
			r = &sessionRows{}
			out[sid] = r
		}
		r.add(h, taken, live)
	}
	return out, rows.Err()
}

// Sessions returns every import session, oldest first, with the hashes
// of its rows (the import history backfill of user.db). It reads the
// columns schema versions 2 and 3 share, so it also works on an older
// catalog (SessionsFile); a missing counter column reads as 0 and a
// catalog without import_sessions has no sessions.
func (d *DB) Sessions(ctx context.Context) ([]Session, error) {
	cols, err := tableColumns(ctx, d.DB, "import_sessions")
	if err != nil {
		return nil, fmt.Errorf("catalog: sessions: %w", err)
	}
	out := []Session{}
	if !cols["id"] || !cols["started_at"] {
		return out, nil
	}
	sel := []string{"id", "started_at", colOr(cols, "finished_at", "NULL"), colOr(cols, "source", "''"),
		colOr(cols, "mode", "''")}
	for _, c := range []string{"imported", "existing", "duplicates", "skipped_small", "non_media", "errors"} {
		sel = append(sel, colOr(cols, c, "0"))
	}
	rows, err := d.QueryContext(ctx, `SELECT `+strings.Join(sel, ", ")+` FROM import_sessions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("catalog: sessions: %w", err)
	}
	for rows.Next() {
		var (
			s        Session
			started  int64
			finished sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &started, &finished, &s.Source, &s.Mode, &s.Imported, &s.Existing,
			&s.Duplicates, &s.SkippedSmall, &s.NonMedia, &s.Errors); err != nil {
			rows.Close()
			return nil, fmt.Errorf("catalog: sessions: %w", err)
		}
		s.StartedAt, s.FinishedAt = time.Unix(started, 0).UTC(), fromUnix(finished)
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: sessions: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}
	byID, err := readSessionRows(ctx, d.DB, 0)
	if err != nil {
		return nil, fmt.Errorf("catalog: session rows: %w", err)
	}
	for i := range out {
		if r := byID[out[i].ID]; r != nil {
			out[i].Hashes, out[i].Newest = r.hashes, r.newest
		}
	}
	return out, nil
}

// SessionHashes returns the hashes of the rows of session (trashed ones
// included, in row id order) and the newest live one (0: none): the items
// of an import whose process died before it recorded them.
func (d *DB) SessionHashes(ctx context.Context, session int64) (hashes []uint64, newest uint64, err error) {
	byID, err := readSessionRows(ctx, d.DB, session)
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: session rows: %w", err)
	}
	if r := byID[session]; r != nil {
		return r.hashes, r.newest, nil
	}
	return []uint64{}, 0, nil
}
