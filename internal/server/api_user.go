package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"picmover/internal/userdata"
)

// maxBodyHashes caps the hash list of one mutation; the client chunks
// larger selections.
const maxBodyHashes = 2000

// maxIgnoreHashes caps similar/ignore: every pair among the hashes is
// stored, so n hashes make n(n-1)/2 rows.
const maxIgnoreHashes = 200

// maxBodyIDs caps tag and album id lists in a body.
const maxBodyIDs = 100

// decodeBody decodes a JSON request body into v. Unknown fields are
// ignored; malformed JSON is a 400 with a generic message, an oversized
// body a 413. It answers itself when it returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// parseHashes parses a body's hash list (16 lower-case hex digits each,
// at most limit), dropping duplicates and keeping the order.
func parseHashes(field string, list []string, limit int) ([]uint64, error) {
	if len(list) > limit {
		return nil, fmt.Errorf("%s: at most %d hashes per request", field, limit)
	}
	out := make([]uint64, 0, len(list))
	seen := make(map[uint64]bool, len(list))
	for _, s := range list {
		h, ok := parseHash(s)
		if !ok {
			return nil, fmt.Errorf("%s: invalid hash %q: want 16 lower-case hex digits", field, s)
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out, nil
}

// checkIDs checks a body's id list (positive, at most maxBodyIDs).
func checkIDs(field string, ids []int64) error {
	if len(ids) > maxBodyIDs {
		return fmt.Errorf("%s: at most %d ids", field, maxBodyIDs)
	}
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("%s: invalid id %d", field, id)
		}
	}
	return nil
}

// userError answers the userdata errors: 400 for invalid input, 404 for
// unknown ids, 409 for a taken name, 500 otherwise.
func (s *Server) userError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, userdata.ErrInvalidName), errors.Is(err, userdata.ErrInvalidRating):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, userdata.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, userdata.ErrNameTaken):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, r, err)
	}
}

// pathID parses the positive id path value name or answers 404.
func pathID(w http.ResponseWriter, r *http.Request, name, what string) (int64, bool) {
	id, ok := parseID(r.PathValue(name))
	if !ok {
		writeError(w, http.StatusNotFound, "no such "+what)
	}
	return id, ok
}

type ratingsBody struct {
	Hashes []string `json:"hashes"`
	Rating *int     `json:"rating"`
}

type updatedJSON struct {
	Updated int64 `json:"updated"`
}

// handleRatings sets the rating (0 clears) of up to 2000 hashes.
func (s *Server) handleRatings(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body ratingsBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Rating == nil || *body.Rating < 0 || *body.Rating > 5 {
		writeError(w, http.StatusBadRequest, "rating: want 0-5 (0 clears)")
		return
	}
	n, err := l.user.SetRating(r.Context(), hashes, *body.Rating)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updatedJSON{Updated: n})
}

type tagJSON struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func newTag(t userdata.Tag) tagJSON { return tagJSON{ID: t.ID, Name: t.Name, Count: t.Count} }

type tagsJSON struct {
	Tags []tagJSON `json:"tags"`
}

func tagList(ts []userdata.Tag) tagsJSON {
	out := tagsJSON{Tags: make([]tagJSON, 0, len(ts))}
	for _, t := range ts {
		out.Tags = append(out.Tags, newTag(t))
	}
	return out
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, l *libHandles) {
	tags, err := l.user.Tags(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tagList(tags))
}

type nameBody struct {
	Name string `json:"name"`
}

// handleTagCreate creates a tag: 201, or 200 with the existing tag of
// that name (case-insensitively).
func (s *Server) handleTagCreate(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body nameBody
	if !decodeBody(w, r, &body) {
		return
	}
	t, created, err := l.user.CreateTag(r.Context(), body.Name)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, newTag(t))
}

// handleTagRename renames a tag; 409 when the name is taken.
func (s *Server) handleTagRename(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "tid", "tag")
	if !ok {
		return
	}
	var body nameBody
	if !decodeBody(w, r, &body) {
		return
	}
	t, err := l.user.RenameTag(r.Context(), id, body.Name)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newTag(t))
}

type removedJSON struct {
	Removed int64 `json:"removed"`
}

func (s *Server) handleTagDelete(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "tid", "tag")
	if !ok {
		return
	}
	n, err := l.user.DeleteTag(r.Context(), id)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, removedJSON{Removed: n})
}

type mediaTagsBody struct {
	Hashes []string `json:"hashes"`
	Add    []int64  `json:"add"`
	Remove []int64  `json:"remove"`
	Create []string `json:"create"`
}

// handleMediaTags adds, creates and removes tags on up to 2000 hashes in
// one transaction and returns the tags added (with their new counts).
func (s *Server) handleMediaTags(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body mediaTagsBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err == nil {
		err = checkIDs("add", body.Add)
	}
	if err == nil {
		err = checkIDs("remove", body.Remove)
	}
	if err == nil && len(body.Create) > maxBodyIDs {
		err = fmt.Errorf("create: at most %d names", maxBodyIDs)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	added, err := l.user.TagMedia(r.Context(), hashes, body.Add, body.Remove, body.Create)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tagList(added))
}

// albumJSON is an album; cover is a hash or null for an empty album.
type albumJSON struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Count     int64   `json:"count"`
	Cover     *string `json:"cover"`
	CreatedAt string  `json:"createdAt"` // RFC 3339
	UpdatedAt string  `json:"updatedAt"`
}

// newAlbum returns the album object. Count and cover consider only live
// members (a row in the catalog, not trashed): the explicit cover while it
// is a live member, else the newest live member by capture time; null
// when no member is live.
func (s *Server) newAlbum(r *http.Request, l *libHandles, a userdata.Album) (albumJSON, error) {
	out := albumJSON{ID: a.ID, Name: a.Name,
		CreatedAt: a.CreatedAt.Local().Format(time.RFC3339), UpdatedAt: a.UpdatedAt.Local().Format(time.RFC3339)}
	if a.Count == 0 {
		return out, nil
	}
	hashes, err := l.user.AlbumHashes(r.Context(), a.ID)
	if err != nil {
		return out, err
	}
	live, err := l.db.LiveHashes(r.Context(), hashes)
	if err != nil {
		return out, err
	}
	out.Count = int64(len(live))
	if len(live) == 0 {
		return out, nil
	}
	cover := a.Cover
	if !a.CoverSet || !live[cover] {
		h, found, err := l.db.NewestOf(r.Context(), hashes)
		if err != nil {
			return out, err
		}
		if !found {
			return out, nil
		}
		cover = h
	}
	c := hexHash(cover)
	out.Cover = &c
	return out, nil
}

// liveAlbumCount is the number of live members of album id.
func liveAlbumCount(r *http.Request, l *libHandles, id int64) (int64, error) {
	hashes, err := l.user.AlbumHashes(r.Context(), id)
	if err != nil {
		return 0, err
	}
	live, err := l.db.LiveHashes(r.Context(), hashes)
	return int64(len(live)), err
}

type albumsJSON struct {
	Albums []albumJSON `json:"albums"`
}

func (s *Server) handleAlbums(w http.ResponseWriter, r *http.Request, l *libHandles) {
	albums, err := l.user.Albums(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := albumsJSON{Albums: make([]albumJSON, 0, len(albums))}
	for _, a := range albums {
		j, err := s.newAlbum(r, l, a)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.Albums = append(out.Albums, j)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeAlbum(w http.ResponseWriter, r *http.Request, l *libHandles, code int, a userdata.Album) {
	j, err := s.newAlbum(r, l, a)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, code, j)
}

type albumCreateBody struct {
	Name   string   `json:"name"`
	Hashes []string `json:"hashes"`
}

func (s *Server) handleAlbumCreate(w http.ResponseWriter, r *http.Request, l *libHandles) {
	var body albumCreateBody
	if !decodeBody(w, r, &body) {
		return
	}
	hashes, err := parseHashes("hashes", body.Hashes, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := l.user.CreateAlbum(r.Context(), body.Name, hashes)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	s.writeAlbum(w, r, l, http.StatusCreated, a)
}

type albumUpdateBody struct {
	Name  *string         `json:"name"`
	Cover json.RawMessage `json:"cover"` // absent: unchanged; null: cleared; a hash: set
}

func (s *Server) handleAlbumUpdate(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "aid", "album")
	if !ok {
		return
	}
	var body albumUpdateBody
	if !decodeBody(w, r, &body) {
		return
	}
	u := userdata.AlbumUpdate{Name: body.Name}
	switch c := string(body.Cover); {
	case c == "":
	case c == "null":
		u.ClearCover = true
	default:
		var hex string
		h, ok := uint64(0), false
		if json.Unmarshal(body.Cover, &hex) == nil {
			h, ok = parseHash(hex)
		}
		if !ok {
			writeError(w, http.StatusBadRequest, "cover: want a hash (16 lower-case hex digits) or null")
			return
		}
		u.Cover = &h
	}
	a, err := l.user.UpdateAlbum(r.Context(), id, u)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	s.writeAlbum(w, r, l, http.StatusOK, a)
}

func (s *Server) handleAlbumDelete(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "aid", "album")
	if !ok {
		return
	}
	if err := l.user.DeleteAlbum(r.Context(), id); err != nil {
		s.userError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

type albumItemsBody struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

type countJSON struct {
	Count int64 `json:"count"`
}

func (s *Server) handleAlbumItems(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "aid", "album")
	if !ok {
		return
	}
	var body albumItemsBody
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.Add)+len(body.Remove) > maxBodyHashes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("add and remove: at most %d hashes per request", maxBodyHashes))
		return
	}
	add, err := parseHashes("add", body.Add, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	remove, err := parseHashes("remove", body.Remove, maxBodyHashes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := l.user.AlbumEdit(r.Context(), id, add, remove); err != nil {
		s.userError(w, r, err)
		return
	}
	n, err := liveAlbumCount(r, l, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, countJSON{Count: n})
}
