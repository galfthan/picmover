package thumbs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"picmover/internal/library"
	"picmover/internal/testimg"
)

// lateCancel is not cancelled at its first Err call and is cancelled at
// every later one: Ctrl+C arriving between Put's ctx check and its INSERT.
type lateCancel struct {
	context.Context
	calls int
	done  chan struct{}
}

func newLateCancel() *lateCancel {
	c := &lateCancel{Context: context.Background(), done: make(chan struct{})}
	close(c.done)
	return c
}

func (c *lateCancel) Done() <-chan struct{} { return c.done }
func (c *lateCancel) Err() error {
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return context.Canceled
}

// A Put that replaces an existing entry and then fails keeps the previous
// preview and row; a successful replace leaves no .old file behind.
func TestStorePutReplaceFailureKeepsPrevious(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	const hash = 0x7700000000000007
	oldThumb := testimg.BareJPEG()
	oldPreview := testimg.JPEG(testimg.Options{Width: 40, Height: 30, NoExif: true, Seed: 1})
	if err := s.Put(ctx, hash, oldThumb, 4, 3, oldPreview); err != nil {
		t.Fatal(err)
	}
	newPreview := testimg.JPEG(testimg.Options{Width: 40, Height: 30, NoExif: true, Seed: 2})

	check := func(what string, wantPreview []byte, wantW int) {
		t.Helper()
		if pb, err := os.ReadFile(s.PreviewPath(hash)); err != nil || !bytes.Equal(pb, wantPreview) {
			t.Errorf("%s: preview not the expected one (err %v)", what, err)
		}
		if _, w, _, ok, err := s.Thumb(ctx, hash); err != nil || !ok || w != wantW {
			t.Errorf("%s: Thumb w=%d ok=%v err=%v, want w=%d", what, w, ok, err, wantW)
		}
		if th, pv, err := s.Has(ctx, hash); !th || !pv || err != nil {
			t.Errorf("%s: Has = %v %v %v", what, th, pv, err)
		}
		if _, err := os.Lstat(s.PreviewPath(hash) + oldSuffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: .old file left behind (%v)", what, err)
		}
	}

	// Cancelled between the ctx check and the insert.
	if err := s.Put(newLateCancel(), hash, oldThumb, 8, 6, newPreview); err == nil {
		t.Fatal("Put with a context cancelled before the insert succeeded")
	}
	check("cancelled replace", oldPreview, 4)

	// The insert itself fails.
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON thumbs BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, hash, oldThumb, 8, 6, newPreview); err == nil {
		t.Fatal("Put succeeded despite the failing insert")
	}
	check("failed insert", oldPreview, 4)
	if _, err := db.Exec(`DROP TRIGGER boom`); err != nil {
		t.Fatal(err)
	}

	// A successful replace.
	if err := s.Put(ctx, hash, oldThumb, 8, 6, newPreview); err != nil {
		t.Fatal(err)
	}
	check("replace", newPreview, 8)
}

// OpenStore restores a .old preview whose replacement never landed and
// removes one whose replacement did; Has and Stats ignore .old files.
func TestOpenStoreCleansOldPreviews(t *testing.T) {
	s, lib := openTestStore(t)
	ctx := context.Background()
	thumb := testimg.BareJPEG()
	preview := testimg.JPEG(testimg.Options{Width: 40, Height: 30, NoExif: true})
	const kept, orphan = 0x1100000000000001, 0x2200000000000002
	for _, h := range []uint64{kept, orphan} {
		if err := s.Put(ctx, h, thumb, 4, 3, preview); err != nil {
			t.Fatal(err)
		}
	}
	_, bytesBefore, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// kept: crash after the new preview landed (both files exist).
	if err := os.WriteFile(s.PreviewPath(kept)+oldSuffix, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	// orphan: crash after the old preview was moved aside.
	if err := os.Rename(s.PreviewPath(orphan), s.PreviewPath(orphan)+oldSuffix); err != nil {
		t.Fatal(err)
	}
	if _, pv, err := s.Has(ctx, orphan); pv || err != nil {
		t.Errorf("Has counted a .old file as the preview (%v)", err)
	}
	if _, b, err := s.Stats(ctx); err != nil || b != bytesBefore-int64(len(preview)) {
		t.Errorf("Stats bytes = %d, %v; want %d (.old not counted)", b, err, bytesBefore-int64(len(preview)))
	}
	s.Close()

	s2, err := OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for _, h := range []uint64{kept, orphan} {
		if pb, err := os.ReadFile(s2.PreviewPath(h)); err != nil || !bytes.Equal(pb, preview) {
			t.Errorf("%016x: preview after reopen: %v", h, err)
		}
		if _, err := os.Lstat(s2.PreviewPath(h) + oldSuffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%016x: .old not cleaned (%v)", h, err)
		}
	}
}

func TestOpenStoreReadOnly(t *testing.T) {
	// A missing cache is an error and nothing is created.
	lib, err := library.Attach(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStoreReadOnly(lib); err == nil {
		t.Fatal("OpenStoreReadOnly without thumbs.db succeeded")
	}
	if _, err := os.Stat(filepath.Join(lib.Root(), library.MetaDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenStoreReadOnly created %s (%v)", library.MetaDir, err)
	}

	// An existing cache, with and without a writer holding it open.
	s, lib := openTestStore(t)
	ctx := context.Background()
	thumb := testimg.BareJPEG()
	preview := testimg.JPEG(testimg.Options{Width: 40, Height: 30, NoExif: true})
	if err := s.Put(ctx, 1, thumb, 4, 3, preview); err != nil {
		t.Fatal(err)
	}
	for _, writerOpen := range []bool{true, false} {
		if !writerOpen {
			s.Close()
		}
		ro, err := OpenStoreReadOnly(lib)
		if err != nil {
			t.Fatalf("writer open %v: %v", writerOpen, err)
		}
		if n, _, err := ro.Stats(ctx); err != nil || n != 1 {
			t.Errorf("writer open %v: Stats = %d, %v", writerOpen, n, err)
		}
		if _, _, _, ok, err := ro.Thumb(ctx, 1); !ok || err != nil {
			t.Errorf("writer open %v: Thumb ok=%v err=%v", writerOpen, ok, err)
		}
		if err := ro.Put(ctx, 2, thumb, 4, 3, preview); err == nil {
			t.Errorf("writer open %v: Put on a read-only store succeeded", writerOpen)
		}
		if _, err := ro.db.Exec("DELETE FROM thumbs"); err == nil {
			t.Errorf("writer open %v: write through the read-only connection succeeded", writerOpen)
		}
		ro.Close()
	}

	// Another format version is refused with ErrFormat, the file untouched.
	raw, err := sql.Open("sqlite", filepath.Join(lib.Root(), library.MetaDir, DBName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 9"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := OpenStoreReadOnly(lib); !errors.Is(err, ErrFormat) {
		t.Errorf("OpenStoreReadOnly on version 9 = %v, want ErrFormat", err)
	}
}
