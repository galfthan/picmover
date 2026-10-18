package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // IANA zones for the home zone on systems without a zone database
)

// The home time model (docs/design/home-time.md): taken_at is the wall
// clock where a photo was taken, taken_at_home the same instant on the
// clock of the library's home zone. The timeline (ordering, months, date
// filters) uses taken_at_home, so a trip's photos and videos interleave
// in the order they were taken, and the UI can still show the local time.

// MetaHomeZone is the meta key holding the library's home zone, an IANA
// zone name. When it is not set, home times are the recorded wall clocks
// (taken_at_home = taken_at), as before home times existed; the first
// import stores its zone there (Options.HomeZone of the importer).
const MetaHomeZone = "home_zone"

// Offset sources stored in taken_at_offset_source.
const (
	OffsetSourceExif   = "exif"   // OffsetTime* EXIF tags
	OffsetSourceVideo  = "video"  // QuickTime creation date with its offset
	OffsetSourceGPS    = "gps"    // reserved: zone looked up from coordinates
	OffsetSourceNearby = "nearby" // a photo of the same camera around the same instant
	OffsetSourceHome   = "home"   // an instant shown in the home zone; local offset unknown
)

// DefaultOffsetSource is the offset source of a capture time read from a
// file: exif and video carry their own offset, the other sources that
// know an offset (video_utc, mtime) are instants expressed in the zone of
// the import, which is the home zone. "" when the offset is unknown.
func DefaultOffsetSource(takenAtSource string, offsetKnown bool) string {
	if !offsetKnown {
		return ""
	}
	switch takenAtSource {
	case "exif":
		return OffsetSourceExif
	case "video":
		return OffsetSourceVideo
	}
	return OffsetSourceHome
}

// validOffsetSource reports whether s may be stored in
// taken_at_offset_source.
func validOffsetSource(s string) bool {
	switch s {
	case OffsetSourceExif, OffsetSourceVideo, OffsetSourceGPS, OffsetSourceNearby, OffsetSourceHome:
		return true
	}
	return false
}

var zoneCache sync.Map // IANA name -> *time.Location

// LoadZone returns the zone of an IANA name (cached). "" and "Local" are
// refused: a home zone must name a place.
func LoadZone(name string) (*time.Location, error) {
	if v, ok := zoneCache.Load(name); ok {
		return v.(*time.Location), nil
	}
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("catalog: invalid time zone %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("catalog: invalid time zone %q: %w", name, err)
	}
	zoneCache.Store(name, loc)
	return loc, nil
}

// ZoneName returns the IANA name of loc, "" when it has none: time.Local
// is resolved through SystemZoneName.
func ZoneName(loc *time.Location) string {
	if loc == nil || loc == time.Local {
		return SystemZoneName()
	}
	if n := loc.String(); n != "Local" {
		return n
	}
	return ""
}

// SystemZoneName returns the IANA name of the system zone: $TZ when it
// names one, else the target of the /etc/localtime link (Linux, macOS);
// "" when unknown (Windows without $TZ).
func SystemZoneName() string {
	if tz, ok := os.LookupEnv("TZ"); ok {
		tz = strings.TrimPrefix(tz, ":")
		if tz == "" {
			return "UTC"
		}
		if _, err := LoadZone(tz); err == nil {
			return tz
		}
		return ""
	}
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	target = filepath.ToSlash(target)
	if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
		if _, err := LoadZone(name); err == nil {
			return name
		}
	}
	return ""
}

// HomeWall returns the wall clock of capture time t in the home zone, as
// a time with the UTC placeholder zone (like an undecoded taken_at): t
// itself (its wall clock) when the offset or the home zone (nil) is
// unknown, the zero time for an unknown capture time.
func HomeWall(t time.Time, offsetKnown bool, home *time.Location) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	if offsetKnown && home != nil {
		t = t.In(home)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// homeZone reads the home zone: its stored name ("" when unset) and the
// zone (nil when unset, or when the name no longer loads).
func homeZone(ctx context.Context, q queryRower) (name string, loc *time.Location, err error) {
	err = q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", MetaHomeZone).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("catalog: home zone: %w", err)
	}
	if loc, err = LoadZone(name); err != nil {
		return name, nil, nil
	}
	return name, loc, nil
}

// HomeZone returns the library's home zone: its name and zone ("" and nil
// when not set: home times are then the recorded wall clocks).
func (d *DB) HomeZone(ctx context.Context) (name string, loc *time.Location, err error) {
	return homeZone(ctx, d.DB)
}

// HomeZoneFile returns the home zone name stored in the catalog at path
// ("" when none), read-only like SessionsFile: a rebuild carries it over
// to the new catalog.
func HomeZoneFile(ctx context.Context, path string) (string, error) {
	var name string
	err := readFile(ctx, path, func(d *DB) error {
		cols, err := tableColumns(ctx, d.DB, "meta")
		if err != nil || !cols["key"] {
			return err
		}
		name, _, err = homeZone(ctx, d.DB)
		return err
	})
	return name, err
}

// SetHomeZone stores name (an IANA zone) as the home zone and recomputes
// every row's home time in the same transaction: taken_at_home, and for
// rows whose offset source is 'home' also taken_at and taken_at_offset
// (they show the instant in the home zone). Setting the zone that is
// already stored changes nothing.
func (d *DB) SetHomeZone(ctx context.Context, name string) error {
	loc, err := LoadZone(name)
	if err != nil {
		return err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog: set home zone: %w", err)
	}
	defer tx.Rollback()
	old, _, err := homeZone(ctx, tx)
	if err != nil {
		return err
	}
	if old == name {
		return nil
	}
	if err := setMeta(ctx, tx, map[string]string{MetaHomeZone: name}); err != nil {
		return err
	}
	if err := recomputeHome(ctx, tx, loc); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog: set home zone: %w", err)
	}
	d.logger.Info("home zone set", "path", d.path, "zone", name, "previous", old)
	return nil
}

// EnsureHomeZone stores name as the home zone when none is stored yet
// (as SetHomeZone; "" or an unloadable name stores nothing) and returns
// the home zone in force (nil when none).
func (d *DB) EnsureHomeZone(ctx context.Context, name string) (*time.Location, error) {
	cur, loc, err := d.HomeZone(ctx)
	if err != nil || cur != "" {
		return loc, err
	}
	if _, err := LoadZone(name); err != nil {
		return loc, nil
	}
	if err := d.SetHomeZone(ctx, name); err != nil {
		return nil, err
	}
	_, loc, err = d.HomeZone(ctx)
	return loc, err
}

// recomputeHome rewrites taken_at_home of every row for home zone loc,
// and re-expresses the rows with offset source 'home' in loc. With no
// home zone (nil) every taken_at_home is taken_at.
func recomputeHome(ctx context.Context, tx *sql.Tx, loc *time.Location) error {
	cond := "taken_at_offset IS NULL AND "
	if loc == nil {
		cond = ""
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media SET taken_at_home = taken_at
		WHERE `+cond+`taken_at_home IS NOT taken_at`); err != nil {
		return fmt.Errorf("catalog: recompute home times: %w", err)
	}
	if loc == nil {
		return nil
	}
	type row struct {
		id, wall, home int64
		off            sql.NullInt64
		homeSet        bool
		src            sql.NullString
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, taken_at, taken_at_offset, taken_at_home, taken_at_offset_source
		FROM media WHERE taken_at IS NOT NULL AND taken_at_offset IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("catalog: recompute home times: %w", err)
	}
	var all []row
	for rows.Next() {
		var (
			r    row
			home sql.NullInt64
		)
		if err := rows.Scan(&r.id, &r.wall, &r.off, &home, &r.src); err != nil {
			rows.Close()
			return fmt.Errorf("catalog: recompute home times: %w", err)
		}
		r.home, r.homeSet = home.Int64, home.Valid
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("catalog: recompute home times: %w", err)
	}
	upHome, err := tx.PrepareContext(ctx, "UPDATE media SET taken_at_home = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("catalog: recompute home times: %w", err)
	}
	defer upHome.Close()
	upAll, err := tx.PrepareContext(ctx, "UPDATE media SET taken_at = ?, taken_at_offset = ?, taken_at_home = ? WHERE id = ?")
	if err != nil {
		return fmt.Errorf("catalog: recompute home times: %w", err)
	}
	defer upAll.Close()
	for _, r := range all {
		inst := time.Unix(r.wall-r.off.Int64, 0).In(loc)
		home := wallSeconds(inst)
		if r.src.String == OffsetSourceHome {
			_, off := inst.Zone()
			if home != r.wall || int64(off) != r.off.Int64 || !r.homeSet || r.home != home {
				if _, err := upAll.ExecContext(ctx, home, off, home, r.id); err != nil {
					return fmt.Errorf("catalog: recompute home times: %w", err)
				}
			}
			continue
		}
		if !r.homeSet || r.home != home {
			if _, err := upHome.ExecContext(ctx, home, r.id); err != nil {
				return fmt.Errorf("catalog: recompute home times: %w", err)
			}
		}
	}
	return nil
}

// NearbyWindow is how far from a video an offset-bearing photo may be
// to lend it its offset.
const NearbyWindow = 6 * time.Hour

// InferVideoOffsets estimates the local offset of videos whose capture
// time is a UTC instant (taken_at_source 'video_utc'): the offset of the
// nearest photo with an EXIF offset taken by the same camera within
// NearbyWindow (offset source 'nearby'; same import session preferred),
// else the home zone (offset source 'home'; with no home zone the stored
// offset, which is the import's zone, is kept). taken_at, taken_at_offset,
// taken_at_offset_source and taken_at_home are rewritten; the instant
// never changes, nor does a home time in a home zone. With sessionID > 0 the videos
// of that import session are estimated, plus every video still on the
// home zone (a later import may have brought its photos); with 0, every
// video_utc row. Returns the number of rows changed.
func (d *DB) InferVideoOffsets(ctx context.Context, sessionID int64) (int, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	defer tx.Rollback()
	_, loc, err := homeZone(ctx, tx)
	if err != nil {
		return 0, err
	}
	n, err := inferVideoOffsets(ctx, tx, loc, sessionID, sessionID == 0)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	return n, nil
}

// offsetVideo is a video_utc row being estimated.
type offsetVideo struct {
	id, inst, wall, home int64
	off                  int64
	src                  string
	make, model          string
	session              int64
}

// offsetPhoto is a row whose offset came from EXIF.
type offsetPhoto struct {
	inst, off            int64
	make, model, camType string
	session              int64
}

// genericCamera reports whether a video names no real camera: Android
// phones write only "com.android.version" (stored as make "Android").
func genericCamera(make, model string) bool {
	return model == "" || strings.EqualFold(make, "Android")
}

// matchTier ranks photo p as the offset donor of video v: 0 the same
// camera in the same import, 1 the same camera, 2 for a video without a
// real camera name (or of the same make) a photo of the same import
// (for an Android video, a phone's); -1 no match.
func matchTier(v *offsetVideo, p *offsetPhoto) int {
	sameSession := v.session != 0 && v.session == p.session
	if v.model != "" && strings.EqualFold(v.make, p.make) && strings.EqualFold(v.model, p.model) {
		if sameSession {
			return 0
		}
		return 1
	}
	if !sameSession {
		return -1
	}
	switch {
	case strings.EqualFold(v.make, "Android"):
		if p.camType == "phone" {
			return 2
		}
	case v.model == "":
		return 2
	case v.make != "" && strings.EqualFold(v.make, p.make):
		return 2
	}
	return -1
}

// donor returns the best photo for v among photos (sorted by instant),
// nil when none is within NearbyWindow.
func donor(v *offsetVideo, photos []offsetPhoto) *offsetPhoto {
	win := int64(NearbyWindow / time.Second)
	i := sort.Search(len(photos), func(i int) bool { return photos[i].inst >= v.inst-win })
	var (
		best     *offsetPhoto
		bestTier = 99
		bestDist int64
	)
	for ; i < len(photos) && photos[i].inst <= v.inst+win; i++ {
		p := &photos[i]
		tier := matchTier(v, p)
		if tier < 0 {
			continue
		}
		dist := p.inst - v.inst
		if dist < 0 {
			dist = -dist
		}
		if tier < bestTier || tier == bestTier && dist < bestDist {
			best, bestTier, bestDist = p, tier, dist
		}
	}
	return best
}

// inferVideoOffsets is InferVideoOffsets inside tx with home zone loc.
// all selects every video_utc row; else those of sessionID and those on
// the home zone.
func inferVideoOffsets(ctx context.Context, tx *sql.Tx, loc *time.Location, sessionID int64, all bool) (int, error) {
	q := `SELECT id, taken_at, taken_at_offset, coalesce(taken_at_home, taken_at), coalesce(taken_at_offset_source, ''),
		camera_make, camera_model, coalesce(import_session_id, 0)
		FROM media WHERE taken_at_source = 'video_utc' AND taken_at IS NOT NULL AND taken_at_offset IS NOT NULL`
	var args []any
	if !all {
		q += " AND (import_session_id = ? OR taken_at_offset_source = 'home')"
		args = append(args, sessionID)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	var (
		videos   []offsetVideo
		lo, hi   int64
		haveSpan bool
	)
	for rows.Next() {
		var v offsetVideo
		if err := rows.Scan(&v.id, &v.wall, &v.off, &v.home, &v.src, &v.make, &v.model, &v.session); err != nil {
			rows.Close()
			return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
		}
		v.inst = v.wall - v.off
		if !haveSpan || v.inst < lo {
			lo = v.inst
		}
		if !haveSpan || v.inst > hi {
			hi = v.inst
		}
		haveSpan = true
		videos = append(videos, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	if len(videos) == 0 {
		return 0, nil
	}
	// The photos' wall clocks are within a day of their instants (UTC
	// offsets are at most 14 hours), so the taken_at index narrows them.
	margin := int64(NearbyWindow/time.Second) + 24*3600
	prow, err := tx.QueryContext(ctx, `SELECT taken_at - taken_at_offset AS inst, taken_at_offset,
		camera_make, camera_model, camera_type, coalesce(import_session_id, 0)
		FROM media WHERE taken_at BETWEEN ? AND ? AND taken_at_offset_source = 'exif'
		AND taken_at_offset IS NOT NULL ORDER BY inst`, lo-margin, hi+margin)
	if err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	var photos []offsetPhoto
	for prow.Next() {
		var p offsetPhoto
		if err := prow.Scan(&p.inst, &p.off, &p.make, &p.model, &p.camType, &p.session); err != nil {
			prow.Close()
			return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
		}
		photos = append(photos, p)
	}
	prow.Close()
	if err := prow.Err(); err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	up, err := tx.PrepareContext(ctx, `UPDATE media SET taken_at = ?, taken_at_offset = ?,
		taken_at_offset_source = ?, taken_at_home = ? WHERE id = ?`)
	if err != nil {
		return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
	}
	defer up.Close()
	changed := 0
	for i := range videos {
		v := &videos[i]
		var (
			off int64
			src string
		)
		switch p := donor(v, photos); {
		case p != nil:
			off, src = p.off, OffsetSourceNearby
		case loc != nil:
			_, o := time.Unix(v.inst, 0).In(loc).Zone()
			off, src = int64(o), OffsetSourceHome
		default:
			off, src = v.off, OffsetSourceHome
		}
		wall := v.inst + off
		home := wall
		if loc != nil {
			home = wallSeconds(time.Unix(v.inst, 0).In(loc))
		}
		if wall == v.wall && off == v.off && src == v.src && home == v.home {
			continue
		}
		if _, err := up.ExecContext(ctx, wall, off, src, home, v.id); err != nil {
			return 0, fmt.Errorf("catalog: infer video offsets: %w", err)
		}
		changed++
	}
	return changed, nil
}

// migrateFromV3 upgrades a version 3 catalog inside tx (an IMMEDIATE
// transaction that read version 3) and commits: the new columns and
// index, the offset sources from taken_at_source, the home zone (when
// meta has none, the system zone when its name is known: the zone the
// old catalog's instants were most likely converted to), every home
// time, and the video offsets inferred (InferVideoOffsets).
func (d *DB) migrateFromV3(ctx context.Context, tx *sql.Tx) error {
	fail := func(err error) error {
		return fmt.Errorf("catalog: migrate %s to version %d: %w", d.path, SchemaVersion, err)
	}
	for _, ddl := range []string{derivedTables, migrateV3} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media SET taken_at_offset_source = CASE
		WHEN taken_at_offset IS NULL THEN NULL
		WHEN taken_at_source = 'exif' THEN 'exif'
		WHEN taken_at_source = 'video' THEN 'video'
		ELSE 'home' END`); err != nil {
		return fail(err)
	}
	name, loc, err := homeZone(ctx, tx)
	if err != nil {
		return fail(err)
	}
	if name == "" {
		if sys := SystemZoneName(); sys != "" {
			if loc, err = LoadZone(sys); err != nil {
				return fail(err)
			}
			if err := setMeta(ctx, tx, map[string]string{MetaHomeZone: sys}); err != nil {
				return fail(err)
			}
			name = sys
		}
	}
	if err := recomputeHome(ctx, tx, loc); err != nil {
		return fail(err)
	}
	n, err := inferVideoOffsets(ctx, tx, loc, 0, true)
	if err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, migrateV3Index); err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	d.logger.Info("catalog migrated", "path", d.path, "from", migratableVersion, "to", SchemaVersion,
		"home_zone", name, "videos_estimated", n)
	return nil
}
