package library

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// ErrTargetExists is returned (wrapped) by MoveFileTo when the target
// library already has a file at the target path: nothing is replaced.
var ErrTargetExists = errors.New("the target library already has a file at this path")

// LiveFile returns the path to read the library file at the catalog path
// rel, checked like a file about to be moved to the trash: inside the
// library and not internal, also with symbolic links resolved
// (ErrNotLibraryPath), and a regular file (ErrNotRegularFile).
func (l *Library) LiveFile(rel string) (string, error) {
	return resolveFile(l.Abs(rel), l.liveRule())
}

// LiveTarget creates the parent directories of the catalog path rel and
// returns the path to place a new file at, its parent resolved; both
// spellings must lie inside the library and outside its internal files
// (ErrNotLibraryPath). It does not check whether a file exists there:
// the caller places the file without replacing (RenameNoReplace,
// CopyReadBack).
func (l *Library) LiveTarget(rel string) (string, error) {
	return prepareTarget(l.Abs(rel), l.liveRule())
}

// MoveFileTo moves the library file at rel to the catalog path dstRel of
// the library dst with one rename, for a move between libraries
// (docs/design/library-move.md). The source is checked like LiveFile, the
// target like LiveTarget. It never replaces a file (ErrTargetExists) and
// returns a rename error as it is: a cross-device error (IsCrossDevice)
// means the libraries are on different volumes and the caller copies
// instead.
func (l *Library) MoveFileTo(rel string, dst *Library, dstRel string) error {
	src, err := l.LiveFile(rel)
	if err != nil {
		return err
	}
	target, err := dst.LiveTarget(dstRel)
	if err != nil {
		return err
	}
	if err := rename(src, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrTargetExists, target)
		}
		return err
	}
	syncDir(filepath.Dir(target))
	syncDir(filepath.Dir(src))
	return nil
}

// RestoreTo moves the trash file of the catalog path trashRel back to
// the library path rel with one rename, never replacing a file
// (fs.ErrExist): it undoes a MoveToTrashAs(rel, trashRel) whose catalog
// update failed. The checks are those of Restore.
func (l *Library) RestoreTo(trashRel, rel string) error {
	src, err := resolveFile(l.TrashPath(trashRel), l.trashRule())
	if err != nil {
		return err
	}
	dst, err := prepareTarget(l.Abs(rel), l.liveRule())
	if err != nil {
		return err
	}
	if err := rename(src, dst); err != nil {
		return err
	}
	syncDir(filepath.Dir(dst))
	syncDir(filepath.Dir(src))
	return nil
}
