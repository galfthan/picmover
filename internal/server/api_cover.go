package server

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"picmover/internal/library"
	"picmover/internal/thumbs"
)

// coverCache is the Cache-Control of library cover thumbnails.
const coverCache = "private, max-age=3600"

// handleLibraryCover serves the 256 px thumbnail of a library's cover
// item (the newest live row with a thumbnail) for a library in any
// readable state, closed included: the catalog is peeked read-only (the
// cached peek of the listing) and the thumbnail store opened read-only,
// without the library lock. ETag "<hash>-<FormatVersion>-<thumbAt or 0>",
// Cache-Control private, max-age=3600. 404 when the id is unknown, the
// catalog unreadable, or there is no cover or no thumbnail.
func (s *Server) handleLibraryCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("lib")
	if !validLibID(id) {
		writeError(w, http.StatusNotFound, errLibNotFound.Error())
		return
	}
	l, err := s.ws.find(id)
	if errors.Is(err, errLibNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !l.library {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	pk, err := s.ws.peek(r.Context(), filepath.Join(l.path, library.DBName))
	if err != nil || !pk.HasCover {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	lib, err := library.Attach(l.path)
	if err != nil {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	store, err := thumbs.OpenStoreReadOnly(lib)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Debug("cover: thumbnail cache", "id", id, "err", err)
		}
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	defer store.Close()
	data, _, _, found, err := store.Thumb(r.Context(), pk.Cover)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	v := "0"
	if !pk.CoverThumbAt.IsZero() {
		v = strconv.FormatInt(pk.CoverThumbAt.Unix(), 10)
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", renditionETag(pk.Cover, v))
	h.Set("Cache-Control", coverCache)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}
