package library

import (
	"context"
	"fmt"
	"os"

	"github.com/cespare/xxhash/v2"
)

// PullFunc copies a source file to the local path dst, creating it,
// and reports the bytes written so far through onProgress (a growing
// total, called on the caller's goroutine); on error it leaves nothing at
// dst. gvfs.Pull with its source bound is one.
type PullFunc func(ctx context.Context, dst string, onProgress func(written int64)) error

// StagePulled is Stage for a source that pull copies (a phone file pulled
// by the gvfs daemon): src itself is only stat'ed, never read. pull writes
// a new temp file copy-*.part in tmpDir; its progress is passed on to
// onWrite (when not nil) as increments, as Stage reports buffers. The temp
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

// HashPulled hashes a source that pull copies: it pulls it into a temp
// file copy-*.part in tmpDir, hashes that and removes it. It returns the
// hash and the size.
func HashPulled(ctx context.Context, tmpDir string, pull PullFunc) (uint64, int64, error) {
	name, err := pullTemp(ctx, tmpDir, pull, nil)
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(name)
	return hashFileCtx(ctx, name)
}

// pullTemp runs pull into a new temp name copy-*.part in tmpDir and
// returns that name; on error nothing is left there. The name is reserved
// with CreateTemp, and the empty file removed again before pull runs so
// that pull creates the file itself: GIO replaces an existing destination
// through a hidden temp file renamed at the end, which would hide the
// progress and leave that file behind if gio were killed. onWrite (when
// not nil) gets the increments of pull's progress and finally of the
// pulled file's size.
func pullTemp(ctx context.Context, tmpDir string, pull PullFunc, onWrite func(int64)) (string, error) {
	tmp, err := os.CreateTemp(tmpDir, "copy-*.part")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	tmp.Close()
	if err := os.Remove(name); err != nil {
		return "", err
	}
	var reported int64
	progress := func(total int64) {
		if total > reported && onWrite != nil {
			onWrite(total - reported)
			reported = total
		}
	}
	fail := func(err error) (string, error) {
		os.Remove(name)
		return "", err
	}
	if err := pull(ctx, name, progress); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	fi, err := os.Stat(name)
	if err != nil {
		return fail(err)
	}
	if !fi.Mode().IsRegular() {
		return fail(fmt.Errorf("%s: pulled file is not a regular file", name))
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
