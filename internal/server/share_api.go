package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/share"
	"picmover/internal/userdata"
)

// The phone's handlers (share listener). Every response is built here
// from catalog rows with the fields the design allows; admin handlers are
// reused only for renditions and originals, after the row checks.

// shareLibJSON is a library of a library share: no path, no state.
type shareLibJSON struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Cover *string `json:"cover"` // a hash for /api/l/{id}/thumb/{hash}; null when none
	Total int64   `json:"total"`
}

// shareAlbumInfoJSON is the album of an album share.
type shareAlbumInfoJSON struct {
	Name  string  `json:"name"`
	Count int64   `json:"count"` // live members
	Cover *string `json:"cover"` // a hash for /api/l/album/thumb/{hash}; null when empty
}

// shareJSON is a share as its phones see it.
type shareJSON struct {
	Name         string              `json:"name"`
	Kind         string              `json:"kind"`
	Capabilities share.Capabilities  `json:"capabilities"`
	ExpiresAt    *string             `json:"expiresAt"` // RFC 3339; null: never
	Libraries    *[]shareLibJSON     `json:"libraries,omitempty"`
	Album        *shareAlbumInfoJSON `json:"album,omitempty"`
}

type shareDeviceRefJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type shareSessionJSON struct {
	Device   shareDeviceRefJSON `json:"device"`
	Computer string             `json:"computer"`
	Share    shareJSON          `json:"share"`
}

// rfc3339Ptr formats t in the local zone, nil for nil or zero.
func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	v := t.Local().Format(time.RFC3339)
	return &v
}

// shareInfo builds the phone's share object. A library share lists its
// libraries that are there (shareLibs); an album share needs its library
// acquired and bound: on a refusal it has answered (403 gone, 503) and
// returns false.
func (s *Server) shareInfo(w http.ResponseWriter, r *http.Request, req *shareReq) (shareJSON, bool) {
	out := shareJSON{Name: req.sh.Name, Kind: req.sh.Scope.Kind, Capabilities: req.sh.Capabilities,
		ExpiresAt: rfc3339Ptr(req.sh.ExpiresAt)}
	if refs := req.auth.libraries(); refs != nil {
		libs := s.shareLibs(r.Context(), refs)
		out.Libraries = &libs
		return out, true
	}
	ref, ok := req.auth.library(albumKey)
	if !ok {
		writeShareState(w, stateUnavailable)
		return out, false
	}
	l, ok := s.shareLibrary(w, r, req, ref)
	if !ok {
		return out, false
	}
	defer l.entry.release()
	a, err := l.user.Album(r.Context(), req.sh.Scope.Album.ID)
	if err != nil {
		s.shareScopeError(w, r, req, errScopeGone)
		return out, false
	}
	j, err := s.newAlbum(r, l, a)
	if err != nil {
		s.internalError(w, r, err)
		return out, false
	}
	out.Album = &shareAlbumInfoJSON{Name: j.Name, Count: j.Count, Cover: j.Cover}
	return out, true
}

// shareLibs describes the libraries of refs that are in the served
// workspace with the shared LibraryID, without opening them (the cached
// peek). A missing or replaced library is left out.
func (s *Server) shareLibs(ctx context.Context, refs []libRef) []shareLibJSON {
	out := []shareLibJSON{}
	for _, ref := range refs {
		li, ok := s.sharedLibrary(ctx, ref)
		if !ok {
			continue
		}
		j := shareLibJSON{ID: li.id, Name: li.id}
		if pk, err := s.ws.peek(ctx, filepath.Join(li.path, library.DBName)); err == nil {
			j.Total = pk.Summary.Total
			if pk.HasCover {
				c := hexHash(pk.Cover)
				j.Cover = &c
			}
		}
		out = append(out, j)
	}
	return out
}

// sharedLibrary finds ref's library in the served workspace and checks
// its LibraryID without opening it: false when it is missing or another
// library.
func (s *Server) sharedLibrary(ctx context.Context, ref libRef) (listed, bool) {
	li, err := s.ws.find(ref.id)
	if err != nil || !li.library {
		return listed{}, false
	}
	id, err := s.libraryIDOf(ctx, li)
	if err != nil {
		s.log.Debug("share: cannot read the library id", "id", li.id, "err", err)
		return listed{}, false
	}
	return li, id != "" && id == ref.libraryID
}

// libraryIDOf reads a listed library's LibraryID: from its open user.db,
// else read-only from the file ("" when it has none yet).
func (s *Server) libraryIDOf(ctx context.Context, li listed) (string, error) {
	if e := s.ws.entry(li.id); e != nil {
		if h, _, ok := e.acquire(); ok {
			defer e.release()
			return h.user.LibraryID(ctx)
		}
	}
	return s.ws.peekLibraryID(ctx, li.path)
}

// albumOf reads album id of a listed library: from its open user.db,
// else read-only from the file. ok is false when there is no such album.
func (s *Server) albumOf(ctx context.Context, li listed, id int64) (userdata.Album, bool, error) {
	if e := s.ws.entry(li.id); e != nil {
		if h, _, ok := e.acquire(); ok {
			defer e.release()
			a, err := h.user.Album(ctx, id)
			if errors.Is(err, userdata.ErrNotFound) {
				return userdata.Album{}, false, nil
			}
			return a, err == nil, err
		}
	}
	albums, err := s.ws.peekAlbums(ctx, li.path)
	if errors.Is(err, userdata.ErrNoUserDB) {
		return userdata.Album{}, false, nil
	}
	if err != nil {
		return userdata.Album{}, false, err
	}
	for _, a := range albums {
		if a.ID == id {
			return a, true, nil
		}
	}
	return userdata.Album{}, false, nil
}

// handleShareSession is GET /api/session: the device and its share, 401
// {error, paired: false} when not paired, 403 {error, state} when the
// share is not active.
func (s *Server) handleShareSession(w http.ResponseWriter, r *http.Request) {
	req, ok := s.shareDeviceAuth(w, r, false)
	if !ok {
		return
	}
	info, ok := s.shareInfo(w, r, req)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, shareSessionJSON{Device: shareDeviceRefJSON{ID: req.dev.ID, Name: req.dev.Name},
		Computer: s.share.hostname, Share: info})
}

// handleShareLibraries is GET /api/libraries: a library share's
// libraries (404 in an album share).
func (s *Server) handleShareLibraries(w http.ResponseWriter, r *http.Request, req *shareReq) {
	refs := req.auth.libraries()
	if refs == nil {
		shareNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Libraries []shareLibJSON `json:"libraries"`
	}{s.shareLibs(r.Context(), refs)})
}

// shareSummaryJSON is the redacted library summary.
type shareSummaryJSON struct {
	Total    int64   `json:"total"`
	Images   int64   `json:"images"`
	Raw      int64   `json:"raw"`
	Videos   int64   `json:"videos"`
	Earliest *string `json:"earliest"`
	Latest   *string `json:"latest"`
}

// handleShareLibrary is GET /api/l/{lib}/library: {id, name, summary}
// (404 unless the scope covers whole libraries).
func (s *Server) handleShareLibrary(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	if !req.auth.wholeLibrary() {
		shareNotFound(w)
		return
	}
	sum, err := l.db.Summary(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID      string           `json:"id"`
		Name    string           `json:"name"`
		Summary shareSummaryJSON `json:"summary"`
	}{l.id, l.id, shareSummaryJSON{Total: sum.Total, Images: sum.Images, Raw: sum.Raw, Videos: sum.Videos,
		Earliest: wallClock(sum.Earliest), Latest: wallClock(sum.Latest)}})
}

// shareFilterKeys are the query parameters a phone's list may carry; the
// media list adds year and month. Anything else is 400 (trashed, import
// and session among them).
var shareFilterKeys = map[string]bool{"type": true, "from": true, "to": true, "recur": true, "months": true,
	"rating": true, "album": true, "tag": true, "camera": true, "gps": true, "minpx": true, "maxpx": true}

// maxShareCamera bounds the camera filter's value (a model name).
const maxShareCamera = 200

// shareFilter parses a phone's list filter: the whitelist, rating only
// with rate, gps only with location, album and tag only where the scope
// covers whole libraries (an album that is not one of this library's is
// 404), then the scope's restriction and the user.db part. It answers
// itself when it returns false.
func (s *Server) shareFilter(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles, extra ...string) (catalog.Filter, bool) {
	q := r.URL.Query()
	for k := range q {
		if !shareFilterKeys[k] && !containsString(extra, k) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid filter %q", k))
			return catalog.Filter{}, false
		}
	}
	caps := req.sh.Capabilities
	for _, c := range []struct {
		key     string
		allowed bool
		why     string
	}{
		{"rating", caps.Rate, "not allowed in this share"},
		{"gps", caps.Location, "not allowed in this share"},
		{"album", req.auth.wholeLibrary(), "this share is one album"},
		{"tag", req.auth.wholeLibrary(), "this share is one album"},
	} {
		if q.Has(c.key) && !c.allowed {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid filter %q: %s", c.key, c.why))
			return catalog.Filter{}, false
		}
	}
	if len(q.Get("camera")) > maxShareCamera {
		writeError(w, http.StatusBadRequest, `invalid filter "camera": too long`)
		return catalog.Filter{}, false
	}
	f, uf, err := parseFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return f, false
	}
	return f, s.shareScope(w, r, req, l, &f, uf)
}

// shareScope narrows f (and its user.db part uf) to the share's scope
// and resolves uf into f.Hashes. It answers itself when it returns false.
func (s *Server) shareScope(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles, f *catalog.Filter, uf userFilter) bool {
	var fe *errFilter
	switch err := req.auth.restrict(r.Context(), l, f, &uf); {
	case errors.As(err, &fe):
		writeError(w, http.StatusBadRequest, fe.Error())
		return false
	case errors.Is(err, errScopeMissing):
		shareNotFound(w)
		return false
	case err != nil:
		s.internalError(w, r, err)
		return false
	}
	if err := resolveFilter(r.Context(), l, f, uf); err != nil {
		s.internalError(w, r, err)
		return false
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// handleShareTimeline is GET /api/l/{lib}/timeline, scope-restricted.
func (s *Server) handleShareTimeline(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	f, ok := s.shareFilter(w, r, req, l)
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

// handleShareMediaList is GET /api/l/{lib}/media?year&month: the admin's
// items, scope-restricted, without rating (no rate) and gps (no
// location).
func (s *Server) handleShareMediaList(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	f, ok := s.shareFilter(w, r, req, l, "year", "month")
	if !ok {
		return
	}
	q := r.URL.Query()
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
	shareRedactItems(items, req)
	writeJSON(w, http.StatusOK, itemsJSON{Items: items})
}

// shareRedactItems drops what a phone's list items may not carry:
// deletedAt always, rating without rate, gps without location.
func shareRedactItems(items []itemJSON, req *shareReq) {
	caps := req.sh.Capabilities
	for i := range items {
		items[i].DeletedAt = nil
		if !caps.Rate {
			items[i].Rating = 0
		}
		if !caps.Location {
			items[i].GPS = false
		}
	}
}

// shareVisibleRow checks a live row of l against the scope: false (and a
// 404) when it is not visible.
func (s *Server) shareVisibleRow(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles, m catalog.Media) bool {
	if !m.DeletedAt.IsZero() {
		shareNotFound(w)
		return false
	}
	ok, err := req.auth.visible(r.Context(), l, m)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	if !ok {
		shareNotFound(w)
	}
	return ok
}

// shareRow is the live, visible row named by {id}, or a 404.
func (s *Server) shareRow(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) (catalog.Media, bool) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		shareNotFound(w)
		return catalog.Media{}, false
	}
	m, err := l.db.GetMedia(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		shareNotFound(w)
		return m, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return m, false
	}
	return m, s.shareVisibleRow(w, r, req, l, m)
}

// shareHashRow is the live, visible row with content hash h, or a 404.
func (s *Server) shareHashRow(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles, h uint64) (catalog.Media, bool) {
	m, ok, err := l.db.LiveByHash(r.Context(), h)
	if err != nil {
		s.internalError(w, r, err)
		return m, false
	}
	if !ok {
		shareNotFound(w)
		return m, false
	}
	return m, s.shareVisibleRow(w, r, req, l, m)
}

// shareRendition serves a rendition with the admin handler after the
// row check of {hash}.
func (s *Server) shareRendition(h func(http.ResponseWriter, *http.Request, *libHandles)) shareLibHandler {
	return func(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
		hash, ok := parseHash(r.PathValue("hash"))
		if !ok {
			shareNotFound(w)
			return
		}
		if _, ok := s.shareHashRow(w, r, req, l, hash); !ok {
			return
		}
		h(w, r, l)
	}
}

// shareDetailJSON is the redacted media detail. lat/lon need location,
// rating needs rate, albums are sent in library shares only.
type shareDetailJSON struct {
	ID          int64      `json:"id"`
	Hash        string     `json:"hash"`
	Type        string     `json:"type"`
	TakenAt     *string    `json:"takenAt"`
	Offset      *int       `json:"offset"`
	W           int        `json:"w"`
	H           int        `json:"h"`
	Dur         int64      `json:"dur"`
	Size        int64      `json:"size"`
	Ext         string     `json:"ext"`
	FileName    string     `json:"fileName"`
	CameraMake  string     `json:"cameraMake"`
	CameraModel string     `json:"cameraModel"`
	Lens        string     `json:"lens"`
	Displayable bool       `json:"displayable"`
	ThumbAt     int64      `json:"thumbAt"`
	Lat         *float64   `json:"lat,omitempty"`
	Lon         *float64   `json:"lon,omitempty"`
	Rating      *int       `json:"rating,omitempty"`
	Albums      *[]refJSON `json:"albums,omitempty"`
}

// handleShareMediaDetail is GET /api/l/{lib}/media/{id}: the redacted
// detail of a visible row.
func (s *Server) handleShareMediaDetail(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	m, ok := s.shareRow(w, r, req, l)
	if !ok {
		return
	}
	ctx := r.Context()
	caps := req.sh.Capabilities
	d := shareDetailJSON{ID: m.ID, Hash: hexHash(m.Hash), Type: m.FileType, TakenAt: wallClock(m.TakenAt),
		W: m.Width, H: m.Height, Dur: m.DurationMS, Size: m.Size, Ext: m.Ext, FileName: path.Base(m.RelPath),
		CameraMake: m.CameraMake, CameraModel: m.CameraModel, Lens: m.Lens,
		Displayable: caps.Download && displayable(m.Ext), ThumbAt: unixOrZero(m.ThumbAt)}
	if m.OffsetKnown && !m.TakenAt.IsZero() {
		_, off := m.TakenAt.Zone()
		d.Offset = &off
	}
	if caps.Location && m.HasGPS {
		lat, lon := m.Lat, m.Lon
		d.Lat, d.Lon = &lat, &lon
	}
	if caps.Rate {
		rating, err := l.user.Rating(ctx, m.Hash)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		d.Rating = &rating
	}
	if req.auth.wholeLibrary() {
		albums, err := l.user.AlbumsOf(ctx, m.Hash)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		refs := make([]refJSON, 0, len(albums))
		for _, a := range albums {
			refs = append(refs, refJSON{ID: a.ID, Name: a.Name})
		}
		d.Albums = &refs
	}
	writeJSON(w, http.StatusOK, d)
}

// errNotAllowed is the 403 of a capability the share does not have.
const errNotAllowed = "not allowed in this share"

// handleShareFile is GET /api/l/{lib}/file/{id}[?download=1]: the
// original of a visible row. A video inline needs video; an image, or
// any download, needs download. Never cached.
func (s *Server) handleShareFile(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	download := false
	switch r.URL.Query().Get("download") {
	case "", "0":
	case "1":
		download = true
	default:
		writeError(w, http.StatusBadRequest, "invalid download value: want 1")
		return
	}
	m, ok := s.shareRow(w, r, req, l)
	if !ok {
		return
	}
	caps := req.sh.Capabilities
	allowed := caps.Download
	if m.FileType == "video" && !download {
		allowed = caps.Video
	}
	if !allowed {
		writeError(w, http.StatusForbidden, errNotAllowed)
		return
	}
	s.serveOriginal(w, r, l, m, download, "private, no-store")
}

// shareAlbumJSON is an album of a library share.
type shareAlbumJSON struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Count int64   `json:"count"`
	Cover *string `json:"cover"`
}

// handleShareAlbums is GET /api/l/{lib}/albums (library shares only).
func (s *Server) handleShareAlbums(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	if !req.auth.wholeLibrary() {
		shareNotFound(w)
		return
	}
	albums, err := l.user.Albums(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]shareAlbumJSON, 0, len(albums))
	for _, a := range albums {
		j, err := s.newAlbum(r, l, a)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, shareAlbumJSON{ID: j.ID, Name: j.Name, Count: j.Count, Cover: j.Cover})
	}
	writeJSON(w, http.StatusOK, struct {
		Albums []shareAlbumJSON `json:"albums"`
	}{out})
}

// handleShareRating is POST /api/l/{lib}/ratings {hashes: [h], rating}:
// one visible hash (404 otherwise), only with rate (403), refused during
// a move (409), logged with the device and the share.
func (s *Server) handleShareRating(w http.ResponseWriter, r *http.Request, req *shareReq, l *libHandles) {
	var body ratingsBody
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.Hashes) != 1 {
		writeError(w, http.StatusBadRequest, "hashes: want exactly one hash")
		return
	}
	hash, ok := parseHash(body.Hashes[0])
	if !ok {
		writeError(w, http.StatusBadRequest, "hashes: invalid hash: want 16 lower-case hex digits")
		return
	}
	if body.Rating == nil || *body.Rating < 0 || *body.Rating > 5 {
		writeError(w, http.StatusBadRequest, "rating: want 0-5 (0 clears)")
		return
	}
	if _, ok := s.shareHashRow(w, r, req, l, hash); !ok {
		return
	}
	if !req.sh.Capabilities.Rate {
		writeError(w, http.StatusForbidden, errNotAllowed)
		return
	}
	if l.entry.moving.Load() > 0 {
		writeError(w, http.StatusConflict, errMoving.Error())
		return
	}
	ctx := r.Context()
	old, err := l.user.Rating(ctx, hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	n, err := l.user.SetRating(ctx, []uint64{hash}, *body.Rating)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	s.log.Info(fmt.Sprintf("rating %d → %d on %s in %s by device %q (share %q)", old, *body.Rating, hexHash(hash),
		l.id, req.dev.Name, req.sh.Name), "device", req.dev.ID, "share", req.sh.ID)
	writeJSON(w, http.StatusOK, updatedJSON{Updated: n})
}
