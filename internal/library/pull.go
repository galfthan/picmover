package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cespare/xxhash/v2"
)

// PullFunc copies a source file to the local path dst, creating it (dst
// does not exist when it is called, and lies in a directory of its own),
// and reports the bytes written so far through onProgress (a growing
// total, called on the caller's goroutine); on error it leaves nothing at
// dst. gvfs.Pull with its source bound is one.
type PullFunc func(ctx context.Context, dst string, onProgress func(written int64)) error

// StagePulled is Stage for a source that pull copies (a phone file pulled
// by the gvfs daemon): src itself is only stat'ed, never read. pull writes
// a new temp file copy-*.part in tmpDir; its progress is passed on to
// onWrite (when not nil) as increments, as Stage reports buffers (see
// pullTemp for where pull writes). The temp
// file is then hashed (a local read), its size must equal src's Stat size
// (otherwise the error wraps ErrSizeChanged), and it gets src's mtime. The
// result is the same as Stage's; place it with Commit. On any error,
// cancellation included, the temp file is removed; increments already
// passed to onWrite are not taken back.
func StagePulled(ctx context.Context, src, tmpDir string, pull PullFunc, onWrite func(n int64)) (Staged, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Staged{}, err
	}
	name, err := pullTemp(ctx, tmpDir, pull, onWrite)
	if err != nil {
		return Staged{}, err
	}
	fail := func(err error) (Staged, error) {
		os.Remove(name)
		return Staged{}, err
	}
	hash, n, err := hashFileCtx(ctx, name)
	if err != nil {
		return fail(err)
	}
	if n != info.Size() {
		return fail(fmt.Errorf("%s: %w (pulled %d bytes, size %d)", src, ErrSizeChanged, n, info.Size()))
	}
	_ = os.Chtimes(name, info.ModTime(), info.ModTime())
	return Staged{Path: name, Hash: hash, Size: n}, nil
}

// HashPulled hashes src, a source that pull copies: it stats src, pulls
// it into a temp file in tmpDir (see pullTemp), hashes that and removes it.
// The pulled size must equal src's Stat size, otherwise the error wraps
// ErrSizeChanged, as in StagePulled, so that the caller can read src
// itself instead. It returns the hash and the size.
func HashPulled(ctx context.Context, src, tmpDir string, pull PullFunc) (uint64, int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, 0, err
	}
	name, err := pullTemp(ctx, tmpDir, pull, nil)
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(name)
	h, n, err := hashFileCtx(ctx, name)
	if err != nil {
		return 0, 0, err
	}
	if n != info.Size() {
		return 0, 0, fmt.Errorf("%s: %w (pulled %d bytes, size %d)", src, ErrSizeChanged, n, info.Size())
	}
	return h, n, nil
}

// pullTemp runs pull and returns the pulled file, a temp file
// copy-*.part in tmpDir; on error nothing is left in tmpDir.
//
// Every pull gets a directory of its own, pull-* in tmpDir (MkdirTemp),
// and pull creates <dir>/copy.part there: gio must create the
// destination itself (GIO replaces an existing destination through a
// hidden temp file renamed at the end, which would hide the progress and
// leave that file behind if gio were killed), and the name it writes to
// must never be one another temp file in tmpDir can draw, since the gvfs
// daemon opens it without O_EXCL. On success the file is renamed onto a
// copy-*.part name reserved with CreateTemp in tmpDir (the rename
// replaces that empty reservation). On failure or cancellation the whole
// directory is removed, so a file the daemon would still create after gio
// was killed finds no directory, or is removed with it; Library.CleanTemp
// removes any left over at the start of the next run. onWrite (when not
// nil) gets the increments of pull's progress and finally of the pulled
// file's size.
func pullTemp(ctx context.Context, tmpDir string, pull PullFunc, onWrite func(int64)) (string, error) {
	dir, err := os.MkdirTemp(tmpDir, "pull-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	part := filepath.Join(dir, "copy.part")
	var reported int64
	progress := func(total int64) {
		if total > reported && onWrite != nil {
			onWrite(total - reported)
			reported = total
		}
	}
	if err := pull(ctx, part, progress); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fi, err := os.Lstat(part)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: pulled file is not a regular file", part)
	}
	tmp, err := os.CreateTemp(tmpDir, "copy-*.part")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	tmp.Close()
	if err := os.Rename(part, name); err != nil {
		os.Remove(name)
		return "", err
	}
	progress(fi.Size())
	return name, nil
}

// hashFileCtx is HashFile with cancellation checked between reads.
func hashFileCtx(ctx context.Context, p string) (uint64, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	h := xxhash.New()
	n, err := copyBuffered(ctx, h, f)
	if err != nil {
		return 0, 0, err
	}
	return h.Sum64(), n, nil
}
