package library

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStageAndCommit(t *testing.T) {
	l := open(t)
	src, data := randomFile(t, t.TempDir(), 3<<20+123)
	mtime := time.Date(2019, 7, 1, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	var reported int64
	writes := 0
	st, err := Stage(context.Background(), src, l.TempDir(), func(n int64) { reported += n; writes++ })
	if err != nil {
		t.Fatal(err)
	}
	want, size, err := HashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hash != want || st.Size != size || reported != size {
		t.Errorf("staged = %+v, reported %d; want hash %016x size %d", st, reported, want, size)
	}
	if writes < 4 {
		t.Errorf("onWrite called %d times for a 3 MiB file, want once per buffer", writes)
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
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, data) {
		t.Error("destination content differs")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mtime) {
		t.Errorf("mtime not preserved: %v", fi.ModTime())
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", fi.Mode().Perm())
	}
	tempIsEmpty(t, l)
}

func TestCommitFailureLeavesNothing(t *testing.T) {
	l := open(t)
	src, _ := randomFile(t, t.TempDir(), 1000)

	// An existing destination is never replaced.
	dst := l.Abs("image/2020/01/a.jpg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Stage(context.Background(), src, l.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Commit(st.Path, dst); err == nil {
		t.Error("Commit replaced an existing destination")
	}
	if got, _ := os.ReadFile(dst); string(got) != "keep" {
		t.Errorf("destination changed: %q", got)
	}
	tempIsEmpty(t, l)

	// The destination directory cannot be created.
	if err := os.WriteFile(l.Abs("video"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = Stage(context.Background(), src, l.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dst2 := l.Abs("video/2020/01/a.mp4")
	if err := Commit(st.Path, dst2); err == nil {
		t.Error("Commit under a file succeeded")
	}
	if _, err := os.Lstat(dst2); err == nil {
		t.Error("something at the destination")
	}
	tempIsEmpty(t, l)
}

func TestStageCancelledAndChanged(t *testing.T) {
	l := open(t)
	src, _ := randomFile(t, t.TempDir(), 3<<20)

	// Cancelled from inside the copy, after the first buffer is written:
	// the partly written temp file is removed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var written int64
	if _, err := Stage(ctx, src, l.TempDir(), func(n int64) {
		written += n
		cancel()
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want Canceled", err)
	}
	if written == 0 || written >= 3<<20 {
		t.Errorf("%d bytes written before the cancellation; want a partial copy", written)
	}
	tempIsEmpty(t, l)

	// The source grows while it is read.
	grown := false
	_, err := Stage(context.Background(), src, l.TempDir(), func(int64) {
		if grown {
			return
		}
		grown = true
		f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("more"))
		f.Close()
	})
	if !errors.Is(err, ErrSizeChanged) || !strings.Contains(err.Error(), src+": size changed while it was read") {
		t.Errorf("err = %v, want %s: size changed while it was read", err, src)
	}
	tempIsEmpty(t, l)
}
