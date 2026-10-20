package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/cespare/xxhash/v2"
)

// ErrSourceChanged is returned (wrapped) by CopyReadBack when the source
// file changed (size or modification time) while it was being copied.
var ErrSourceChanged = errors.New("the source file changed while it was copied")

// CopyResult describes a completed CopyReadBack.
type CopyResult struct {
	Hash  uint64    // xxhash64 of the bytes read back from the copy
	Size  int64     // bytes copied
	MTime time.Time // the source's modification time, set on the copy
}

// CopyReadBack copies src to dst through a temp file in tmpDir, which must
// be on dst's volume: the bytes are written and fsynced, the temp file is
// read back and hashed, and only a copy whose read-back hash equals the
// hash of the bytes read from src (and *want, when want is not nil) is
// renamed into place with RenameNoReplace, so an existing dst is never
// replaced (the error then wraps fs.ErrExist). The source's modification
// time is set on the copy. A source that changes size or modification
// time during the copy fails with ErrSourceChanged. On any failure
// nothing is left at dst and the temp file is removed. Missing parent
// directories of dst are created.
func CopyReadBack(ctx context.Context, src, dst, tmpDir string, want *uint64) (CopyResult, error) {
	return copyReadBack(ctx, src, dst, tmpDir, want, true)
}

// CopyReadBackDeferSync is CopyReadBack without the final sync of dst's
// folder: the copy is fsynced, but its name is durable only after the
// caller's SyncDir(filepath.Dir(dst)), which a caller copying many files
// into few folders does once per folder (the backup does it before
// committing the rows of a batch).
func CopyReadBackDeferSync(ctx context.Context, src, dst, tmpDir string, want *uint64) (CopyResult, error) {
	return copyReadBack(ctx, src, dst, tmpDir, want, false)
}

func copyReadBack(ctx context.Context, src, dst, tmpDir string, want *uint64, sync bool) (CopyResult, error) {
	in, err := os.Open(src)
	if err != nil {
		return CopyResult{}, err
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil {
		return CopyResult{}, err
	}
	if !before.Mode().IsRegular() {
		return CopyResult{}, fmt.Errorf("%s: %w", src, ErrNotRegularFile)
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return CopyResult{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, "copy-*.part")
	if err != nil {
		return CopyResult{}, err
	}
	tmpName := tmp.Name()
	closed := false
	fail := func(err error) (CopyResult, error) {
		if !closed {
			tmp.Close()
		}
		os.Remove(tmpName)
		return CopyResult{}, err
	}
	h := xxhash.New()
	n, err := copyBuffered(ctx, io.MultiWriter(tmp, h), in)
	if err != nil {
		return fail(err)
	}
	after, err := in.Stat()
	if err != nil {
		return fail(err)
	}
	if n != after.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return fail(fmt.Errorf("%s: %w", src, ErrSourceChanged))
	}
	srcHash := h.Sum64()
	if want != nil && srcHash != *want {
		return fail(fmt.Errorf("%w: %s (want %016x, got %016x)", ErrHashMismatch, src, *want, srcHash))
	}
	if err := tmp.Chmod(0o644); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	back, size, err := readBack(ctx, tmpName)
	if err != nil {
		return fail(err)
	}
	if size != n || back != srcHash {
		return fail(fmt.Errorf("%w: the copy of %s reads back as %016x, not %016x", ErrHashMismatch, src, back, srcHash))
	}
	mtime := before.ModTime()
	_ = os.Chtimes(tmpName, mtime, mtime)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fail(err)
	}
	if err := RenameNoReplace(tmpName, dst); err != nil {
		return fail(err)
	}
	if sync {
		syncDir(filepath.Dir(dst))
	}
	return CopyResult{Hash: srcHash, Size: n, MTime: mtime}, nil
}

// readBack hashes the written copy; tests replace it to simulate a copy
// that does not read back as written.
var readBack = readBackUncached

// ReadBackUncached returns the xxhash64 digest and size of the file at p
// read back from the disk where the platform allows it (readBackUncached).
// The file must have been fsynced, as Commit and CopyReadBack do, or its
// dirty pages stay cached. The importer uses it to check a library copy
// before a source is deleted, or a copy import is reported.
func ReadBackUncached(ctx context.Context, p string) (uint64, int64, error) {
	return readBackUncached(ctx, p)
}

// readBackUncached hashes the written (and fsynced) copy, first asking
// the kernel to drop its cached pages where it can (Linux:
// posix_fadvise DONTNEED), so the bytes come from the disk rather than
// from the page cache the write just filled. Elsewhere the read may be
// served from the cache: the check then proves the bytes reached the
// file system, not the platter.
func readBackUncached(ctx context.Context, p string) (uint64, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	dropCache(f)
	h := xxhash.New()
	n, err := copyBuffered(ctx, h, f)
	if err != nil {
		return 0, 0, err
	}
	return h.Sum64(), n, nil
}

// HashFileContext is HashFile with cancellation checked between reads.
func HashFileContext(ctx context.Context, p string) (uint64, int64, error) {
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

// SyncDir flushes a directory's metadata so a completed rename survives a
// crash (best effort; a no-op on Windows).
func SyncDir(dir string) { syncDir(dir) }
