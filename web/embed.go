// Package web embeds the built single-page app (web/dist, produced by
// "make web"). The Svelte sources live next to this file; Go never needs
// Node: without a build, dist holds only .gitkeep and Dist reports false.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built frontend rooted at dist/, and true when it holds
// an index.html. It returns nil and false when the frontend was not built.
func Dist() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if st, err := fs.Stat(sub, "index.html"); err != nil || st.IsDir() {
		return nil, false
	}
	return sub, true
}
