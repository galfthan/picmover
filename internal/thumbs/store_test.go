package thumbs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"picmover/internal/library"
	"picmover/internal/testimg"
)

func openTestStore(t *testing.T) (*Store, *library.Library) {
	t.Helper()
	lib, err := library.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, lib
}

func TestStoreRoundTrip(t *testing.T) {
	s, lib := openTestStore(t)
	ctx := context.Background()
	const hash = 0xabcdef0123456789
	// Without a preview, PreviewPath names the WebP file a new one gets.
	want := filepath.Join(lib.Root(), library.MetaDir, PreviewDir, "ab", "abcdef0123456789.webp")
	if got := s.PreviewPath(hash); got != want {
		t.Errorf("PreviewPath = %q, want %q", got, want)
	}
	wantLarge := filepath.Join(lib.Root(), library.MetaDir, LargeDir, "ab", "abcdef0123456789.webp")
	if got := s.LargePath(hash); got != wantLarge {
		t.Errorf("LargePath = %q, want %q", got, wantLarge)
	}
	if _, err := os.Stat(filepath.Join(lib.Root(), library.MetaDir, DBName)); err != nil {
		t.Errorf("thumbs.db: %v", err)
	}

	if _, _, _, ok, err := s.Thumb(ctx, hash); ok || err != nil {
		t.Fatalf("Thumb before Put: ok=%v err=%v", ok, err)
	}
	if th, _, pv, err := s.Has(ctx, hash); th || pv || err != nil {
		t.Fatalf("Has before Put = %v %v %v", th, pv, err)
	}

	thumb := testimg.BareJPEG()
	preview := testimg.JPEG(testimg.Options{Width: 40, Height: 30, NoExif: true})
	if err := s.Put(ctx, hash, thumb, 16, 12, nil, preview); err != nil {
		t.Fatal(err)
	}
	got, w, h, ok, err := s.Thumb(ctx, hash)
	if err != nil || !ok || w != 16 || h != 12 || !bytes.Equal(got, thumb) {
		t.Fatalf("Thumb = %d bytes %dx%d ok=%v err=%v", len(got), w, h, ok, err)
	}
	// JPEG preview bytes (as older versions wrote) keep the .jpg name.
	if got := s.PreviewPath(hash); got != strings.TrimSuffix(want, ".webp")+".jpg" {
		t.Errorf("PreviewPath of a JPEG preview = %q", got)
	}
	if pb, err := os.ReadFile(s.PreviewPath(hash)); err != nil || !bytes.Equal(pb, preview) {
		t.Fatalf("preview file: %v", err)
	}
	if th, _, pv, err := s.Has(ctx, hash); !th || !pv || err != nil {
		t.Fatalf("Has = %v %v %v", th, pv, err)
	}
	// The temp dir holds no leftovers.
	if ents, _ := os.ReadDir(lib.TempDir()); len(ents) != 0 {
		t.Errorf("temp dir not empty: %v", ents)
	}

	// Replace.
	if err := s.Put(ctx, hash, preview, 40, 30, nil, thumb); err != nil {
		t.Fatal(err)
	}
	if _, w, h, _, _ := s.Thumb(ctx, hash); w != 40 || h != 30 {
		t.Errorf("after replace %dx%d", w, h)
	}

	// The hash with the top bit set survives the signed INTEGER mapping.
	const high = 0xffffffffffffffff
	if err := s.Put(ctx, high, thumb, 1, 1, nil, preview); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, _ := s.Thumb(ctx, high); !ok {
		t.Error("high hash not found")
	}

	n, size, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantSize := int64(len(preview) + len(thumb) + len(thumb) + len(preview))
	if n != 2 || size != wantSize {
		t.Errorf("Stats = %d, %d; want 2, %d", n, size, wantSize)
	}

	if err := s.Delete(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if th, _, pv, err := s.Has(ctx, hash); th || pv || err != nil {
		t.Errorf("Has after Delete = %v %v %v", th, pv, err)
	}
	if err := s.Delete(ctx, hash); err != nil {
		t.Errorf("second Delete: %v", err)
	}
	if n, _, _ := s.Stats(ctx); n != 1 {
		t.Errorf("count after delete = %d", n)
	}
	if err := s.Put(ctx, 1, nil, 1, 1, nil, preview); err == nil {
		t.Error("Put with empty thumb succeeded")
	}
}

func TestStorePutFailureLeavesNothing(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	thumb, preview := testimg.BareJPEG(), testimg.BareJPEG()

	// 1. The preview cannot be written: the fan-out directory is a file.
	const h1 = 0x1200000000000001
	fan := filepath.Dir(s.PreviewPath(h1))
	if err := os.WriteFile(fan, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, h1, thumb, 1, 1, nil, preview); err == nil {
		t.Fatal("Put succeeded although the preview directory is a file")
	}
	if th, _, _, err := s.Has(ctx, h1); th || err != nil {
		t.Errorf("row exists after failed preview write (err %v)", err)
	}

	// 2. The row insert fails after the preview was renamed into place:
	// the preview is removed again.
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON thumbs BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	const h2 = 0x3400000000000002
	if err := s.Put(ctx, h2, thumb, 1, 1, nil, preview); err == nil {
		t.Fatal("Put succeeded despite the failing insert")
	}
	if th, _, pv, err := s.Has(ctx, h2); th || pv || err != nil {
		t.Errorf("after failed insert: thumb=%v preview=%v err=%v", th, pv, err)
	}

	// 3. A cancelled context writes nothing.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	const h3 = 0x5600000000000003
	if err := s.Put(cctx, h3, thumb, 1, 1, nil, preview); !errors.Is(err, context.Canceled) {
		t.Errorf("Put with cancelled ctx = %v", err)
	}
	if _, err := os.Stat(s.PreviewPath(h3)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("preview written with cancelled ctx: %v", err)
	}
}

func TestStoreRefusesOtherVersion(t *testing.T) {
	lib, err := library.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenStore(lib); !errors.Is(err, ErrFormat) {
		t.Fatalf("OpenStore on version 7 = %v, want ErrFormat", err)
	}

	// A foreign database (tables but version 0) is refused too.
	lib2, _ := library.Open(t.TempDir())
	db, err := sql.Open("sqlite", filepath.Join(lib2.Root(), library.MetaDir, DBName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE other (x)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := OpenStore(lib2); !errors.Is(err, ErrFormat) {
		t.Fatalf("OpenStore on foreign db = %v, want ErrFormat", err)
	}

	// Reopening a valid store works.
	lib3, _ := library.Open(t.TempDir())
	for i := 0; i < 2; i++ {
		s, err := OpenStore(lib3)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}
