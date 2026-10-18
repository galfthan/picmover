package importer

import (
	"cmp"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// walkNewestFirst is filepath.WalkDir with one difference: the entries of
// each directory are visited newest first (sortNewestFirst) instead of in
// lexical order. Phone and camera files are named by their capture time
// (IMG_YYYYMMDD_HHMMSS), so a lexical walk reaches the oldest files first;
// on a slow source (a phone over MTP) the newest photos, the ones most
// likely not yet in the library, are then imported last.
//
// Everything else follows filepath.WalkDir exactly, so the set of entries
// visited and the calls to fn are the same, only their order differs: the
// root is Lstat'ed; fn is called for every directory before its entries
// and once more with the error when it cannot be read (the entries read
// before the error are still visited unless fn returns SkipDir); SkipDir
// on a directory skips it, on a file the rest of its directory; SkipAll
// stops the walk. Symbolic links are not followed. readDir lists a
// directory (os.ReadDir; a variable for tests). After ctx is done the
// entries are left in name order (no more stats); fn decides when to stop.
func walkNewestFirst(ctx context.Context, root string, readDir func(string) ([]fs.DirEntry, error), fn fs.WalkDirFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		err = fn(root, nil, err)
	} else {
		err = walkNewestDir(ctx, root, fs.FileInfoToDirEntry(info), readDir, fn)
	}
	if err == filepath.SkipDir || err == filepath.SkipAll {
		return nil
	}
	return err
}

// walkNewestDir walks the entry d at path; it mirrors the walkDir of the
// path/filepath package.
func walkNewestDir(ctx context.Context, path string, d fs.DirEntry, readDir func(string) ([]fs.DirEntry, error), fn fs.WalkDirFunc) error {
	if err := fn(path, d, nil); err != nil || !d.IsDir() {
		if err == filepath.SkipDir && d.IsDir() {
			err = nil // skip the directory
		}
		return err
	}
	ents, err := readDir(path)
	if err != nil {
		// Second call, to report the error.
		if err = fn(path, d, err); err != nil {
			if err == filepath.SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
	}
	sortNewestFirst(ctx, ents)
	for _, e := range ents {
		if err := walkNewestDir(ctx, filepath.Join(path, e.Name()), e, readDir, fn); err != nil {
			if err == filepath.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

// sortNewestFirst orders the entries of one directory newest first: by
// modification time, latest first, then by name, last first (the capture
// time in a phone's file names breaks ties of a coarse or equal mtime).
// Files and sub-directories are interleaved; a directory sorts by its own
// mtime, which a phone or camera updates when a file is added to it, so
// the folder holding the latest shots comes first. A newest-under-it rule
// would need the whole tree listed before the first file is dispatched,
// which is what a slow source cannot afford.
//
// The mtime comes from DirEntry.Info: one lstat per entry, which a worker
// makes anyway when it reads the file. An entry whose Info fails (it
// vanished, the mount hiccupped) is never dropped: it goes after the dated
// ones, in name order (last first), and its error, if any, surfaces when
// it is walked or read.
// Once ctx is done no more entries are stat'ed.
func sortNewestFirst(ctx context.Context, ents []fs.DirEntry) {
	type keyed struct {
		e     fs.DirEntry
		mtime time.Time
		dated bool
	}
	ks := make([]keyed, len(ents))
	for i, e := range ents {
		ks[i].e = e
		if ctx.Err() != nil {
			continue
		}
		if fi, err := e.Info(); err == nil {
			ks[i].mtime, ks[i].dated = fi.ModTime(), true
		}
	}
	slices.SortStableFunc(ks, func(a, b keyed) int {
		if a.dated != b.dated {
			if a.dated {
				return -1
			}
			return 1
		}
		return cmp.Or(b.mtime.Compare(a.mtime), strings.Compare(b.e.Name(), a.e.Name()))
	})
	for i := range ks {
		ents[i] = ks[i].e
	}
}
