package backup

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"picmover/internal/catalog"
	"picmover/internal/library"
)

// Entry is one file of the library to back up.
type Entry struct {
	Rel       string // set path: the library-relative path (trashed rows under .picmover/trash/)
	Hash      uint64
	HashKnown bool // media (from the catalog), or a non-media file matched by size and mtime
	Size      int64
	MTime     int64 // Unix seconds; 0 for media (not stat'ed by the scan)
	Media     bool
	Trashed   bool
}

// Scan is the library side of a plan: the files to back up in copy
// order and what was left out.
type Scan struct {
	Entries  []Entry
	Symlinks []string // set paths of symbolic links (never followed, never backed up)
	Missing  []string // catalog rows whose file is not in the library
	Invalid  []string // catalog rows whose path is not a library path
	// CatalogHashes are the content hashes of every catalog row, live or
	// trashed.
	CatalogHashes map[uint64]struct{}
}

// ScanLibrary walks the library: the catalog rows (newest first, the copy
// order) matched against the files on disk, then every other regular
// file (non-media: sidecars, notes) under the root, newest first.
// Skipped: picmover's own files (.picmover except its trash, media.db and
// user.db with their side files and backups, import_*.log at the root),
// symbolic links (listed) and other non-regular files. rows is
// catalog.PathRows; nil means the library has no catalog.
func ScanLibrary(ctx context.Context, lib *library.Library, rows []catalog.PathRow) (*Scan, error) {
	sc := &Scan{CatalogHashes: make(map[uint64]struct{}, len(rows))}
	type rowRef struct {
		idx   int
		found bool
	}
	byPath := make(map[string]*rowRef, len(rows))
	media := make([]Entry, 0, len(rows))
	for _, r := range rows {
		sc.CatalogHashes[r.Hash] = struct{}{}
		rel := r.RelPath
		if r.Trashed {
			rel = trashPrefix + rel
		}
		if !validLibRel(r.RelPath) || !validSetPath(rel) {
			sc.Invalid = append(sc.Invalid, r.RelPath)
			continue
		}
		byPath[rel] = &rowRef{idx: len(media)}
		media = append(media, Entry{Rel: rel, Hash: r.Hash, HashKnown: true, Size: r.Size, Media: true, Trashed: r.Trashed})
	}
	var other []Entry
	root := lib.Root()
	trashDir := lib.TrashDir()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == root {
				return err
			}
			// An unreadable folder or a file that vanished: skip it.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		inTrash := p == trashDir || strings.HasPrefix(p, trashDir+string(filepath.Separator))
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == library.MetaDir || inTrash {
				return nil
			}
			if lib.IsInternal(p) {
				return fs.SkipDir
			}
			return nil
		}
		if !inTrash && lib.IsInternal(p) {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			sc.Symlinks = append(sc.Symlinks, rel)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !validSetPath(rel) {
			return nil
		}
		if ref, ok := byPath[rel]; ok {
			ref.found = true
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		other = append(other, Entry{Rel: rel, Size: fi.Size(), MTime: fi.ModTime().Unix(), Trashed: inTrash})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, e := range media {
		if byPath[e.Rel].found {
			sc.Entries = append(sc.Entries, e)
		} else {
			sc.Missing = append(sc.Missing, e.Rel)
		}
	}
	slices.SortStableFunc(other, func(a, b Entry) int {
		switch {
		case a.MTime > b.MTime:
			return -1
		case a.MTime < b.MTime:
			return 1
		}
		return strings.Compare(a.Rel, b.Rel)
	})
	sc.Entries = append(sc.Entries, other...)
	return sc, nil
}

// validLibRel reports whether a catalog rel_path is a library path:
// slash-separated components, none empty, "." or "..", none containing
// \, : or NUL, and not inside .picmover (the trash is reached through
// trashed rows only).
func validLibRel(rel string) bool {
	if rel == "" {
		return false
	}
	for _, c := range strings.Split(rel, "/") {
		if c == "" || c == "." || c == ".." || strings.ContainsAny(c, "\\:\x00") {
			return false
		}
	}
	first, _, _ := strings.Cut(rel, "/")
	return strings.ToLower(strings.TrimRight(first, ". ")) != library.MetaDir
}
