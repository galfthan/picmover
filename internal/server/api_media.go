package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"picmover/internal/catalog"
)

// wallClockLayout is the JSON form of wall-clock capture times: no zone,
// the offset (when known) is sent separately.
const wallClockLayout = "2006-01-02T15:04:05"

// wallClock formats a catalog wall-clock time, or nil for the zero time.
func wallClock(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(wallClockLayout)
	return &s
}

// hexHash is the JSON and URL form of a content hash: 16 lower-case hex
// digits (JavaScript numbers cannot hold a uint64).
func hexHash(h uint64) string { return fmt.Sprintf("%016x", h) }

// parseHash parses exactly 16 lower-case hex digits.
func parseHash(s string) (uint64, bool) {
	if len(s) != 16 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return 0, false
		}
	}
	h, err := strconv.ParseUint(s, 16, 64)
	return h, err == nil
}

// parseID parses a positive row id.
func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// dateLayout is the from/to filter format.
const dateLayout = "2006-01-02"

// userFilter is the part of a filter resolved through user.db: minimum
// rating, tags (all must match) and album. Zero values mean none.
type userFilter struct {
	rating int
	tags   []int64
	album  int64
}

func (u userFilter) empty() bool { return u.rating == 0 && len(u.tags) == 0 && u.album == 0 }

// parseFilter reads the filters shared by timeline, media, geo and
// geo/items: type, gps, camera, session, from, to, recur, rating, tag
// (repeatable), album and trashed. A malformed or unknown value is an
// error (400) naming the parameter.
func parseFilter(q url.Values) (catalog.Filter, userFilter, error) {
	var (
		f  catalog.Filter
		uf userFilter
	)
	if v := q.Get("type"); v != "" {
		if !catalog.ValidFileType(v) {
			return f, uf, fmt.Errorf("invalid type %q: want image, image_raw or video", v)
		}
		f.FileType = v
	}
	switch v := q.Get("gps"); v {
	case "", "0":
	case "1":
		f.HasGPS = true
	default:
		return f, uf, fmt.Errorf("invalid gps %q: want 1", v)
	}
	f.CameraModel = q.Get("camera")
	if v := q.Get("session"); v != "" {
		id, ok := parseID(v)
		if !ok {
			return f, uf, fmt.Errorf("invalid session %q", v)
		}
		f.SessionID = id
	}
	for _, b := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		v := q.Get(b.name)
		if v == "" {
			continue
		}
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return f, uf, fmt.Errorf("invalid %s %q: want YYYY-MM-DD", b.name, v)
		}
		*b.dst = t
	}
	if v := q.Get("recur"); v != "" {
		r, err := parseRecur(v)
		if err != nil {
			return f, uf, err
		}
		f.Recur = r
	}
	switch v := q.Get("trashed"); v {
	case "", "0":
	case "1":
		f.Trashed = true
	default:
		return f, uf, fmt.Errorf("invalid trashed %q: want 1", v)
	}
	if v := q.Get("rating"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 5 {
			return f, uf, fmt.Errorf("invalid rating %q: want a minimum rating 1-5", v)
		}
		uf.rating = n
	}
	for _, v := range q["tag"] {
		id, ok := parseID(v)
		if !ok {
			return f, uf, fmt.Errorf("invalid tag %q: want a tag id", v)
		}
		uf.tags = append(uf.tags, id)
	}
	if len(uf.tags) > maxFilterTags {
		return f, uf, fmt.Errorf("invalid tag: at most %d tags", maxFilterTags)
	}
	if v := q.Get("album"); v != "" {
		id, ok := parseID(v)
		if !ok {
			return f, uf, fmt.Errorf("invalid album %q: want an album id", v)
		}
		uf.album = id
	}
	return f, uf, nil
}

// maxFilterTags bounds the tag parameters of one request.
const maxFilterTags = 32

// parseRecur parses "MM-DD" or "MM-DD..MM-DD" (inclusive, may wrap the
// year end).
func parseRecur(v string) (*catalog.Recur, error) {
	from, to, found := strings.Cut(v, "..")
	if !found {
		to = from
	}
	if !catalog.ValidMonthDay(from) || !catalog.ValidMonthDay(to) {
		return nil, fmt.Errorf("invalid recur %q: want MM-DD or MM-DD..MM-DD", v)
	}
	return &catalog.Recur{From: from, To: to}, nil
}

// resolveFilter turns the user.db part of a filter into f.Hashes: the
// intersection of the rated, tagged and album hash sets.
func resolveFilter(ctx context.Context, l *libHandles, f *catalog.Filter, uf userFilter) error {
	if uf.empty() {
		return nil
	}
	var sets [][]uint64
	if uf.rating > 0 {
		hs, err := l.user.HashesWithRating(ctx, uf.rating)
		if err != nil {
			return err
		}
		sets = append(sets, hs)
	}
	if len(uf.tags) > 0 {
		hs, err := l.user.HashesWithTags(ctx, uf.tags)
		if err != nil {
			return err
		}
		sets = append(sets, hs)
	}
	if uf.album > 0 {
		hs, err := l.user.AlbumHashes(ctx, uf.album)
		if err != nil {
			return err
		}
		sets = append(sets, hs)
	}
	f.Hashes = intersect(sets)
	return nil
}

// intersect returns the hashes present in every set (non-nil).
func intersect(sets [][]uint64) []uint64 {
	out := slices.Clone(sets[0])
	for _, s := range sets[1:] {
		in := make(map[uint64]bool, len(s))
		for _, h := range s {
			in[h] = true
		}
		out = slices.DeleteFunc(out, func(h uint64) bool { return !in[h] })
	}
	if out == nil {
		out = []uint64{}
	}
	return out
}

// filter parses and resolves the request's filter, answering 400 or 500
// itself when it returns false.
func (s *Server) filter(w http.ResponseWriter, r *http.Request, l *libHandles) (catalog.Filter, bool) {
	f, uf, err := parseFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return f, false
	}
	if err := resolveFilter(r.Context(), l, &f, uf); err != nil {
		s.internalError(w, r, err)
		return f, false
	}
	return f, true
}

// itemJSON is the compact media item of lists.
type itemJSON struct {
	ID      int64   `json:"id"`
	Hash    string  `json:"hash"`
	Type    string  `json:"type"`
	TakenAt *string `json:"takenAt"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	Dur     int64   `json:"dur,omitempty"` // milliseconds
	Thumb   string  `json:"thumb"`
	GPS     bool    `json:"gps,omitempty"`
	// ThumbAt is the Unix time the renditions were last (re)generated,
	// omitted when never: clients request /api/thumb/<hash>?v=<thumbAt>
	// (and /api/preview) so a regenerated rendition is not served from
	// the browser cache.
	ThumbAt int64 `json:"thumbAt,omitempty"`
	// Rating is the user.db rating, omitted when 0.
	Rating int `json:"rating,omitempty"`
	// DeletedAt is when the item was trashed (wall clock in the server's
	// zone), only in trash listings.
	DeletedAt *string `json:"deletedAt,omitempty"`
}

func newItem(m *catalog.Media) itemJSON {
	return itemJSON{
		ID: m.ID, Hash: hexHash(m.Hash), Type: m.FileType, TakenAt: wallClock(m.TakenAt),
		W: m.Width, H: m.Height, Dur: m.DurationMS, Thumb: m.ThumbState, GPS: m.HasGPS,
		ThumbAt: unixOrZero(m.ThumbAt),
	}
}

// unixOrZero is t in Unix seconds, 0 for the zero time.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// localWallClock formats a real instant as a wall-clock string in the
// server's zone, or nil for the zero time.
func localWallClock(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := t.Local().Format(wallClockLayout)
	return &v
}

// itemList returns the items of ms with their ratings from user.db and,
// for trashed rows, deletedAt.
func itemList(ctx context.Context, l *libHandles, ms []catalog.Media) ([]itemJSON, error) {
	out := make([]itemJSON, 0, len(ms))
	hashes := make([]uint64, len(ms))
	for i := range ms {
		hashes[i] = ms[i].Hash
	}
	ratings, err := l.user.Ratings(ctx, hashes)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		it := newItem(&ms[i])
		it.Rating = ratings[ms[i].Hash]
		it.DeletedAt = localWallClock(ms[i].DeletedAt)
		out = append(out, it)
	}
	return out, nil
}

// detailJSON is /api/media/{id}: the compact item plus every column the
// UI shows.
type detailJSON struct {
	itemJSON
	RelPath       string    `json:"relPath"`
	Ext           string    `json:"ext"`
	Size          int64     `json:"size"`
	Offset        *int      `json:"offset"` // seconds east of UTC; null when unknown
	TakenAtSource string    `json:"takenAtSource"`
	Orientation   int       `json:"orientation"`
	Lat           *float64  `json:"lat,omitempty"`
	Lon           *float64  `json:"lon,omitempty"`
	Altitude      *float64  `json:"altitude,omitempty"`
	CameraMake    string    `json:"cameraMake"`
	CameraModel   string    `json:"cameraModel"`
	CameraType    string    `json:"cameraType"`
	Lens          string    `json:"lens"`
	OriginalPath  string    `json:"originalPath"`
	ImportedAt    *string   `json:"importedAt"` // RFC 3339, a real instant; null when unknown
	SessionID     *int64    `json:"sessionId"`  // null when none
	Rating        int       `json:"rating"`     // user.db; 0 when unrated
	PHash         *string   `json:"phash"`      // 16 hex digits; null when none
	ThumbState    string    `json:"thumbState"`
	ThumbError    string    `json:"thumbError"`
	Displayable   bool      `json:"displayable"`
	Tags          []refJSON `json:"tags"`
	Albums        []refJSON `json:"albums"`
	DeletedAt     *string   `json:"deletedAt"` // wall clock in the server's zone; null when live
}

// refJSON names a tag or an album.
type refJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func newDetail(m *catalog.Media) detailJSON {
	d := detailJSON{
		itemJSON:      newItem(m),
		RelPath:       m.RelPath,
		Ext:           m.Ext,
		Size:          m.Size,
		TakenAtSource: m.TakenAtSource,
		Orientation:   m.Orientation,
		CameraMake:    m.CameraMake,
		CameraModel:   m.CameraModel,
		CameraType:    m.CameraType,
		Lens:          m.Lens,
		OriginalPath:  m.OriginalPath,
		ThumbState:    m.ThumbState,
		ThumbError:    m.ThumbError,
		Displayable:   displayable(m.Ext),
	}
	if m.OffsetKnown && !m.TakenAt.IsZero() {
		_, off := m.TakenAt.Zone()
		d.Offset = &off
	}
	if m.HasGPS {
		lat, lon := m.Lat, m.Lon
		d.Lat, d.Lon = &lat, &lon
	}
	if m.HasAltitude {
		alt := m.Altitude
		d.Altitude = &alt
	}
	if !m.ImportedAt.IsZero() {
		ia := m.ImportedAt.Local().Format(time.RFC3339)
		d.ImportedAt = &ia
	}
	if m.ImportSessionID != 0 {
		id := m.ImportSessionID
		d.SessionID = &id
	}
	if m.PHash != 0 {
		p := hexHash(m.PHash)
		d.PHash = &p
	}
	return d
}

type timelineJSON struct {
	Buckets []bucketJSON `json:"buckets"`
}

type bucketJSON struct {
	Year  int   `json:"year"`
	Month int   `json:"month"`
	Count int64 `json:"count"`
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request, l *libHandles) {
	f, ok := s.filter(w, r, l)
	if !ok {
		return
	}
	buckets, err := l.db.Buckets(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := timelineJSON{Buckets: make([]bucketJSON, 0, len(buckets))}
	for _, b := range buckets {
		out.Buckets = append(out.Buckets, bucketJSON{Year: b.Year, Month: b.Month, Count: b.Count})
	}
	writeJSON(w, http.StatusOK, out)
}

type itemsJSON struct {
	Items []itemJSON `json:"items"`
}

func (s *Server) handleMediaList(w http.ResponseWriter, r *http.Request, l *libHandles) {
	q := r.URL.Query()
	f, ok := s.filter(w, r, l)
	if !ok {
		return
	}
	year, err1 := strconv.Atoi(q.Get("year"))
	month, err2 := strconv.Atoi(q.Get("month"))
	valid := (year == 0 && month == 0) || (year >= 1 && year <= 9999 && month >= 1 && month <= 12)
	if err1 != nil || err2 != nil || !valid {
		writeError(w, http.StatusBadRequest, "year and month are required: a month (1-12) of a year, or 0 and 0 for undated media")
		return
	}
	rows, err := l.db.BucketItems(r.Context(), f, year, month)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := itemList(r.Context(), l, rows)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsJSON{Items: items})
}

// mediaRow returns the row named by the {id} path value (live or
// trashed), or answers 400/404/500 and returns false.
func (s *Server) mediaRow(w http.ResponseWriter, r *http.Request, l *libHandles) (catalog.Media, bool) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return catalog.Media{}, false
	}
	m, err := l.db.GetMedia(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such media")
		return catalog.Media{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return catalog.Media{}, false
	}
	return m, true
}

func (s *Server) handleMediaDetail(w http.ResponseWriter, r *http.Request, l *libHandles) {
	m, ok := s.mediaRow(w, r, l)
	if !ok {
		return
	}
	ctx := r.Context()
	d := newDetail(&m)
	d.DeletedAt = localWallClock(m.DeletedAt)
	var err error
	if d.Rating, err = l.user.Rating(ctx, m.Hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	tags, err := l.user.TagsOf(ctx, m.Hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	albums, err := l.user.AlbumsOf(ctx, m.Hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d.Tags = make([]refJSON, 0, len(tags))
	for _, t := range tags {
		d.Tags = append(d.Tags, refJSON{ID: t.ID, Name: t.Name})
	}
	d.Albums = make([]refJSON, 0, len(albums))
	for _, a := range albums {
		d.Albums = append(d.Albums, refJSON{ID: a.ID, Name: a.Name})
	}
	writeJSON(w, http.StatusOK, d)
}

// contentTypes maps lower-case extensions (without the dot) of originals
// to the type /api/file sends; anything else is application/octet-stream.
var contentTypes = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif",
	"webp": "image/webp", "avif": "image/avif", "heic": "image/heic", "heif": "image/heif",
	"bmp": "image/bmp", "tif": "image/tiff", "tiff": "image/tiff",
	"mp4": "video/mp4", "m4v": "video/mp4", "mov": "video/quicktime", "webm": "video/webm",
	"mkv": "video/x-matroska", "avi": "video/x-msvideo", "mpg": "video/mpeg", "mpeg": "video/mpeg",
	"3gp": "video/3gpp", "wmv": "video/x-ms-wmv", "asf": "video/x-ms-asf", "flv": "video/x-flv",
	"mts": "video/mp2t", "m2ts": "video/mp2t", "ts": "video/mp2t",
}

// browserFormats are the originals a browser can show itself.
var browserFormats = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "avif": true, "bmp": true,
	"mp4": true, "m4v": true, "webm": true, "mov": true,
}

func contentType(ext string) string {
	if ct, ok := contentTypes[strings.ToLower(strings.TrimPrefix(ext, "."))]; ok {
		return ct
	}
	return "application/octet-stream"
}

func displayable(ext string) bool {
	return browserFormats[strings.ToLower(strings.TrimPrefix(ext, "."))]
}
