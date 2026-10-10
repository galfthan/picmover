package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// TrashDirName is the trash directory inside MetaDir.
const TrashDirName = "trash"

// ErrTrashTargetExists is returned (wrapped) by MoveToTrash when the trash
// path is already taken: picmover never overwrites a file.
var ErrTrashTargetExists = errors.New("the trash already holds a file at this path")

// rename is os.Rename; tests inject failures (a cross-device error).
var rename = os.Rename

// TrashDir returns <root>/.picmover/trash, where trashed files are kept
// under their catalog path until they are restored or deleted for good.
func (l *Library) TrashDir() string { return filepath.Join(l.root, MetaDir, TrashDirName) }

// TrashPath returns the trash location of the catalog path rel: the same
// relative path under TrashDir, so a restore is unambiguous.
func (l *Library) TrashPath(rel string) string {
	return filepath.Join(l.TrashDir(), filepath.FromSlash(rel))
}

// MoveToTrash moves the library file at rel to TrashPath(rel) with one
// rename (both are inside the library, so normally on one volume). It
// creates the parent directories, refuses when the trash path exists
// (ErrTrashTargetExists) and returns a rename error as it is: a
// cross-device error (EXDEV, ERROR_NOT_SAME_DEVICE, for example a
// mounted subfolder) is never turned into a copy and delete.
func (l *Library) MoveToTrash(rel string) error {
	return l.MoveToTrashAs(rel, rel)
}

// MoveToTrashAs is MoveToTrash for a file whose trash path is that of
// another catalog path, trashRel: it undoes a Restore that picked a new
// name.
func (l *Library) MoveToTrashAs(rel, trashRel string) error {
	src, dst := l.Abs(rel), l.TrashPath(trashRel)
	if _, err := os.Lstat(src); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %s", ErrTrashTargetExists, dst)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := rename(src, dst); err != nil {
		return err
	}
	syncDir(filepath.Dir(dst))
	syncDir(filepath.Dir(src))
	return nil
}

// Restore moves the trashed file of rel back into the library and
// returns the catalog path it now has: rel when that is free, otherwise
// the first free UniqueName variant (stem_1.ext, ...). A path is taken
// when something exists there on disk or taken (the catalog's check,
// which may be nil) says so. Like MoveToTrash it only renames and returns
// rename errors as they are.
func (l *Library) Restore(rel string, taken func(rel string) (bool, error)) (string, error) {
	src := l.TrashPath(rel)
	if _, err := os.Lstat(src); err != nil {
		return "", err
	}
	isTaken := func(c string) (bool, error) {
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
	target, err := UniqueName(rel, isTaken)
	if err != nil {
		return "", err
	}
	dst := l.Abs(target)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := rename(src, dst); err != nil {
		return "", err
	}
	syncDir(filepath.Dir(dst))
	return target, nil
}
