package imaging

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gen2brain/jpegli"

	"picmover/internal/testimg"
)

// heicTestdata returns a file from the gen2brain/heic module's testdata,
// or skips when the module cache does not have it.
func heicTestdata(t *testing.T, name string) []byte {
	t.Helper()
	mc := os.Getenv("GOMODCACHE")
	if mc == "" {
		mc = filepath.Join(os.Getenv("HOME"), "go", "pkg", "mod")
	}
	b, err := os.ReadFile(filepath.Join(mc, "github.com/gen2brain/heic@v0.7.2/testdata", name))
	if err != nil {
		t.Skipf("heic testdata not available: %v", err)
	}
	return b
}

func rbox(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(n))
	b = append(b, typ...)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// hostileHEICSeq builds a ~160-byte HEIC whose moov/trak sample tables
// claim n samples (stsz with uniform size) and n durations (stts).
// gen2brain/heic allocates slices of n entries for them.
func hostileHEICSeq(n uint32) []byte {
	ftyp := rbox("ftyp", []byte("heic"), u32(0), []byte("mif1heic"))
	hdlr := rbox("hdlr", u32(0), u32(0), []byte("pict"), make([]byte, 12))
	stsz := rbox("stsz", u32(0), u32(1), u32(n))
	stts := rbox("stts", u32(0), u32(1), u32(n), u32(1))
	stco := rbox("stco", u32(0), u32(1), u32(0))
	stbl := rbox("stbl", stsz, stts, stco)
	minf := rbox("minf", stbl)
	mdia := rbox("mdia", hdlr, minf)
	moov := rbox("moov", rbox("trak", mdia))
	return append(ftyp, moov...)
}

// allocated returns the bytes allocated by f.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestHEICSequenceAllocBombRefused(t *testing.T) {
	file := hostileHEICSeq(0xFFFFFFFF) // 48 GiB of sample tables upstream
	if f := Sniff(bytes.NewReader(file)); f != FormatHEIF {
		t.Fatalf("sniff = %q", f)
	}
	ctx := context.Background()
	var errs [4]error
	a := allocated(func() {
		_, _, _, errs[0] = Config(bytes.NewReader(file), int64(len(file)))
		_, _, errs[1] = Decode(ctx, bytes.NewReader(file), int64(len(file)), Options{})
		_, _, errs[2] = DecodeFit(ctx, bytes.NewReader(file), int64(len(file)), 256, Options{})
		_, _, errs[3] = Decode(ctx, bytes.NewReader(file), int64(len(file)), Options{Known: &Known{W: 10, H: 10, Format: FormatHEIF}})
	})
	for i, err := range errs {
		if !errors.Is(err, ErrUnsupported) || !bytes.Contains([]byte(err.Error()), []byte("HEIF image sequence")) {
			t.Errorf("call %d: err = %v, want ErrUnsupported (HEIF image sequence)", i, err)
		}
	}
	if a >= 1<<20 {
		t.Errorf("a %d-byte file cost %d bytes", len(file), a)
	}
}

func TestHEICSequenceSampleRefused(t *testing.T) {
	data := heicTestdata(t, "anim.heic")
	if _, _, _, err := Config(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Config: %v", err)
	}
	if _, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Decode: %v", err)
	}
}

// TestHEICIspeLie: for grid HEICs the decoder produces the tile-derived
// size whatever ispe says; the decoded size is checked against the budget.
func TestHEICIspeLie(t *testing.T) {
	for _, name := range []string{"test8.heic", "test.heic", "exif.heic"} {
		b := append([]byte(nil), heicTestdata(t, name)...)
		for i := 0; ; {
			j := bytes.Index(b[i:], []byte("ispe"))
			if j < 0 {
				break
			}
			e := i + j + 8
			binary.BigEndian.PutUint32(b[e:], 1)
			binary.BigEndian.PutUint32(b[e+4:], 1)
			i = e
		}
		img, _, err := Decode(context.Background(), bytes.NewReader(b), int64(len(b)), Options{MaxPixels: 1})
		if err == nil && img.Bounds().Dx()*img.Bounds().Dy() > 1 {
			t.Errorf("%s: pixel budget of 1 bypassed: decoded %v", name, img.Bounds())
		}
	}
}

func TestDecodedSizeChecked(t *testing.T) {
	// Known claims 1x1; the real image has 400 pixels.
	data := testimg.PNG(20, 20, 1)
	opts := Options{MaxPixels: 100, Known: &Known{W: 1, H: 1, Format: FormatPNG}}
	if _, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), opts); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	opts.MaxPixels = 400
	if img, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), opts); err != nil || img.Bounds().Dx() != 20 {
		t.Errorf("within budget: %v", err)
	}
}

// countingReader counts ReadAt calls.
type countingReader struct {
	r *bytes.Reader
	n atomic.Int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.n.Add(1)
	return c.r.ReadAt(p, off)
}

func TestKnownSkipsConfig(t *testing.T) {
	data := testimg.PNG(30, 20, 1)
	cr := &countingReader{r: bytes.NewReader(data)}
	_, _, err := Decode(context.Background(), cr, int64(len(data)), Options{MaxPixels: 10, Known: &Known{W: 30, H: 20, Format: FormatPNG}})
	if !errors.Is(err, ErrTooLarge) || cr.n.Load() != 0 {
		t.Errorf("err = %v after %d reads; want ErrTooLarge with no reads", err, cr.n.Load())
	}
	img, f, err := DecodeFit(context.Background(), cr, int64(len(data)), 10, Options{Known: &Known{W: 30, H: 20, Format: FormatPNG}})
	if err != nil || f != FormatPNG || img.Bounds().Dx() != 30 {
		t.Errorf("DecodeFit with Known: %v %q %v", img, f, err)
	}
}

func cmykJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	src := image.NewCMYK(image.Rect(0, 0, w, h))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0, 255, 255, 0 // pure red
	}
	var buf bytes.Buffer
	if err := jpegli.Encode(&buf, src, &jpegli.EncodingOptions{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	// An Adobe APP14 segment (transform 0 = CMYK), as Photoshop writes.
	app14 := []byte{0xFF, 0xEE, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0}
	return append([]byte{0xFF, 0xD8}, append(app14, buf.Bytes()[2:]...)...)
}

func TestJpegliCMYKFallsBack(t *testing.T) {
	data := cmykJPEG(t, 1200, 900)
	ctx := context.Background()
	std, _, err := Decode(ctx, bytes.NewReader(data), int64(len(data)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	fit, _, err := DecodeFit(ctx, bytes.NewReader(data), int64(len(data)), 200, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c1 := color.RGBAModel.Convert(std.At(10, 10)).(color.RGBA)
	c2 := color.RGBAModel.Convert(fit.At(10, 10)).(color.RGBA)
	diff := func(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }
	if diff(c1.R, c2.R) > 20 || diff(c1.G, c2.G) > 20 || diff(c1.B, c2.B) > 20 {
		t.Errorf("scaled and full decodes disagree: %v vs %v", c1, c2)
	}
	// The fallback is what is being exercised: jpegli itself returns the
	// samples without the Adobe inversion (Photoshop stores CMYK inverted;
	// this fixture's samples are inverted by the APP14 marker added after
	// encoding, so image/jpeg reads pure red as black and jpegli as red).
	raw, err := jpegliScaled(bytes.NewReader(data), 200, 150)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := raw.(*image.CMYK); !ok {
		t.Fatalf("jpegli returned %T, want *image.CMYK", raw)
	}
	c3 := color.RGBAModel.Convert(raw.At(10, 10)).(color.RGBA)
	t.Logf("standard %v, DecodeFit %v, raw jpegli %v", c1, c2, c3)
	if diff(c1.R, c3.R)+diff(c1.G, c3.G)+diff(c1.B, c3.B) < 60 {
		t.Errorf("fixture no longer distinguishes jpegli from image/jpeg")
	}
}

// tinyProgressiveJPEG is a ~1 KB JPEG whose SOF2 claims 10000x10000: within
// the 100 MP budget, but implausible for its size.
func tinyProgressiveJPEG(t *testing.T) []byte {
	data := append([]byte(nil), testimg.JPEG(testimg.Options{Width: 16, Height: 16, NoExif: true})...)
	sof := bytes.Index(data, []byte{0xFF, 0xC0})
	sos := bytes.Index(data, []byte{0xFF, 0xDA})
	if sof < 0 || sos < 0 {
		t.Fatal("markers not found")
	}
	data[sof+1] = 0xC2
	binary.BigEndian.PutUint16(data[sof+5:], 10000)
	binary.BigEndian.PutUint16(data[sof+7:], 10000)
	ns := int(data[sos+4])
	data[sos+5+2*ns+1] = 0 // Se = 0: DC-only first scan
	return data
}

// tinyPNG is a PNG header claiming w x h pixels with a valid IHDR CRC and
// no image data.
func tinyPNG(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	return append([]byte("\x89PNG\r\n\x1a\n"), testimg.PNGChunk("IHDR", ihdr)...)
}

func TestImplausibleHeadersRefused(t *testing.T) {
	ctx := context.Background()
	jpg := tinyProgressiveJPEG(t)
	png := tinyPNG(10000, 10000)
	_, _, _ = DecodeFit(ctx, bytes.NewReader(jpg), int64(len(jpg)), 1600, Options{}) // warm up
	for name, call := range map[string]func() error{
		"jpeg DecodeFit": func() error {
			_, _, err := DecodeFit(ctx, bytes.NewReader(jpg), int64(len(jpg)), 1600, Options{})
			return err
		},
		"jpeg Decode": func() error {
			_, _, err := Decode(ctx, bytes.NewReader(jpg), int64(len(jpg)), Options{})
			return err
		},
		"png Decode": func() error {
			_, _, err := Decode(ctx, bytes.NewReader(png), int64(len(png)), Options{})
			return err
		},
	} {
		var err error
		if a := allocated(func() { err = call() }); a >= 1<<20 {
			t.Errorf("%s: allocated %d bytes", name, a)
		}
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: err = %v, want ErrTooLarge", name, err)
		}
	}
}

// rawPNG builds a PNG with the given IHDR fields whose IDAT is the zlib
// compression of rows (each a filter byte plus packed samples).
func rawPNG(t *testing.T, w, h uint32, depth, colorType byte, rows [][]byte) []byte {
	t.Helper()
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, depth, colorType, 0, 0, 0)
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := zw.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	out := append([]byte("\x89PNG\r\n\x1a\n"), testimg.PNGChunk("IHDR", ihdr)...)
	out = append(out, testimg.PNGChunk("IDAT", z.Bytes())...)
	return append(out, testimg.PNGChunk("IEND", nil)...)
}

// A 1-bit grayscale scan (here a white band over a black page) compresses
// to far more than 4096 pixels per byte but is within deflate's ratio on
// raw bytes; it must decode.
func TestOneBitPNGDecodes(t *testing.T) {
	const n = 2000
	rows := make([][]byte, n)
	for y := range rows {
		rows[y] = make([]byte, 1+(n+7)/8) // filter 0, black
		if y < 8 {
			for i := 1; i < len(rows[y]); i++ {
				rows[y][i] = 0xFF // white
			}
		}
	}
	data := rawPNG(t, n, n, 1, 0, rows)
	if px := int64(n * n); px <= 4096*int64(len(data)) {
		t.Fatalf("fixture too large (%d bytes) to exercise the old pixel bound", len(data))
	}
	img, f, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{})
	if err != nil || f != FormatPNG {
		t.Fatalf("%d-byte 1-bit PNG: %q %v", len(data), f, err)
	}
	if b := img.Bounds(); b.Dx() != n || b.Dy() != n {
		t.Fatalf("bounds %v", b)
	}
	g := color.GrayModel.Convert
	if g(img.At(n-5, 5)).(color.Gray).Y != 255 || g(img.At(5, n-5)).(color.Gray).Y != 0 {
		t.Errorf("pixels %v %v", img.At(n-5, 5), img.At(5, n-5))
	}
}

func TestPNGMemoryEstimate(t *testing.T) {
	row := make([]byte, 1+8*8)
	data16 := rawPNG(t, 8, 4, 16, 6, [][]byte{row, row, row, row})
	_, _, _, mem, err := prepare(bytes.NewReader(data16), int64(len(data16)), Options{})
	if err != nil || mem != 8*4*8 {
		t.Errorf("16-bit RGBA: mem %d, err %v; want %d", mem, err, 8*4*8)
	}
	row = make([]byte, 2)
	data1 := rawPNG(t, 8, 4, 1, 0, [][]byte{row, row, row, row})
	_, _, _, mem, err = prepare(bytes.NewReader(data1), int64(len(data1)), Options{})
	if err != nil || mem != 8*4*4 {
		t.Errorf("1-bit gray: mem %d, err %v; want %d", mem, err, 8*4*4)
	}
}

// A truncated IHDR skips the plausibility check and reaches the decoder,
// which reports its own error.
func TestPNGShortIHDRFallsThrough(t *testing.T) {
	data := tinyPNG(100, 100)[:20]
	if _, ok := readPNGIHDR(bytes.NewReader(data)); ok {
		t.Fatal("short IHDR parsed")
	}
	_, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{Known: &Known{W: 100, H: 100, Format: FormatPNG}})
	if err == nil || errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want a decode error", err)
	}
	if _, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{}); err == nil || errors.Is(err, ErrTooLarge) {
		t.Errorf("without Known: err = %v, want a config error", err)
	}
}

func TestJpegliTooLargeNoFallback(t *testing.T) {
	// Called directly, bypassing prepare's plausibility check, so jpegli
	// itself rejects the file with its ErrTooLarge. That must come back as
	// our ErrTooLarge, not as a request to fall back to image/jpeg.
	if !errors.Is(func() error {
		_, err := decodeJPEGScaled(context.Background(), bytes.NewReader(tinyProgressiveJPEG(t)), 10000, 10000, 1600)
		return err
	}(), ErrTooLarge) {
		t.Error("jpegli ErrTooLarge not propagated")
	}
}

func TestMemBudget(t *testing.T) {
	ctx := context.Background()
	b := NewMemBudget(100)
	if err := b.Acquire(ctx, 60); err != nil {
		t.Fatal(err)
	}
	// 60 + 50 > 100: blocks until the context ends.
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	if err := b.Acquire(tctx, 50); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("over budget: %v", err)
	}
	cancel()
	// A release wakes a waiter.
	done := make(chan error, 1)
	go func() { done <- b.Acquire(ctx, 50) }()
	time.Sleep(10 * time.Millisecond)
	b.Release(60)
	if err := <-done; err != nil || b.Held() != 50 {
		t.Errorf("after release: %v, held %d", err, b.Held())
	}
	b.Release(50)
	// An acquisition larger than the budget proceeds alone.
	if err := b.Acquire(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	tctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
	if err := b.Acquire(tctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("beside a huge holder: %v", err)
	}
	cancel()
	b.Release(1000)
	// nil budget: no limit.
	var nb *MemBudget
	if err := nb.Acquire(ctx, 1<<40); err != nil {
		t.Error(err)
	}
	nb.Release(1 << 40)

	// Concurrent users never exceed the budget.
	b = NewMemBudget(10)
	var cur, peak atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.Acquire(ctx, 4); err != nil {
				t.Error(err)
				return
			}
			n := cur.Add(4)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			cur.Add(-4)
			b.Release(4)
		}()
	}
	wg.Wait()
	if peak.Load() > 10 || b.Held() != 0 {
		t.Errorf("peak %d, held %d", peak.Load(), b.Held())
	}
}

func TestDecodeWaitsForBudgetAndSlots(t *testing.T) {
	ctx := context.Background()
	data := testimg.PNG(20, 20, 1)
	budget := NewMemBudget(1 << 20)
	if err := budget.Acquire(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// 20*20*4 = 1600 bytes do not fit beside 1 MiB - 1... they do; hold
	// almost everything instead.
	if err := budget.Acquire(ctx, 1<<20-2); err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, _, err := Decode(tctx, bytes.NewReader(data), int64(len(data)), Options{Budget: budget}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("budget exhausted: %v", err)
	}
	budget.Release(1<<20 - 1)
	if _, _, err := Decode(ctx, bytes.NewReader(data), int64(len(data)), Options{Budget: budget}); err != nil || budget.Held() != 0 {
		t.Errorf("budget free: %v, held %d", err, budget.Held())
	}

	// HEIF decodes take a slot; with none free they wait. Known skips
	// Config, so the fake coded data is never parsed.
	heif := testimg.HEIF(testimg.HEIFOptions{Width: 64, Height: 48})
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	tctx2, cancel2 := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel2()
	opts := Options{HEIFSlots: slots, Budget: budget, Known: &Known{W: 64, H: 48, Format: FormatHEIF}}
	if _, _, err := Decode(tctx2, bytes.NewReader(heif), int64(len(heif)), opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no HEIF slot: %v", err)
	}
	<-slots
	_, _, _ = Decode(ctx, bytes.NewReader(heif), int64(len(heif)), opts) // fails on the fake data
	if len(slots) != 0 || budget.Held() != 0 {
		t.Errorf("slot or budget leaked: %d slots, %d bytes", len(slots), budget.Held())
	}
}
