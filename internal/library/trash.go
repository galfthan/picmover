package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// TrashDirName is the trash directory inside MetaDir.
const TrashDirName = "trash"

// ErrTrashTargetExists is returned (wrapped) by MoveToTrash when the trash
// path is already taken: picmover never overwrites a file.
var ErrTrashTargetExists = errors.New("the trash already holds a file at this path")

// ErrNotLibraryPath is returned (wrapped) by the trash operations when a
// catalog path leads out of the library (or out of the trash), into
// picmover's own files, or through a symbolic link that does.
var ErrNotLibraryPath = errors.New("the file's catalog path is not a library path")

// ErrNotRegularFile is returned (wrapped) by the trash operations when the
// file at a catalog path is a directory, a symbolic link or another
// non-regular file: only regular files are moved or deleted.
var ErrNotRegularFile = errors.New("the path is not a regular file")

// rename is RenameNoReplace; tests inject failures (a cross-device error).
var rename = RenameNoReplace

// TrashDir returns <root>/.picmover/trash, where trashed files are kept
// under their catalog path until they are restored or deleted for good.
func (l *Library) TrashDir() string { return filepath.Join(l.root, MetaDir, TrashDirName) }

// TrashPath returns the trash location of the catalog path rel: the same
// relative path under TrashDir, so a restore is unambiguous.
func (l *Library) TrashPath(rel string) string {
	return filepath.Join(l.TrashDir(), filepath.FromSlash(rel))
}

// pathRule says where a path may lie: in the library (inside the root and
// not internal) or in the trash (strictly inside TrashDir).
type pathRule func(p string) bool

func (l *Library) liveRule() pathRule {
	return func(p string) bool { return l.Contains(p) && !l.IsInternal(p) }
}

// trashRule accepts paths inside the trash directory, both as spelled and
// with the trash directory's own symbolic links resolved; the resolved
// trash directory must itself be inside the library's .picmover.
func (l *Library) trashRule() pathRule {
	lexical := l.TrashDir()
	resolved := lexical
	if r, err := filepath.EvalSymlinks(lexical); err == nil {
		resolved = filepath.Clean(r)
	}
	ok := resolved == lexical || l.IsInternal(resolved)
	return func(p string) bool {
		return ok && (within(lexical, p) || within(resolved, p))
	}
}

// within reports whether p lies strictly inside dir (lexically).
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// resolveDir resolves the symbolic links of dir. Some Windows volumes
// cannot be resolved at all (library.Open keeps the unresolved root then):
// there the lexical path is used, as the file server does.
func resolveDir(dir string) (string, error) {
	r, err := filepath.EvalSymlinks(dir)
	switch {
	case err == nil:
		return filepath.Clean(r), nil
	case runtime.GOOS == "windows" && !errors.Is(err, fs.ErrNotExist):
		return dir, nil
	}
	return "", err
}

// resolveFile checks the existing file p the way the file server does
// before it is renamed or removed: p must satisfy rule; its parent
// directory is resolved (EvalSymlinks) and the resolved path must satisfy
// rule too; the last component must be a regular file (Lstat: not a
// directory, not a link). It returns the resolved path, which is what the
// caller must rename or remove.
func resolveFile(p string, rule pathRule) (string, error) {
	if !rule(p) {
		return "", fmt.Errorf("%w: %s", ErrNotLibraryPath, p)
	}
	dir, err := resolveDir(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	r := filepath.Join(dir, filepath.Base(p))
	if !rule(r) {
		return "", fmt.Errorf("%w: %s resolves to %s", ErrNotLibraryPath, p, r)
	}
	fi, err := os.Lstat(r)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s (%s)", ErrNotRegularFile, r, fi.Mode().Type())
	}
	return r, nil
}

// prepareTarget creates the parent directories of the rename target p
// and returns p with its parent resolved, after checking both spellings
// against rule (a symbolic link in the target's directories must not lead
// elsewhere).
func prepareTarget(p string, rule pathRule) (string, error) {
	if !rule(p) {
		return "", fmt.Errorf("%w: %s", ErrNotLibraryPath, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	dir, err := resolveDir(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	r := filepath.Join(dir, filepath.Base(p))
	if !rule(r) {
		return "", fmt.Errorf("%w: %s resolves to %s", ErrNotLibraryPath, p, r)
	}
	return r, nil
}

// MoveToTrash moves the library file at rel to TrashPath(rel) with one
// rename (both are inside the library, so normally on one volume). The
// source must be a regular file inside the library, also with symbolic
// links resolved (ErrNotLibraryPath, ErrNotRegularFile). It creates the
// parent directories, never replaces an existing trash file
// (ErrTrashTargetExists, checked atomically by RenameNoReplace) and
// returns a rename error as it is: a cross-device error (EXDEV,
// ERROR_NOT_SAME_DEVICE, for example a mounted subfolder) is never turned
// into a copy and delete.
func (l *Library) MoveToTrash(rel string) error {
	return l.MoveToTrashAs(rel, rel)
}

// MoveToTrashAs is MoveToTrash for a file whose trash path is that of
// another catalog path, trashRel: it undoes a Restore that picked a new
// name.
func (l *Library) MoveToTrashAs(rel, trashRel string) error {
	src, err := resolveFile(l.Abs(rel), l.liveRule())
	if err != nil {
		return err
	}
	dst, err := prepareTarget(l.TrashPath(trashRel), l.trashRule())
	if err != nil {
		return err
	}
	if err := rename(src, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrTrashTargetExists, dst)
		}
		return err
	}
	syncDir(filepath.Dir(dst))
	syncDir(filepath.Dir(src))
	return nil
}

// maxRestoreAttempts bounds the names Restore tries when the chosen one is
// taken between the check and the rename.
const maxRestoreAttempts = 100

// Restore moves the trashed file of rel back into the library and
// returns the catalog path it now has: rel when that is free, otherwise
// the first free UniqueName variant (stem_1.ext, ...). A path is taken
// when something exists there on disk or taken (the catalog's check,
// which may be nil) says so. The trash file must be a regular file inside
// the trash, also with symbolic links resolved, and the target must stay
// inside the library. The rename never replaces a file: when another
// process takes the chosen name first, the next variant is tried. Like
// MoveToTrash it only renames and returns rename errors as they are.
func (l *Library) Restore(rel string, taken func(rel string) (bool, error)) (string, error) {
	src, err := resolveFile(l.TrashPath(rel), l.trashRule())
	if err != nil {
		return "", err
	}
	lost := map[string]bool{} // names taken between the check and the rename
	isTaken := func(c string) (bool, error) {
		if lost[strings.ToLower(c)] {
			return true, nil
		}
		if _, err := os.Lstat(l.Abs(c)); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if taken == nil {
			return false, nil
		}
		return taken(c)
	}
	for range maxRestoreAttempts {
		target, err := UniqueName(rel, isTaken)
		if err != nil {
			return "", err
		}
		dst, err := prepareTarget(l.Abs(target), l.liveRule())
		if err != nil {
			return "", err
		}
		err = rename(src, dst)
		if errors.Is(err, fs.ErrExist) {
			lost[strings.ToLower(target)] = true
			continue
		}
		if err != nil {
			return "", err
		}
		syncDir(filepath.Dir(dst))
		syncDir(filepath.Dir(src))
		return target, nil
	}
	return "", fmt.Errorf("library: restore %s: every name tried was taken meanwhile", rel)
}

// RemoveTrashFile deletes the trash file of the catalog path rel for good.
// The file must be a regular file inside the trash, also with symbolic
// links resolved (ErrNotLibraryPath, ErrNotRegularFile): a directory or a
// link is refused, never followed. A missing file counts as removed.
func (l *Library) RemoveTrashFile(rel string) error {
	p, err := resolveFile(l.TrashPath(rel), l.trashRule())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	syncDir(filepath.Dir(p))
	return nil
}

// TrashFiles returns the catalog paths (slash form, relative to TrashDir)
// of the regular files in the trash, in walk order. Links are not
// followed and not listed. A missing trash is empty.
func (l *Library) TrashFiles() ([]string, error) {
	dir := l.TrashDir()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}
