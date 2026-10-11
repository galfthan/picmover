package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopyReadBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	os.WriteFile(src, []byte("hello backup"), 0o644)
	mt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(src, mt, mt)
	want, _, _ := HashFile(src)
	tmp := filepath.Join(dir, "tmp")
	dst := filepath.Join(dir, "out", "a", "dst.bin")
	res, err := CopyReadBack(ctx, src, dst, tmp, &want)
	if err != nil || res.Hash != want || res.Size != 12 || !res.MTime.Equal(mt) {
		t.Fatalf("copy %+v %v", res, err)
	}
	if fi, err := os.Stat(dst); err != nil || !fi.ModTime().Equal(mt) {
		t.Fatalf("dst %v %v", fi, err)
	}
	// Never replaces.
	if _, err := CopyReadBack(ctx, src, dst, tmp, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("replace: %v", err)
	}
	// Wrong expected hash: nothing at the destination, no temp left.
	bad := want + 1
	dst2 := filepath.Join(dir, "out", "b.bin")
	if _, err := CopyReadBack(ctx, src, dst2, tmp, &bad); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	// A copy that does not read back as written.
	readBack = func(ctx context.Context, p string) (uint64, int64, error) { return 1, 12, nil }
	defer func() { readBack = readBackUncached }()
	if _, err := CopyReadBack(ctx, src, dst2, tmp, nil); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("read-back mismatch: %v", err)
	}
	if _, err := os.Lstat(dst2); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a failed copy left the destination")
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("temp files left: %v", ents)
	}
	// Not a regular file.
	if _, err := CopyReadBack(ctx, dir, dst2, tmp, nil); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("directory: %v", err)
	}
}
