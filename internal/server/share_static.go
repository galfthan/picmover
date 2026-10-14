package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// shareAppFile is the phone app's page in the built frontend (a second
// Vite entry next to index.html).
const shareAppFile = "share.html"

// shareNotBuilt is the page served when the binary's frontend has no
// phone app.
const shareNotBuilt = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>picmover</title></head>
<body style="font-family: sans-serif; margin: 2em; line-height: 1.4">
<h1>picmover</h1>
<p>The phone app is not built into this picmover. Build the web frontend with <code>make web</code>
(it needs Node) and rebuild picmover.</p>
</body></html>
`

// handleShareStatic serves one file of the phone app: share.html for /
// and /share.html, /favicon.png, /manifest.webmanifest (the patterns of
// shareRouteTable). The name comes from the route, never from the
// request's path.
func (s *Server) handleShareStatic(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.serveShareFile(w, r, name) }
}

// handleShareStaticDir serves a file under dir ("assets", "icons") named
// by the route's {file...} wildcard. The wildcard is unescaped, so an
// escaped separator ("..%2Fsecret.txt") arrives as a slash: any empty,
// "." or ".." segment, a backslash, and an escaped slash or backslash in the raw
// path are refused, and the name must stay under dir.
func (s *Server) handleShareStaticDir(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		raw := strings.ToLower(r.URL.EscapedPath())
		if file == "" || strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.ContainsRune(file, '\\') {
			shareNotFound(w)
			return
		}
		for _, seg := range strings.Split(file, "/") {
			if seg == "" || seg == "." || seg == ".." {
				shareNotFound(w)
				return
			}
		}
		name := dir + "/" + file
		if path.Clean(name) != name || !strings.HasPrefix(name, dir+"/") {
			shareNotFound(w)
			return
		}
		s.serveShareFile(w, r, name)
	}
}

// serveShareFile serves name from the frontend: never index.html, no
// hidden files; share.html falls back to the "not built" page.
func (s *Server) serveShareFile(w http.ResponseWriter, r *http.Request, name string) {
	if s.dist == nil {
		if name == shareAppFile {
			s.serveShareNotBuilt(w)
			return
		}
		shareNotFound(w)
		return
	}
	if !fs.ValidPath(name) || hasDotSegment(name) || name == "index.html" {
		shareNotFound(w)
		return
	}
	st, err := fs.Stat(s.dist, name)
	if err != nil || st.IsDir() {
		if name == shareAppFile {
			s.serveShareNotBuilt(w)
			return
		}
		shareNotFound(w)
		return
	}
	cache := "no-cache"
	if strings.HasPrefix(name, "assets/") {
		cache = "public, max-age=31536000, immutable"
	}
	serveStaticFile(s, w, r, s.dist, name, cache, shareCSP)
}

func (s *Server) serveShareNotBuilt(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", shareCSP)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(shareNotBuilt))
}
