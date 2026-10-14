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

// handleShareStatic serves the phone app's files: share.html for / and
// /share.html, and the allowlisted /assets/, /icons/, /favicon.png and
// /manifest.webmanifest (the patterns of shareRouteTable). Never
// index.html, and nothing else of the frontend.
func (s *Server) handleShareStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = shareAppFile
	}
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
