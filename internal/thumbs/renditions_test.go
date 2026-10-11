package thumbs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"picmover/internal/imaging"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
)

// jpegPathPHash is the pHash the JPEG renditions (before WebP) gave for an
// upright image: the preview encoded as JPEG at PreviewQuality, decoded
// at thumbnail scale.
func jpegPathPHash(t *testing.T, img image.Image) uint64 {
	t.Helper()
	var buf bytes.Buffer
	if err := imaging.EncodeJPEG(&buf, flatten(imaging.Fit(img, PreviewSize)), PreviewQuality); err != nil {
		t.Fatal(err)
	}
	small, _, err := imaging.DecodeFit(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len()), phashFit, imaging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return imaging.PHash(small)
}

// photoScene is a photo-like test scene: overlapping shapes in varied
// colours (strong low-frequency structure, as in photographs) with mild
// sensor-like noise.
func photoScene(w, h int, seed uint32) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := seed*2654435761 + 1
	next := func(n int) int {
		rnd ^= rnd << 13
		rnd ^= rnd >> 17
		rnd ^= rnd << 5
		return int(rnd % uint32(n))
	}
	type shape struct {
		cx, cy, r int
		col       [3]int
		disc      bool
	}
	shapes := make([]shape, 6)
	for i := range shapes {
		shapes[i] = shape{next(w), next(h), w/8 + next(w/3), [3]int{next(256), next(256), next(256)}, next(2) == 0}
	}
	sky := [3]int{next(256), next(256), next(256)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := [3]int{sky[0] * (h - y) / h, sky[1] * (h - y) / h, sky[2]}
			for _, s := range shapes {
				dx, dy := x-s.cx, y-s.cy
				in := dx*dx+dy*dy < s.r*s.r
				if !s.disc {
					in = max(dx, -dx) < s.r && max(dy, -dy) < s.r*2/3
				}
				if in {
					c = s.col
				}
			}
			n := next(17) - 8
			o := y*img.Stride + 4*x
			for k := 0; k < 3; k++ {
				img.Pix[o+k] = uint8(max(0, min(255, c[k]+n)))
			}
			img.Pix[o+3] = 255
		}
	}
	return img
}

// The pHash computed from the WebP preview stays within 4 bits of the one
// the JPEG preview gave on photo-like content, so near-duplicate pairs
// found before and after the switch stay comparable.
//
// Synthetic gradients and noise (testimg's samples, the smooth scenes)
// are logged, not asserted: their pHash has many AC coefficients next to
// the median, so it moves by up to ~25 bits between two JPEG qualities
// already (the "JPEG q80" column), and the WebP path moves it about as
// much.
func TestPHashWebPMatchesJPEG(t *testing.T) {
	g := newGen(t)
	type sample struct {
		name   string
		data   []byte
		assert bool
	}
	var samples []sample
	for i, sz := range [][2]int{{2000, 1500}, {1500, 2000}, {1800, 1200}, {1600, 1200}, {1200, 900}, {2400, 1200}} {
		samples = append(samples, sample{"photo.jpg", encJPEG(photoScene(sz[0], sz[1], uint32(i+1))), true})
	}
	samples = append(samples,
		sample{"gradient.jpg", testimg.BigJPEG(2000, 1500), false},
		sample{"camera.jpg", testimg.JPEG(testimg.Options{Width: 400, Height: 300, Seed: 3}), false},
		sample{"phone.jpg", testimg.JPEG(testimg.Options{Width: 64, Height: 48, Seed: 1}), false},
		sample{"scan.png", testimg.PNG(32, 32, 5), false},
	)
	for _, seed := range []float64{0.1, 0.3, 0.55, 0.71, 0.9} {
		samples = append(samples, sample{"smooth.png", encPNG(smooth(2400, 1600, seed)), false})
	}
	for i, s := range samples {
		img, _, err := imaging.Decode(context.Background(), bytes.NewReader(s.data), int64(len(s.data)), imaging.Options{})
		if err != nil {
			t.Fatal(err)
		}
		res := g.Generate(context.Background(), Source{Path: write(t, s.name, s.data), Kind: metadata.KindImage, Hash: uint64(0x500 + i)}, false)
		if res.State != StateOK {
			t.Fatalf("%s: %+v", s.name, res)
		}
		want := jpegPathPHash(t, img)
		d := imaging.Distance(res.PHash, want)
		// The pHash's own instability: JPEG at quality 80 instead of 85.
		var q80 bytes.Buffer
		if err := imaging.EncodeJPEG(&q80, flatten(imaging.Fit(img, PreviewSize)), 80); err != nil {
			t.Fatal(err)
		}
		small, _, err := imaging.DecodeFit(context.Background(), bytes.NewReader(q80.Bytes()), int64(q80.Len()), phashFit, imaging.Options{})
		if err != nil {
			t.Fatal(err)
		}
		floor := imaging.Distance(imaging.PHash(small), want)
		t.Logf("%-12s #%2d: WebP vs JPEG %2d bits (JPEG q80 vs q85: %2d)", s.name, i, d, floor)
		if s.assert && d > 4 {
			t.Errorf("%s #%d: WebP pHash %016x is %d bits from the JPEG path's %016x", s.name, i, res.PHash, d, want)
		}
	}
}

// putLegacy stores renditions as an older version did: JPEG thumbnail and
// JPEG preview, no large thumbnail.
func putLegacy(t *testing.T, s *Store, hash uint64, w, h int) []byte {
	t.Helper()
	preview := encJPEG(smooth(w, h, 0.4))
	if err := s.Put(context.Background(), hash, testimg.BareJPEG(), 16, 12, nil, preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestStoreLargeAndMixedFormats(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	const hash = 0x4200000000000042
	legacy := putLegacy(t, s, hash, 1600, 1000)
	if th, lg, pv, err := s.Has(ctx, hash); !th || lg || !pv || err != nil {
		t.Fatalf("Has = %v %v %v %v; want thumb and preview, no large", th, lg, pv, err)
	}
	if !strings.HasSuffix(s.PreviewPath(hash), ".jpg") {
		t.Fatalf("PreviewPath %s", s.PreviewPath(hash))
	}

	// A WebP file next to the JPEG wins the lookup.
	var wbuf bytes.Buffer
	if err := imaging.EncodeWebP(&wbuf, smooth(160, 100, 0.4), 85); err != nil {
		t.Fatal(err)
	}
	webpPath := strings.TrimSuffix(s.PreviewPath(hash), ".jpg") + ".webp"
	if err := os.WriteFile(webpPath, wbuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.PreviewPath(hash); got != webpPath {
		t.Errorf("PreviewPath with both = %s, want the .webp", got)
	}
	os.Remove(webpPath)

	// A Put of WebP renditions replaces the JPEG preview (the .jpg is
	// removed) and writes the large thumbnail.
	large := []byte("RIFF\x00\x00\x00\x00WEBPlarge")
	if err := s.Put(ctx, hash, wbuf.Bytes(), 16, 10, large, wbuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSuffix(webpPath, ".webp") + ".jpg"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("JPEG preview kept after a WebP Put (%v)", err)
	}
	if got := s.PreviewPath(hash); got != webpPath {
		t.Errorf("PreviewPath = %s", got)
	}
	if th, lg, pv, err := s.Has(ctx, hash); !th || !lg || !pv || err != nil {
		t.Fatalf("Has = %v %v %v %v", th, lg, pv, err)
	}
	if b, _ := os.ReadFile(s.LargePath(hash)); !bytes.Equal(b, large) {
		t.Error("large thumbnail not stored")
	}
	n, size, err := s.Stats(ctx)
	if err != nil || n != 1 || size != int64(2*wbuf.Len()+len(large)) {
		t.Errorf("Stats = %d %d %v; want 1, %d", n, size, err, 2*wbuf.Len()+len(large))
	}

	// A Put without a large thumbnail removes the stale one; the next
	// fill derives it from the (legacy JPEG) preview.
	putLegacy(t, s, hash, 1600, 1000)
	if _, lg, _, _ := s.Has(ctx, hash); lg {
		t.Error("large thumbnail of the previous preview kept")
	}
	_ = legacy

	// Delete removes all of it.
	if err := s.Put(ctx, hash, wbuf.Bytes(), 16, 10, large, wbuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, hash); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.LargePath(hash), webpPath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left after Delete (%v)", p, err)
		}
	}
}

// A JPEG preview (an older version's, or any legacy one) gets a WebP large
// thumbnail derived from it; the original is never read.
func TestFillLargeFromJPEGPreview(t *testing.T) {
	g := newGen(t)
	ctx := context.Background()
	const hash = 0x7100000000000001
	putLegacy(t, g.Store, hash, 1600, 1000)
	filled, err := g.FillLarge(ctx, hash)
	if err != nil || !filled {
		t.Fatalf("FillLarge = %v, %v", filled, err)
	}
	if l := storedLarge(t, g.Store, hash); size(l) != "640x400" {
		t.Errorf("large %s", size(l))
	}
	// The preview stays the JPEG it was.
	if !strings.HasSuffix(g.Store.PreviewPath(hash), ".jpg") {
		t.Errorf("preview now %s", g.Store.PreviewPath(hash))
	}
	// A second fill finds it and writes nothing.
	before, _ := os.Stat(g.Store.LargePath(hash))
	if filled, err := g.FillLarge(ctx, hash); filled || err != nil {
		t.Errorf("second FillLarge = %v, %v", filled, err)
	}
	if after, _ := os.Stat(g.Store.LargePath(hash)); !os.SameFile(before, after) {
		t.Error("existing large thumbnail replaced")
	}
	// No preview: ErrNoPreview.
	if _, err := g.FillLarge(ctx, 0x99); !errors.Is(err, ErrNoPreview) {
		t.Errorf("FillLarge without preview = %v", err)
	}
	// A preview smaller than the large size is kept at its size.
	putLegacy(t, g.Store, 0x72, 300, 200)
	if _, err := g.FillLarge(ctx, 0x72); err != nil {
		t.Fatal(err)
	}
	if l := storedLarge(t, g.Store, 0x72); size(l) != "300x200" {
		t.Errorf("small large %s", size(l))
	}
}

// Generate on cached renditions without a large thumbnail derives it from
// the preview, even when the original is gone.
func TestGenerateCachedFillsLarge(t *testing.T) {
	g := newGen(t)
	ctx := context.Background()
	p := write(t, "a.png", encPNG(smooth(1200, 800, 0.3)))
	src := Source{Path: p, Kind: metadata.KindImage, Hash: 77}
	first := g.Generate(ctx, src, false)
	if first.State != StateOK || first.LargeFilled {
		t.Fatalf("%+v", first)
	}
	os.Remove(g.Store.LargePath(77))
	os.Remove(p)
	res := g.Generate(ctx, src, false)
	if res.State != StateOK || !res.LargeFilled || res.LargeError != "" || res.PHash != first.PHash {
		t.Fatalf("cached %+v", res)
	}
	if l := storedLarge(t, g.Store, 77); size(l) != "640x427" {
		t.Errorf("large %s", size(l))
	}
}

// PutLarge works on a read-only store, never replaces, and leaves no temp
// file; OpenStore removes stale temp files only.
func TestPutLargeReadOnlyAndClean(t *testing.T) {
	s, lib := openTestStore(t)
	putLegacy(t, s, 0x10, 800, 600)
	ro, err := OpenStoreReadOnly(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if written, err := ro.PutLarge(0x10, []byte("one")); !written || err != nil {
		t.Fatalf("PutLarge = %v, %v", written, err)
	}
	if written, err := ro.PutLarge(0x10, []byte("two")); written || err != nil {
		t.Fatalf("second PutLarge = %v, %v", written, err)
	}
	if b, _ := os.ReadFile(ro.LargePath(0x10)); string(b) != "one" {
		t.Errorf("large = %q", b)
	}
	fan := filepath.Dir(ro.LargePath(0x10))
	ents, _ := os.ReadDir(fan)
	if len(ents) != 1 {
		t.Errorf("fan-out dir holds %d entries", len(ents))
	}
	// Temp files: a stale one is removed at open, a fresh one kept.
	stale := filepath.Join(fan, ".0000000000000010-1"+tmpSuffix)
	fresh := filepath.Join(fan, ".0000000000000010-2"+tmpSuffix)
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTmpAge)
	os.Chtimes(stale, old, old)
	// Stats ignores temp files.
	_, before, _ := s.Stats(context.Background())
	s.Close()
	s2, err := OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp kept (%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp removed (%v)", err)
	}
	if _, after, _ := s2.Stats(context.Background()); after != before {
		t.Errorf("Stats bytes %d, before %d", after, before)
	}
	// Presence sees the large thumbnail and the preview, not temp files.
	pr := s2.NewPresence()
	if !pr.Large(0x10) || !pr.Preview(0x10) || pr.Large(0x11) || pr.Preview(0x11) {
		t.Error("Presence wrong")
	}
}

// A .old JPEG preview is removed at open when a WebP preview of the same
// hash landed (an interrupted format change), restored when none did.
func TestCleanOldAcrossFormats(t *testing.T) {
	s, lib := openTestStore(t)
	ctx := context.Background()
	putLegacy(t, s, 0x20, 100, 80)
	putLegacy(t, s, 0x21, 100, 80)
	var wbuf bytes.Buffer
	if err := imaging.EncodeWebP(&wbuf, smooth(100, 80, 0.4), 85); err != nil {
		t.Fatal(err)
	}
	jpg20, jpg21 := s.PreviewPath(0x20), s.PreviewPath(0x21)
	// 0x20: the WebP landed, the JPEG is still .old.
	if err := os.Rename(jpg20, jpg20+oldSuffix); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(jpg20, ".jpg")+".webp", wbuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// 0x21: crashed before the WebP landed.
	if err := os.Rename(jpg21, jpg21+oldSuffix); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := OpenStore(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := os.Stat(jpg20 + oldSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("0x20 .old kept (%v)", err)
	}
	if !strings.HasSuffix(s2.PreviewPath(0x20), ".webp") {
		t.Errorf("0x20 preview %s", s2.PreviewPath(0x20))
	}
	if _, err := os.Stat(jpg21); err != nil {
		t.Errorf("0x21 not restored (%v)", err)
	}
	if _, _, pv, _ := s2.Has(ctx, 0x21); !pv {
		t.Error("0x21 has no preview")
	}
	_ = library.MetaDir
}

// Review fixes (docs/design/renditions.md, "Review fixes"): a zero-length
// large is replaced, failed derivations are remembered per Store, and an
// unwritable large directory still yields the derived bytes.
func TestLargeEmptyFailedUnwritable(t *testing.T) {
	ctx := context.Background()
	s, lib := openTestStore(t)
	putLegacy(t, s, 0x20, 800, 600)
	putLegacy(t, s, 0x21, 800, 600)

	// An empty large file is not "present" for writing: PutLarge replaces it.
	if err := os.MkdirAll(filepath.Dir(s.LargePath(0x20)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.LargePath(0x20), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if filled, err := DeriveLarge(ctx, s, 0x20, imaging.Options{}); !filled || err != nil {
		t.Fatalf("DeriveLarge over an empty file = %v, %v", filled, err)
	}
	if st, err := os.Stat(s.LargePath(0x20)); err != nil || st.Size() == 0 {
		t.Fatalf("large after replacing the empty file: %v %v", st, err)
	}

	// An undecodable preview fails once, then is refused without decoding.
	if err := os.WriteFile(s.PreviewPath(0x21), []byte("\xff\xd8\xff\xe0 not a jpeg at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveLarge(ctx, s, 0x21, imaging.Options{}); err == nil || errors.Is(err, ErrLargeFailed) || !s.LargeFailed(0x21) {
		t.Fatalf("first DeriveLarge = %v, failed %v", err, s.LargeFailed(0x21))
	}
	if _, err := DeriveLargeData(ctx, s, 0x21, imaging.Options{}); !errors.Is(err, ErrLargeFailed) {
		t.Fatalf("second derivation = %v, want ErrLargeFailed", err)
	}
	// A missing preview is not remembered (it may be generated later).
	if _, err := DeriveLarge(ctx, s, 0x99, imaging.Options{}); !errors.Is(err, ErrNoPreview) || s.LargeFailed(0x99) {
		t.Fatalf("no preview: %v, failed %v", err, s.LargeFailed(0x99))
	}
	if s.MarkLargeFailed(0x21) {
		t.Error("MarkLargeFailed of a known hash reported new")
	}
	// A reopened Store starts afresh.
	s2, err := OpenStoreReadOnly(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.LargeFailed(0x21) {
		t.Error("failed hash carried over to another Store")
	}

	// Unwritable large directory: the bytes are derived and returned, the
	// write fails once and is then skipped; a later successful write
	// clears the state.
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		return
	}
	os.Remove(s.LargePath(0x20))
	fan := filepath.Dir(s.LargePath(0x20))
	if err := os.Chmod(fan, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(fan, 0o755) })
	res, err := DeriveLargeData(ctx, s2, 0x20, imaging.Options{})
	if err != nil || len(res.Data) == 0 || res.Written || res.WriteErr == nil || errors.Is(res.WriteErr, ErrLargeUnwritable) || !s2.LargeUnwritable() {
		t.Fatalf("read-only: %d bytes, written %v, %v, %v", len(res.Data), res.Written, res.WriteErr, err)
	}
	res, err = DeriveLargeData(ctx, s2, 0x20, imaging.Options{})
	if err != nil || len(res.Data) == 0 || !errors.Is(res.WriteErr, ErrLargeUnwritable) {
		t.Fatalf("read-only again: %d bytes, %v, %v", len(res.Data), res.WriteErr, err)
	}
	os.Chmod(fan, 0o755)
	if written, err := s2.PutLarge(0x20, res.Data); !written || err != nil || s2.LargeUnwritable() {
		t.Fatalf("PutLarge after chmod = %v, %v, unwritable %v", written, err, s2.LargeUnwritable())
	}
}
