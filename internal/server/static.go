package server

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// notBuilt is the answer for the frontend when the binary was built
// without it.
const notBuilt = "picmover: the web frontend is not built into this binary.\n" +
	"Build it with 'make web' (needs Node) and rebuild picmover; the API under /api/ works without it.\n"

// staticTypes are the content types of the frontend's files; the OS mime
// tables are not used (on Windows they come from the registry).
var staticTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "application/javascript; charset=utf-8",
	".mjs": "application/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
	".json": "application/json", ".map": "application/json", ".svg": "image/svg+xml",
	".png": "image/png", ".jpg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".ico": "image/x-icon", ".woff": "font/woff", ".woff2": "font/woff2", ".txt": "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json", ".wasm": "application/wasm",
}

// handleStatic serves the single-page app: a file of the built frontend,
// or index.html for any other path (hash routing does not need it, deep
// links will). A missing file under /assets/, or any missing path whose
// last segment has an extension, is a 404, so a stale page never
// receives HTML for a script. Dot files and directories (dist/.gitkeep)
// are never served.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		// No route matched: an unknown endpoint, or a known one with
		// another method (the "/" pattern takes every method).
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			writeError(w, http.StatusNotFound, "no such endpoint")
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.static == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, notBuilt)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "index.html" {
		s.serveIndex(w, r)
		return
	}
	if !fs.ValidPath(name) || hasDotSegment(name) {
		http.NotFound(w, r)
		return
	}
	st, err := fs.Stat(s.static, name)
	if err != nil || st.IsDir() {
		if name == "assets" || strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		s.serveIndex(w, r)
		return
	}
	cache := "no-cache"
	if strings.HasPrefix(name, "assets/") {
		cache = "public, max-age=31536000, immutable" // Vite puts content hashes in these names
	}
	s.serveFile(w, r, name, cache)
}

// hasDotSegment reports whether a segment of the slash path name starts
// with a dot.
func hasDotSegment(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r, "index.html", "no-cache")
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name, cache string) {
	f, err := s.static.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.log.Error("static file", "name", name, "err", err)
		http.Error(w, internalErrorMsg, http.StatusInternalServerError)
		return
	}
	defer f.Close()
	var modTime time.Time
	if st, err := f.Stat(); err == nil {
		modTime = st.ModTime()
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			s.log.Error("static file", "name", name, "err", err)
			http.Error(w, internalErrorMsg, http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	h := w.Header()
	ct, ok := staticTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", cache)
	if strings.HasPrefix(ct, "text/html") {
		h.Set("Content-Security-Policy", htmlCSP)
	}
	http.ServeContent(w, r, "", modTime, rs)
}
