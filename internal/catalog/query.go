package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Filter narrows the browsing queries (Buckets, BucketItems, GeoPoints,
// GeoItems). Zero values mean "no constraint".
type Filter struct {
	FileType       string    // "" | image | image_raw | video
	HasGPS         bool      // only rows with coordinates
	CameraModel    string    // exact match on camera_model
	SessionID      int64     // import_session_id
	From, To       time.Time // wall-clock bounds on taken_at (To exclusive); zero = open
	IncludeDeleted bool
	// Trashed selects the trashed rows (deleted_at set) instead of the
	// live ones; it takes precedence over IncludeDeleted.
	Trashed bool
	// Hashes restricts the rows to these content hashes. nil means no
	// restriction; an empty non-nil slice matches nothing.
	Hashes []uint64
	// Recur restricts dated rows to a day range every year; nil = none.
	// Undated rows never match.
	Recur *Recur
}

// Recur is an inclusive day-of-year range repeated every year, as
// "MM-DD" bounds. From > To wraps the year end (12-30..01-02); From ==
// To is one day. 02-29 is a valid bound.
type Recur struct {
	From, To string
}

// ValidMonthDay reports whether s is "MM-DD" with a month 01-12 and a day
// that exists in that month in a leap year.
func ValidMonthDay(s string) bool {
	if len(s) != 5 || s[2] != '-' {
		return false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	m := int(s[0]-'0')*10 + int(s[1]-'0')
	d := int(s[3]-'0')*10 + int(s[4]-'0')
	if m < 1 || m > 12 || d < 1 {
		return false
	}
	// 2000 is a leap year, so 02-29 exists.
	return d <= time.Date(2000, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// File types stored in file_type.
const (
	FileTypeImage = "image"
	FileTypeRaw   = "image_raw"
	FileTypeVideo = "video"
)

// ValidFileType reports whether s is a file_type value.
func ValidFileType(s string) bool {
	return s == FileTypeImage || s == FileTypeRaw || s == FileTypeVideo
}

// wallSeconds encodes the wall-clock fields of t like taken_at: Unix
// seconds as if t's wall clock were UTC.
func wallSeconds(t time.Time) int64 {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC).Unix()
}

// where returns the SQL conditions of f (joined with AND by the caller) and
// their arguments. Only constant SQL is produced; values are parameters.
func (f Filter) where() ([]string, []any, error) {
	var (
		conds []string
		args  []any
	)
	switch {
	case f.Trashed:
		conds = append(conds, "deleted_at IS NOT NULL")
	case !f.IncludeDeleted:
		conds = append(conds, "deleted_at IS NULL")
	}
	if f.Hashes != nil {
		conds = append(conds, "hash IN (SELECT value FROM json_each(?))")
		args = append(args, hashArray(f.Hashes))
	}
	if r := f.Recur; r != nil {
		if !ValidMonthDay(r.From) || !ValidMonthDay(r.To) {
			return nil, nil, fmt.Errorf("catalog: invalid recurring range %q..%q", r.From, r.To)
		}
		// Inside the dated-range guard: strftime cannot format years
		// beyond 9999, and undated rows never match.
		md := "strftime('%m-%d', taken_at, 'unixepoch')"
		if r.From <= r.To {
			conds = append(conds, datedRange+" AND "+md+" BETWEEN ? AND ?")
		} else {
			conds = append(conds, datedRange+" AND ("+md+" >= ? OR "+md+" <= ?)")
		}
		args = append(args, r.From, r.To)
	}
	if f.FileType != "" {
		if !ValidFileType(f.FileType) {
			return nil, nil, fmt.Errorf("catalog: unknown file type %q", f.FileType)
		}
		conds = append(conds, "file_type = ?")
		args = append(args, f.FileType)
	}
	if f.HasGPS {
		conds = append(conds, "lat IS NOT NULL AND lon IS NOT NULL")
	}
	if f.CameraModel != "" {
		conds = append(conds, "camera_model = ?")
		args = append(args, f.CameraModel)
	}
	if f.SessionID != 0 {
		conds = append(conds, "import_session_id = ?")
		args = append(args, f.SessionID)
	}
	if !f.From.IsZero() {
		conds = append(conds, "taken_at >= ?")
		args = append(args, wallSeconds(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "taken_at < ?")
		args = append(args, wallSeconds(f.To))
	}
	return conds, args, nil
}

func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// The range of taken_at values that the timeline dates: 0001-01-01
// 00:00:00 to 9999-12-31 23:59:59 (wall clock as UTC). strftime cannot
// format years beyond 9999, and years before 1 have no bucket, so rows
// outside the range count as undated in both Buckets and BucketItems.
const (
	minDatedTakenAt = -62135596800 // time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	maxDatedTakenAt = 253402300799 // time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Unix()
)

var (
	datedRange   = fmt.Sprintf("taken_at BETWEEN %d AND %d", minDatedTakenAt, maxDatedTakenAt)
	undatedConds = fmt.Sprintf("(taken_at IS NULL OR taken_at < %d OR taken_at > %d)", minDatedTakenAt, maxDatedTakenAt)
)

// Bucket is one calendar month of dated rows, or the undated rows
// (Year == 0 && Month == 0), which sort last.
type Bucket struct {
	Year, Month int
	Count       int64
}

// Buckets returns the months matching f, newest first, undated last.
// Implemented with one GROUP BY over strftime('%Y-%m', taken_at, 'unixepoch');
// taken_at is wall-clock-as-UTC so no zone conversion is applied. Rows
// without taken_at, or with one outside years 1-9999, are undated.
func (d *DB) Buckets(ctx context.Context, f Filter) ([]Bucket, error) {
	conds, args, err := f.where()
	if err != nil {
		return nil, err
	}
	q := `SELECT CASE WHEN ` + datedRange + ` THEN strftime('%Y-%m', taken_at, 'unixepoch') END AS ym, COUNT(*) FROM media` +
		whereClause(conds) + ` GROUP BY ym ORDER BY ym IS NULL, ym DESC`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: buckets: %w", err)
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var (
			ym sql.NullString
			b  Bucket
		)
		if err := rows.Scan(&ym, &b.Count); err != nil {
			return nil, fmt.Errorf("catalog: buckets: %w", err)
		}
		if ym.Valid {
			y, m, ok := parseYearMonth(ym.String)
			if !ok {
				return nil, fmt.Errorf("catalog: buckets: unexpected month %q", ym.String)
			}
			b.Year, b.Month = y, m
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: buckets: %w", err)
	}
	return out, nil
}

// parseYearMonth parses strftime's "YYYY-MM".
func parseYearMonth(s string) (year, month int, ok bool) {
	ys, ms, found := strings.Cut(s, "-")
	if !found {
		return 0, 0, false
	}
	y, err1 := strconv.Atoi(ys)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || m < 1 || m > 12 {
		return 0, 0, false
	}
	return y, m, true
}

// BucketItems returns the rows of one bucket ordered taken_at DESC, id DESC
// (undated, year == 0 && month == 0: id DESC; that includes taken_at
// outside years 1-9999, as in Buckets). The month range is a
// half-open [first, next-first) pair of Unix wall-clock seconds and the
// query names the media_taken_at index (INDEXED BY), so another index
// matching a filter (file_type, session) is never chosen instead.
func (d *DB) BucketItems(ctx context.Context, f Filter, year, month int) ([]Media, error) {
	q, args, err := bucketItemsQuery(f, year, month)
	if err != nil {
		return nil, err
	}
	out, err := d.queryMedia(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("catalog: bucket items: %w", err)
	}
	return out, nil
}

func bucketItemsQuery(f Filter, year, month int) (string, []any, error) {
	conds, args, err := f.where()
	if err != nil {
		return "", nil, err
	}
	order := "taken_at DESC, id DESC"
	switch {
	case year == 0 && month == 0:
		conds = append(conds, undatedConds)
		order = "id DESC"
	case month >= 1 && month <= 12 && year >= 1 && year <= 9999:
		first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		conds = append(conds, "taken_at >= ? AND taken_at < ?")
		args = append(args, first.Unix(), first.AddDate(0, 1, 0).Unix())
	default:
		return "", nil, fmt.Errorf("catalog: invalid bucket %04d-%02d", year, month)
	}
	q := "SELECT " + mediaColumns + " FROM media INDEXED BY media_taken_at" + whereClause(conds) + " ORDER BY " + order
	return q, args, nil
}

// Camera is one distinct camera with its number of (non-deleted) rows.
type Camera struct {
	Make, Model string
	Count       int64
}

// Cameras lists distinct (camera_make, camera_model) with counts, most
// frequent first (then by model and make, case-insensitively), empty model and soft-deleted
// rows excluded.
func (d *DB) Cameras(ctx context.Context) ([]Camera, error) {
	rows, err := d.QueryContext(ctx, `SELECT camera_make, camera_model, COUNT(*) AS n FROM media
		WHERE deleted_at IS NULL AND camera_model <> ''
		GROUP BY camera_make, camera_model ORDER BY n DESC, camera_model COLLATE NOCASE, camera_make COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("catalog: cameras: %w", err)
	}
	defer rows.Close()
	var out []Camera
	for rows.Next() {
		var c Camera
		if err := rows.Scan(&c.Make, &c.Model, &c.Count); err != nil {
			return nil, fmt.Errorf("catalog: cameras: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog: cameras: %w", err)
	}
	return out, nil
}

// GeoPoint is the position of one row with coordinates.
type GeoPoint struct {
	ID       int64
	Hash     uint64
	Lat, Lon float64
	TakenAt  time.Time // wall-clock as in Media.TakenAt (UTC placeholder zone); zero = unknown
}

// geoBox is a validated bounding box query: the box condition with its
// arguments, and the full condition list (filter plus box).
type geoBox struct {
	box     string
	boxArgs []any
	conds   []string
	args    []any
}

// geoWhere adds the bounding box (west, south, east, north; west > east
// crosses the antimeridian) to f's conditions. The lat IS NOT NULL term
// lets SQLite use the partial media_latlon index.
func geoWhere(f Filter, west, south, east, north float64) (geoBox, error) {
	for _, v := range []float64{west, south, east, north} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return geoBox{}, fmt.Errorf("catalog: invalid bounding box")
		}
	}
	if south > north {
		return geoBox{}, fmt.Errorf("catalog: invalid bounding box: south %v > north %v", south, north)
	}
	g := geoBox{box: "lat IS NOT NULL AND lon IS NOT NULL AND lat >= ? AND lat <= ? AND "}
	if west <= east {
		g.box += "lon >= ? AND lon <= ?"
	} else {
		g.box += "(lon >= ? OR lon <= ?)"
	}
	g.boxArgs = []any{south, north, west, east}
	conds, args, err := f.where()
	if err != nil {
		return geoBox{}, err
	}
	g.conds = append(conds, g.box)
	g.args = append(args, g.boxArgs...)
	return g, nil
}

// from returns the FROM clause for a query over the box. The media_latlon
// index pays off for a box holding a small part of the catalog; for a box
// holding a large part (a world view, or a zoomed-in home town that holds
// most photos) its random row lookups cost several times a plain table
// scan. Without ANALYZE statistics SQLite always takes the index, so the
// choice is made here from an index-only count that stops at a quarter of
// the rows (cheap: the partial index covers lat and lon).
func (g geoBox) from(ctx context.Context, d *DB) (string, error) {
	var total int64
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&total); err != nil {
		return "", err
	}
	limit := total/4 + 1
	var n int64
	if err := d.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM media WHERE "+g.box+" LIMIT ?)",
		append(append([]any{}, g.boxArgs...), limit)...).Scan(&n); err != nil {
		return "", err
	}
	if n >= limit {
		return "media NOT INDEXED", nil
	}
	return "media", nil
}

// GeoPoints returns every row with coordinates matching f inside the box
// (west, south, east, north; west > east crosses the antimeridian). With a
// limit (> 0) the newest rows are returned, ordered taken_at DESC, id DESC
// (undated last); limit 0 returns every row in no particular order (the
// clustering pass does not need an order, and sorting 100k rows would
// double its cost).
//
// A full-table scan for limit 0 is split into geoScanParts id ranges read
// concurrently on separate connections (SQLite in WAL mode serves
// concurrent readers): the scan is CPU-bound in the pure-Go SQLite, and
// this roughly divides a 100k-row world view by the number of parts.
func (d *DB) GeoPoints(ctx context.Context, f Filter, west, south, east, north float64, limit int) ([]GeoPoint, error) {
	g, err := geoWhere(f, west, south, east, north)
	if err != nil {
		return nil, err
	}
	from, err := g.from(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("catalog: geo points: %w", err)
	}
	q := "SELECT id, hash, lat, lon, taken_at FROM " + from + whereClause(g.conds)
	if limit > 0 {
		out, err := d.geoPoints(ctx, q+" ORDER BY taken_at IS NULL, taken_at DESC, id DESC LIMIT ?", append(g.args, limit)...)
		if err != nil {
			return nil, fmt.Errorf("catalog: geo points: %w", err)
		}
		return out, nil
	}
	parts := 1
	var maxID int64
	if from != "media" { // a table scan
		if err := d.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM media").Scan(&maxID); err != nil {
			return nil, fmt.Errorf("catalog: geo points: %w", err)
		}
		parts = min(geoScanParts, max(1, runtime.NumCPU()), int(maxID/geoMinPartRows+1))
	}
	if parts == 1 {
		out, err := d.geoPoints(ctx, q, g.args...)
		if err != nil {
			return nil, fmt.Errorf("catalog: geo points: %w", err)
		}
		return out, nil
	}
	results := make([][]GeoPoint, parts)
	errs := make([]error, parts)
	var wg sync.WaitGroup
	step := maxID/int64(parts) + 1
	for i := range parts {
		wg.Go(func() {
			lo, hi := int64(i)*step, int64(i+1)*step
			results[i], errs[i] = d.geoPoints(ctx, q+" AND id > ? AND id <= ?", append(slices.Clip(g.args), lo, hi)...)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("catalog: geo points: %w", err)
	}
	return slices.Concat(results...), nil
}

const geoScanParts = 4 // concurrent id ranges of a GeoPoints table scan

// geoMinPartRows is the least number of ids per range; smaller catalogs
// are scanned in one query. A variable for tests.
var geoMinPartRows int64 = 10000

func (d *DB) geoPoints(ctx context.Context, q string, args ...any) ([]GeoPoint, error) {
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GeoPoint
	for rows.Next() {
		var (
			p    GeoPoint
			hash int64
			wall sql.NullInt64
		)
		if err := rows.Scan(&p.ID, &hash, &p.Lat, &p.Lon, &wall); err != nil {
			return nil, err
		}
		p.Hash = uint64(hash)
		p.TakenAt, _ = decodeTakenAt(wall, sql.NullInt64{})
		out = append(out, p)
	}
	return out, rows.Err()
}

// GeoItems returns up to limit rows (all when limit <= 0) with coordinates
// matching f inside the box, newest first (taken_at DESC, id DESC, undated
// last), and the total number of matching rows. Both come from one
// statement (a COUNT(*) OVER () window, computed before the LIMIT), so they
// describe the same snapshot even while an import adds rows.
func (d *DB) GeoItems(ctx context.Context, f Filter, west, south, east, north float64, limit int) ([]Media, int64, error) {
	g, err := geoWhere(f, west, south, east, north)
	if err != nil {
		return nil, 0, err
	}
	from, err := g.from(ctx, d)
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: geo items: %w", err)
	}
	q := "SELECT " + mediaColumns + ", COUNT(*) OVER () FROM " + from + whereClause(g.conds) +
		" ORDER BY taken_at IS NULL, taken_at DESC, id DESC"
	args := g.args
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: geo items: %w", err)
	}
	defer rows.Close()
	var (
		items []Media
		total int64
	)
	for rows.Next() {
		m, err := scanMedia(withExtra{rows, []any{&total}})
		if err != nil {
			return nil, 0, fmt.Errorf("catalog: geo items: %w", err)
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("catalog: geo items: %w", err)
	}
	return items, total, nil // no row: an empty box, total 0
}

// withExtra scans the media columns followed by extra columns.
type withExtra struct {
	r     rowScanner
	extra []any
}

func (w withExtra) Scan(dest ...any) error { return w.r.Scan(append(dest, w.extra...)...) }
