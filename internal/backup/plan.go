package backup

import (
	"context"
	"sort"

	"picmover/internal/catalog"
	"picmover/internal/library"
	"picmover/internal/userdata"
)

// Plan classes.
const (
	ClassCurrent = "current" // same path, same content: nothing to do
	ClassMoved   = "moved"   // the content is on the target under a path the library no longer has: rename there
	ClassChanged = "changed" // same path, other content: keep the old file in previous/, copy the new one
	ClassNew     = "new"     // not on the target: copy
	ClassExtra   = "extra"   // on the target, neither the path nor the content is in the library: report (prune removes)
)

// Count is a number of files and their bytes.
type Count struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

func (c *Count) add(size int64) { c.Files++; c.Bytes += size }

// Item is one action of a plan.
type Item struct {
	Class string
	Entry
	From string   // moved: the set path the content is at now
	Row  *FileRow // changed: the row at the path
}

// Plan is what a backup run would do.
type Plan struct {
	Current, Moved, Changed, New, Extra Count
	// Items are the moved, changed and new files: moves first (they are
	// renames on the target), then copies in the library's order (newest
	// first).
	Items  []Item
	Extras []FileRow // sorted by path
	// CurrentPaths are the first MaxExamples current files.
	CurrentPaths []string
	Symlinks     []string
	Missing      []string    // catalog rows whose file is missing in the library
	Invalid      []string    // catalog rows whose path is not a library path
	Errors       []Failure   // what the library walk could not read (the scan is incomplete)
	Collisions   []Collision // files left out: their names differ from another's only in case or Unicode form
	// Restore is what "restore --mode missing" copies back: the set's
	// rows whose path the library does not have and whose content it
	// does not have under another path. RestorePaths are the first
	// MaxExamples of them, by path.
	Restore      Count
	RestorePaths []string
	// LibraryHashes is every content hash the library has: the catalog
	// (live and trashed) and the non-media files with a known hash.
	LibraryHashes map[uint64]struct{}
	// LibraryPaths are the NOCASE keys of the library's set paths.
	LibraryPaths map[string]struct{}
}

// MaxExamples caps the example paths a plan keeps per class.
const MaxExamples = 500

func (p *Plan) addCurrent(rel string) {
	if len(p.CurrentPaths) < MaxExamples {
		p.CurrentPaths = append(p.CurrentPaths, rel)
	}
}

// CopyBytes is the number of bytes a run copies: new and changed files.
func (p *Plan) CopyBytes() int64 { return p.New.Bytes + p.Changed.Bytes }

// MakePlan classifies the library's files against the set's rows. It is
// pure: no file is read.
//
//   - current: a row at the same path (NOCASE) with the same hash; for a
//     non-media file, the same size and mtime (its hash is then the row's).
//   - changed: a row at the same path with another hash (or, non-media,
//     another size or mtime: the run hashes it and only copies when the
//     content differs).
//   - moved: no row at the path, but a row with the hash at a path the
//     library no longer has (each such row serves one move).
//   - new: everything else.
//   - extra: rows whose path is not in the library and whose hash is not
//     in it either (and that no move used).
func MakePlan(sc *Scan, rows []FileRow) *Plan {
	p := &Plan{
		Symlinks:      sc.Symlinks,
		Missing:       sc.Missing,
		Invalid:       sc.Invalid,
		Errors:        sc.Errors,
		Collisions:    sc.Collisions,
		LibraryHashes: make(map[uint64]struct{}, len(sc.CatalogHashes)+len(sc.Entries)),
		LibraryPaths:  make(map[string]struct{}, len(sc.Entries)),
	}
	for h := range sc.CatalogHashes {
		p.LibraryHashes[h] = struct{}{}
	}
	byKey := make(map[string]*FileRow, len(rows))
	for i := range rows {
		byKey[keyOf(rows[i].RelPath)] = &rows[i]
	}
	for _, e := range sc.Entries {
		p.LibraryPaths[keyOf(e.Rel)] = struct{}{}
	}
	// Files left out by a collision are still the library's: their
	// earlier copies are neither extras nor to be restored.
	present := make(map[uint64]struct{}, len(sc.Entries))
	for _, e := range sc.Collided {
		p.LibraryPaths[keyOf(e.Rel)] = struct{}{}
		if e.HashKnown {
			p.LibraryHashes[e.Hash] = struct{}{}
			present[e.Hash] = struct{}{}
		}
	}
	// Rows whose path the library no longer has, by hash: move sources.
	gone := map[uint64][]*FileRow{}
	for i := range rows {
		r := &rows[i]
		if _, ok := p.LibraryPaths[keyOf(r.RelPath)]; !ok {
			gone[r.Hash] = append(gone[r.Hash], r)
		}
	}
	for _, list := range gone {
		sort.Slice(list, func(i, j int) bool { return list[i].RelPath < list[j].RelPath })
	}
	used := map[*FileRow]bool{}
	var moves, copies []Item
	for _, e := range sc.Entries {
		r := byKey[keyOf(e.Rel)]
		switch {
		case r != nil && e.Media && r.Hash == e.Hash:
			p.Current.add(e.Size)
			p.addCurrent(e.Rel)
		case r != nil && !e.Media && r.Size == e.Size && r.MTime == e.MTime:
			e.Hash, e.HashKnown = r.Hash, true
			p.Current.add(e.Size)
			p.addCurrent(e.Rel)
		case r != nil:
			p.Changed.add(e.Size)
			copies = append(copies, Item{Class: ClassChanged, Entry: e, Row: r})
		default:
			var from *FileRow
			if e.HashKnown {
				for _, c := range gone[e.Hash] {
					if !used[c] {
						from = c
						break
					}
				}
			}
			if from != nil {
				used[from] = true
				p.Moved.add(e.Size)
				moves = append(moves, Item{Class: ClassMoved, Entry: e, From: from.RelPath})
			} else {
				p.New.add(e.Size)
				copies = append(copies, Item{Class: ClassNew, Entry: e})
			}
		}
		if e.HashKnown {
			p.LibraryHashes[e.Hash] = struct{}{}
			present[e.Hash] = struct{}{}
		}
	}
	p.Items = append(moves, copies...)
	var restore []string
	for i := range rows {
		r := &rows[i]
		if _, ok := p.LibraryPaths[keyOf(r.RelPath)]; ok {
			continue
		}
		// Restore looks at the files present: a catalog row whose file
		// is missing does not count as having the content.
		if _, ok := present[r.Hash]; !ok {
			p.Restore.add(r.Size)
			restore = append(restore, r.RelPath)
		}
		if used[r] {
			continue
		}
		if _, ok := p.LibraryHashes[r.Hash]; ok {
			continue
		}
		p.Extra.add(r.Size)
		p.Extras = append(p.Extras, *r)
	}
	sort.Slice(p.Extras, func(i, j int) bool { return p.Extras[i].RelPath < p.Extras[j].RelPath })
	sort.Strings(restore)
	if len(restore) > MaxExamples {
		restore = restore[:MaxExamples]
	}
	p.RestorePaths = restore
	return p
}

// Source is the library side of a backup: the library, its catalog (nil
// when it has none) and its user.db (nil: no user.db snapshot).
type Source struct {
	Lib     *library.Library
	Catalog *catalog.DB
	User    *userdata.DB
}

// PlanFor scans the library and reads the set (nil: no set yet) and
// returns the plan.
func PlanFor(ctx context.Context, src Source, set *Set) (*Plan, error) {
	sc, err := scanSource(ctx, src)
	if err != nil {
		return nil, err
	}
	var rows []FileRow
	if set != nil {
		if rows, err = set.Files(ctx); err != nil {
			return nil, err
		}
	}
	return MakePlan(sc, rows), nil
}

func scanSource(ctx context.Context, src Source) (*Scan, error) {
	var rows []catalog.PathRow
	if src.Catalog != nil {
		var err error
		if rows, err = src.Catalog.PathRows(ctx); err != nil {
			return nil, err
		}
	}
	return ScanLibrary(ctx, src.Lib, rows)
}
