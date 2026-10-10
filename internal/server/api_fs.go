package server

import (
	"cmp"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"picmover/internal/metadata"
)

type dirJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type fsRootsJSON struct {
	Path string    `json:"path"` // always ""
	Dirs []dirJSON `json:"dirs"`
}

type fsDirJSON struct {
	Path       string    `json:"path"`
	Parent     string    `json:"parent"` // "" at a root
	Dirs       []dirJSON `json:"dirs"`
	MediaFiles int       `json:"mediaFiles"`
	OtherFiles int       `json:"otherFiles"`
}

// handleFS is the import source picker's directory browser. It lists
// directories only (never file names), with the number of media and
// other files directly inside. Hidden entries are omitted.
func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		dirs := roots()
		if dirs == nil {
			dirs = []dirJSON{}
		}
		writeJSON(w, http.StatusOK, fsRootsJSON{Path: "", Dirs: dirs})
		return
	}
	if err := ValidSourcePath(p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, code, err := listDir(p)
	if err != nil {
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listDir lists the directory p (absolute, already checked by
// ValidSourcePath: no UNC or device path). On failure it returns the
// status code to answer with.
func listDir(p string) (fsDirJSON, int, error) {
	if !filepath.IsAbs(p) {
		return fsDirJSON{}, http.StatusBadRequest, errors.New("path must be absolute")
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fsDirJSON{}, http.StatusNotFound, err
	case err != nil:
		return fsDirJSON{}, http.StatusBadRequest, err
	case !st.IsDir():
		return fsDirJSON{}, http.StatusBadRequest, errors.New(p + ": not a directory")
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return fsDirJSON{}, http.StatusBadRequest, err
	}
	out := fsDirJSON{Path: p, Dirs: []dirJSON{}}
	if parent := filepath.Dir(p); parent != p {
		out.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(p, name)
		if strings.HasPrefix(name, ".") || isHidden(e, full) {
			continue
		}
		typ := e.Type()
		isDir := typ.IsDir()
		if typ&fs.ModeSymlink != 0 {
			// Listed as a directory when it points to one; the counts
			// below never follow it.
			if t, err := os.Stat(full); err == nil && t.IsDir() {
				isDir = true
			}
		}
		switch {
		case isDir:
			out.Dirs = append(out.Dirs, dirJSON{Name: name, Path: full})
		case isMediaName(name):
			out.MediaFiles++
		default:
			out.OtherFiles++
		}
	}
	slices.SortFunc(out.Dirs, func(a, b dirJSON) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Name, b.Name))
	})
	return out, 0, nil
}

// isMediaName reports whether the importer would take the file: a media
// extension or a zip archive.
func isMediaName(name string) bool {
	if _, ok := metadata.Classify(name); ok {
		return true
	}
	return strings.EqualFold(filepath.Ext(name), ".zip")
}

// existingDirs returns the entries of candidates that are directories.
func existingDirs(candidates ...string) []dirJSON {
	var out []dirJSON
	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			seen[c] = true
			out = append(out, dirJSON{Name: c, Path: c})
		}
	}
	return out
}
