package library

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePull returns a PullFunc that writes data to dst in 1 MiB chunks,
// reporting the total after each, and then calls after (when not nil),
// returning its error.
func fakePull(data []byte, after func(ctx context.Context, dst string) error) PullFunc {
	return func(ctx context.Context, dst string, onProgress func(int64)) error {
		if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
			return errors.New("destination exists before the pull")
		}
		if filepath.Base(dst) != "copy.part" || !strings.HasPrefix(filepath.Base(filepath.Dir(dst)), "pull-") {
			return errors.New("destination not <tmp>/pull-*/copy.part: " + dst)
		}
		if entries, err := os.ReadDir(filepath.Dir(dst)); err != nil || len(entries) != 0 {
			return errors.New("pull directory not empty before the pull")
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		for off := 0; off < len(data); off += 1 << 20 {
			end := min(off+1<<20, len(data))
			if _, err := f.Write(data[off:end]); err != nil {
				f.Close()
				return err
			}
			onProgress(int64(end))
		}
		if err := f.Close(); err != nil {
			return err
		}
		if after != nil {
			return after(ctx, dst)
		}
		return nil
	}
}

func TestStagePulled(t *testing.T) {
	l := open(t)
	src, data := randomFile(t, t.TempDir(), 3<<20+123)
	mtime := time.Date(2019, 7, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	var reported int64
	writes := 0
	st, err := StagePulled(context.Background(), src, l.TempDir(), fakePull(data, nil),
		func(n int64) { reported += n; writes++ })
	if err != nil {
		t.Fatal(err)
	}
	want, size, _ := HashFile(src)
	if st.Hash != want || st.Size != size || reported != size || writes != 4 {
		t.Errorf("staged = %+v, reported %d in %d calls; want hash %016x size %d in 4", st, reported, writes, want, size)
	}
	if filepath.Dir(st.Path) != l.TempDir() || !strings.HasPrefix(filepath.Base(st.Path), "copy-") ||
		!strings.HasSuffix(st.Path, ".part") {
		t.Errorf("temp path %s", st.Path)
	}
	if fi, err := os.Stat(st.Path); err != nil || !fi.ModTime().Equal(mtime) {
		t.Errorf("staged temp mtime: %v, %v", fi, err)
	}
	dst := l.Abs("image/2019/07/src.bin")
	if err := Commit(st.Path, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dst); err != nil || !bytes.Equal(got, data) {
		t.Error("destination content differs")
	}
	tempIsEmpty(t, l)

	// A pull that reports no progress is counted once, at the end.
	reported = 0
	silent := func(ctx context.Context, dst string, _ func(int64)) error {
		return fakePull(data, nil)(ctx, dst, func(int64) {})
	}
	st, err = StagePulled(context.Background(), src, l.TempDir(), silent, func(n int64) { reported += n })
	if err != nil || reported != size {
		t.Errorf("silent pull: %v, reported %d", err, reported)
	}
	os.Remove(st.Path)

	h, n, err := HashPulled(context.Background(), src, l.TempDir(), fakePull(data, nil))
	if err != nil || h != want || n != size {
		t.Errorf("HashPulled = %016x, %d, %v; want %016x, %d", h, n, err, want, size)
	}
	tempIsEmpty(t, l)
}

func TestStagePulledFailures(t *testing.T) {
	l := open(t)
	src, data := randomFile(t, t.TempDir(), 2<<20+5)

	// The pulled size differs from the source's.
	_, err := StagePulled(context.Background(), src, l.TempDir(), fakePull(data[:len(data)-1], nil), nil)
	if !errors.Is(err, ErrSizeChanged) {
		t.Errorf("short pull: err = %v, want ErrSizeChanged", err)
	}
	tempIsEmpty(t, l)

	// The pull fails after writing part of the file and leaves it.
	boom := errors.New("gio copy: Operation not supported")
	_, err = StagePulled(context.Background(), src, l.TempDir(),
		fakePull(data[:1<<20], func(context.Context, string) error { return boom }), nil)
	if !errors.Is(err, boom) {
		t.Errorf("failing pull: err = %v", err)
	}
	tempIsEmpty(t, l)
	if _, _, err := HashPulled(context.Background(), src, l.TempDir(),
		fakePull(data, func(context.Context, string) error { return boom })); !errors.Is(err, boom) {
		t.Errorf("HashPulled with a failing pull: %v", err)
	}
	tempIsEmpty(t, l)

	// HashPulled checks the size too, so the caller reads the source
	// itself instead.
	if _, _, err := HashPulled(context.Background(), src, l.TempDir(), fakePull(data[:len(data)-1], nil)); !errors.Is(err, ErrSizeChanged) {
		t.Errorf("short HashPulled: err = %v, want ErrSizeChanged", err)
	}
	tempIsEmpty(t, l)

	// A file the daemon would create after a killed gio finds its
	// directory gone.
	var late string
	_, err = StagePulled(context.Background(), src, l.TempDir(), func(_ context.Context, dst string, _ func(int64)) error {
		late = dst
		return boom
	}, nil)
	if !errors.Is(err, boom) {
		t.Errorf("failing pull: err = %v", err)
	}
	if err := os.WriteFile(late, data, 0o644); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("late write into the pull directory: %v, want ErrNotExist", err)
	}
	tempIsEmpty(t, l)

	// Cancelled while pulling: the pull returns ctx.Err().
	ctx, cancel := context.WithCancel(context.Background())
	_, err = StagePulled(ctx, src, l.TempDir(), fakePull(data, func(ctx context.Context, _ string) error {
		cancel()
		return ctx.Err()
	}), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled pull: err = %v", err)
	}
	tempIsEmpty(t, l)

	// Cancelled just as the pull finished successfully.
	ctx, cancel = context.WithCancel(context.Background())
	_, err = StagePulled(ctx, src, l.TempDir(), fakePull(data, func(context.Context, string) error {
		cancel()
		return nil
	}), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancel after the pull: err = %v", err)
	}
	tempIsEmpty(t, l)

	// The source cannot be stat'ed: nothing is pulled.
	called := false
	_, err = StagePulled(context.Background(), src+".missing", l.TempDir(),
		func(context.Context, string, func(int64)) error { called = true; return nil }, nil)
	if err == nil || called {
		t.Errorf("missing source: err = %v, pull called %v", err, called)
	}
	tempIsEmpty(t, l)
}

// CleanTemp removes pull directories left by an earlier run.
func TestCleanTempRemovesPullDirs(t *testing.T) {
	l := open(t)
	dir, err := os.MkdirTemp(l.TempDir(), "pull-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "copy.part"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.CleanTemp(); err != nil {
		t.Fatal(err)
	}
	tempIsEmpty(t, l)
}
