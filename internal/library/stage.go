package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/cespare/xxhash/v2"
)

// ErrSourceChanged is returned (wrapped) by Stage when the number of bytes
// read differs from the source's size: the file changed while it was read.
var ErrSourceChanged = errors.New("changed while it was read")

// Staged is a source file copied into a library temp file by Stage.
type Staged struct {
	Path string // the temp file, <tmpDir>/copy-*.part; place it with Commit
	Hash uint64 // xxhash64 of the bytes written, as HashFile computes it
	Size int64  // bytes written, equal to the source's size
}

// Stage copies the file src once, sequentially, into a new temp file
// copy-*.part in tmpDir (the library's TempDir), hashing the bytes as they
// are written. See StageFile.
func Stage(ctx context.Context, src, tmpDir string, onWrite func(n int64)) (Staged, error) {
	in, err := os.Open(src)
	if err != nil {
		return Staged{}, err
	}
	defer in.Close()
	return StageFile(ctx, in, tmpDir, onWrite)
}

// StageFile is Stage for an opened source; it reads in to the end and
// does not close it. The pooled 1 MiB buffer is used; onWrite, when not
// nil, is called with the size of every buffer written, as it is written.
// The number of bytes written must equal the size of in's Stat, otherwise
// the error is "<name>: changed while it was read" (wrapping
// ErrSourceChanged). The temp file gets the source's mtime and is closed
// but not fsynced; Commit does that. On any error, cancellation included,
// the temp file is removed.
func StageFile(ctx context.Context, in *os.File, tmpDir string, onWrite func(n int64)) (Staged, error) {
	info, err := in.Stat()
	if err != nil {
		return Staged{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, "copy-*.part")
	if err != nil {
		return Staged{}, err
	}
	name := tmp.Name()
	fail := func(err error) (Staged, error) {
		tmp.Close()
		os.Remove(name)
		return Staged{}, err
	}
	h := xxhash.New()
	var w io.Writer = io.MultiWriter(tmp, h)
	if onWrite != nil {
		w = progressWriter{w, onWrite}
	}
	n, err := copyBuffered(ctx, w, in)
	if err != nil {
		return fail(err)
	}
	if n != info.Size() {
		return fail(fmt.Errorf("%s: %w", in.Name(), ErrSourceChanged))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return Staged{}, err
	}
	_ = os.Chtimes(name, info.ModTime(), info.ModTime())
	return Staged{Path: name, Hash: h.Sum64(), Size: n}, nil
}

// progressWriter reports every successful write to f.
type progressWriter struct {
	w io.Writer
	f func(int64)
}

func (p progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if n > 0 {
		p.f(int64(n))
	}
	return n, err
}

// Commit places the staged temp file tmpPath at dst, an absolute path
// inside the library on the same volume: the temp file is made readable
// (0644), fsynced and closed, dst's directory is created, the file is
// renamed into place without ever replacing an existing dst, and the
// directory is synced. The temp file's mtime is kept. On failure nothing
// is left at dst and the temp file is removed.
func Commit(tmpPath, dst string) error {
	f, err := os.OpenFile(tmpPath, os.O_RDWR, 0)
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	var mtime time.Time
	if fi, err := f.Stat(); err == nil {
		mtime = fi.ModTime()
	}
	return commitFile(f, dst, mtime)
}

// commitFile is Commit for the open temp file f; it always closes f. A
// non-zero mtime is set on dst after the rename when it differs.
func commitFile(f *os.File, dst string, mtime time.Time) error {
	name := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(name)
		return err
	}
	// CreateTemp makes the file 0600; imported files should be readable
	// like the rest of the library. Chmod is a no-op on Windows.
	if err := f.Chmod(0o644); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(name)
		return err
	}
	if err := RenameNoReplace(name, dst); err != nil {
		os.Remove(name)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("destination already exists: %s", dst)
		}
		return err
	}
	if !mtime.IsZero() {
		if fi, err := os.Stat(dst); err != nil || !fi.ModTime().Equal(mtime) {
			_ = os.Chtimes(dst, mtime, mtime)
		}
	}
	syncDir(filepath.Dir(dst))
	return nil
}
