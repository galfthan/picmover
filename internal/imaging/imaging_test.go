package imaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"picmover/internal/testimg"
)

func decodeBytes(t *testing.T, b []byte) (image.Image, Format) {
	t.Helper()
	img, f, err := Decode(context.Background(), bytes.NewReader(b), int64(len(b)), Options{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return img, f
}

var orientTIFF = testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0112, 6)}})

type fixture struct {
	name string
	data []byte
	want Format
}

func fixtures() []fixture {
	return []fixture{
		{"jpeg", testimg.JPEG(testimg.Options{Width: 24, Height: 16, NoExif: true}), FormatJPEG},
		{"jpeg-exif", testimg.JPEG(testimg.Options{Width: 24, Height: 16, Orientation: 6}), FormatJPEG},
		{"png", testimg.PNG(24, 16, 1), FormatPNG},
		{"gif", testimg.GIF(24, 16, 2), FormatGIF},
		{"bmp", testimg.BMP(24, 16, 3), FormatBMP},
		{"tiff", testimg.TIFFImage(24, 16, 4), FormatTIFF},
		{"webp", testimg.WebP(24, 16, 5), FormatWebP},
		{"webp-exif", testimg.WebPWithExif(24, 16, 5, orientTIFF, true), FormatWebP},
		{"png-exif", testimg.PNGWithExif(24, 16, 1, orientTIFF), FormatPNG},
		{"avif", testimg.AVIF(24, 16, 6), FormatAVIF},
	}
}

func TestDecodeFormats(t *testing.T) {
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			if got := Sniff(bytes.NewReader(fx.data)); got != fx.want {
				t.Fatalf("Sniff = %q, want %q", got, fx.want)
			}
			w, h, f, err := Config(bytes.NewReader(fx.data), int64(len(fx.data)))
			if err != nil || w != 24 || h != 16 || f != fx.want {
				t.Fatalf("Config = %d %d %q %v", w, h, f, err)
			}
			img, f := decodeBytes(t, fx.data)
			if f != fx.want {
				t.Fatalf("format %q, want %q", f, fx.want)
			}
			if b := img.Bounds(); b.Dx() != 24 || b.Dy() != 16 {
				t.Fatalf("bounds %v", b)
			}
		})
	}
}

func TestDecodeHEIC(t *testing.T) {
	path := filepath.Join("..", "testimg", "testdata", "sample.heic")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("HEIC sample not available (%v); add a small HEIC at internal/testimg/testdata/sample.heic", err)
	}
	if f := Sniff(bytes.NewReader(data)); f != FormatHEIF {
		t.Fatalf("Sniff = %q", f)
	}
	w, h, _, err := Config(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	img, f := decodeBytes(t, data)
	if f != FormatHEIF || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Fatalf("decoded %q %v, config %dx%d", f, img.Bounds(), w, h)
	}
}

func TestSniff(t *testing.T) {
	ftyp := func(major string, compat ...string) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(16+4*len(compat)))
		b = append(b, "ftyp"+major+"\x00\x00\x00\x00"...)
		for _, c := range compat {
			b = append(b, c...)
		}
		return append(b, make([]byte, 16)...)
	}
	cases := []struct {
		name string
		data []byte
		want Format
	}{
		{"empty", nil, FormatUnknown},
		{"garbage", []byte("this is not an image at all, just some text"), FormatUnknown},
		{"short-jpeg", []byte{0xFF, 0xD8}, FormatUnknown},
		{"heic", ftyp("heic", "mif1", "heic"), FormatHEIF},
		{"mif1-heic", ftyp("mif1", "mif1", "heic"), FormatHEIF},
		{"mif1-only", ftyp("mif1", "mif1"), FormatHEIF},
		{"mif1-avif", ftyp("mif1", "mif1", "avif"), FormatAVIF},
		{"avif", ftyp("avif", "mif1", "miaf"), FormatAVIF},
		{"mp4", ftyp("isom", "isom", "mp41"), FormatUnknown},
		{"cr3", ftyp("crx ", "crx ", "isom"), FormatUnknown},
		{"riff-avi", []byte("RIFF\x00\x00\x00\x00AVI LIST"), FormatUnknown},
	}
	for _, c := range cases {
		if got := Sniff(bytes.NewReader(c.data)); got != c.want {
			t.Errorf("%s: Sniff = %q, want %q", c.name, got, c.want)
		}
	}
	for _, c := range []string{"empty", "garbage"} {
		var data []byte
		if c == "garbage" {
			data = []byte("garbage garbage garbage")
		}
		if _, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: Decode err = %v, want ErrUnsupported", c, err)
		}
	}
}

func TestCorruptInputs(t *testing.T) {
	for _, fx := range fixtures() {
		// Truncated after the header and with the body scrambled.
		for _, mut := range []func([]byte) []byte{
			func(b []byte) []byte { return b[:min(len(b), 40)] },
			func(b []byte) []byte {
				c := bytes.Clone(b)
				for i := 32; i < len(c); i += 3 {
					c[i] ^= 0x5A
				}
				return c
			},
		} {
			data := mut(fx.data)
			// Must not panic; an error or an image are both acceptable.
			_, _, _ = Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{})
			_, _, _ = DecodeFit(context.Background(), bytes.NewReader(data), int64(len(data)), 8, Options{})
		}
	}
}

// hugeJPEG returns a tiny JPEG whose SOF claims w x h.
func hugeJPEG(t *testing.T, w, h int) []byte {
	b := testimg.BareJPEG()
	for i := 2; i+9 < len(b); i++ {
		if b[i] == 0xFF && (b[i+1] == 0xC0 || b[i+1] == 0xC2) {
			binary.BigEndian.PutUint16(b[i+5:], uint16(h))
			binary.BigEndian.PutUint16(b[i+7:], uint16(w))
			return b
		}
	}
	t.Fatal("no SOF marker")
	return nil
}

func TestDecodeRefusesHugeBeforeAllocating(t *testing.T) {
	data := hugeJPEG(t, 20000, 10000) // 200 megapixels
	r := bytes.NewReader(data)
	w, h, _, err := Config(r, int64(len(data)))
	if err != nil || w != 20000 || h != 10000 {
		t.Fatalf("Config = %d %d %v", w, h, err)
	}
	// Warm up so one-time initialisation does not count.
	_, _, _ = Decode(context.Background(), r, int64(len(data)), Options{})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, err = Decode(context.Background(), r, int64(len(data)), Options{})
	_, _, err2 := DecodeFit(context.Background(), r, int64(len(data)), 256, Options{})
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrTooLarge) || !errors.Is(err2, ErrTooLarge) {
		t.Fatalf("err = %v / %v, want ErrTooLarge", err, err2)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew >= 1<<20 {
		t.Fatalf("allocated %d bytes refusing a huge image", grew)
	}
	// An explicit budget is honoured too.
	small := testimg.PNG(20, 20, 0)
	if _, _, err := Decode(context.Background(), bytes.NewReader(small), int64(len(small)), Options{MaxPixels: 399}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("budget 399 for 400 px: err = %v", err)
	}
	if _, _, err := Decode(context.Background(), bytes.NewReader(small), int64(len(small)), Options{MaxPixels: 400}); err != nil {
		t.Fatalf("budget 400 for 400 px: err = %v", err)
	}
}

func TestDecodeContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := testimg.PNG(4, 4, 0)
	if _, _, err := Decode(ctx, bytes.NewReader(data), int64(len(data)), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode err = %v", err)
	}
	if _, _, err := DecodeFit(ctx, bytes.NewReader(data), int64(len(data)), 2, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeFit err = %v", err)
	}
}

func TestOrient(t *testing.T) {
	const w, h = 5, 3
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	src.Set(0, 0, red)     // top-left corner
	src.Set(w-1, 0, green) // top-right corner, to tell flips from rotations
	type pos struct{ x, y int }
	cases := []struct {
		o          int
		dw, dh     int
		red, green pos
	}{
		{1, w, h, pos{0, 0}, pos{w - 1, 0}},
		{2, w, h, pos{w - 1, 0}, pos{0, 0}},
		{3, w, h, pos{w - 1, h - 1}, pos{0, h - 1}},
		{4, w, h, pos{0, h - 1}, pos{w - 1, h - 1}},
		{5, h, w, pos{0, 0}, pos{0, w - 1}},
		{6, h, w, pos{h - 1, 0}, pos{h - 1, w - 1}},
		{7, h, w, pos{h - 1, w - 1}, pos{h - 1, 0}},
		{8, h, w, pos{0, w - 1}, pos{0, 0}},
	}
	for _, c := range cases {
		out := Orient(src, c.o)
		b := out.Bounds()
		if b.Dx() != c.dw || b.Dy() != c.dh {
			t.Errorf("orientation %d: size %dx%d, want %dx%d", c.o, b.Dx(), b.Dy(), c.dw, c.dh)
			continue
		}
		var reds, greens int
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				switch color.RGBAModel.Convert(out.At(x, y)) {
				case red:
					reds++
					if (pos{x - b.Min.X, y - b.Min.Y}) != c.red {
						t.Errorf("orientation %d: red at %d,%d, want %v", c.o, x, y, c.red)
					}
				case green:
					greens++
					if (pos{x - b.Min.X, y - b.Min.Y}) != c.green {
						t.Errorf("orientation %d: green at %d,%d, want %v", c.o, x, y, c.green)
					}
				}
			}
		}
		if reds != 1 || greens != 1 {
			t.Errorf("orientation %d: %d red, %d green pixels", c.o, reds, greens)
		}
	}
	if Orient(src, 0) != image.Image(src) || Orient(src, 9) != image.Image(src) {
		t.Error("invalid orientation should return the input")
	}
	// Non-RGBA input with a non-zero origin.
	ycc := image.NewYCbCr(image.Rect(10, 20, 14, 22), image.YCbCrSubsampleRatio444)
	if b := Orient(ycc, 6).Bounds(); b != image.Rect(0, 0, 2, 4) {
		t.Errorf("YCbCr orient 6 bounds %v", b)
	}
}

func TestFit(t *testing.T) {
	cases := []struct{ w, h, limit int }{
		{1, 1, 256},
		{1, 1, 1},
		{100, 50, 256},
		{256, 100, 256},
		{1000, 750, 256},
		{750, 1000, 256},
		{4000, 3, 256},
		{3, 4000, 256},
		{10000, 1, 100},
		{1601, 1600, 1600},
		{3001, 2003, 1000},
	}
	for _, c := range cases {
		src := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
		out := Fit(src, c.limit)
		b := out.Bounds()
		if c.w <= c.limit && c.h <= c.limit {
			if out != image.Image(src) {
				t.Errorf("%dx%d->%d: expected the same image back", c.w, c.h, c.limit)
			}
			continue
		}
		if max(b.Dx(), b.Dy()) != c.limit {
			t.Errorf("%dx%d->%d: got %v", c.w, c.h, c.limit, b)
		}
		if b.Dx() > c.w || b.Dy() > c.h {
			t.Errorf("%dx%d->%d: upscaled to %v", c.w, c.h, c.limit, b)
		}
		// Aspect within 1 px on the shorter side.
		if c.w >= c.h {
			want := float64(c.h) * float64(c.limit) / float64(c.w)
			if math.Abs(float64(b.Dy())-math.Max(1, want)) > 1 {
				t.Errorf("%dx%d->%d: height %d, want ~%.1f", c.w, c.h, c.limit, b.Dy(), want)
			}
		} else {
			want := float64(c.w) * float64(c.limit) / float64(c.h)
			if math.Abs(float64(b.Dx())-math.Max(1, want)) > 1 {
				t.Errorf("%dx%d->%d: width %d, want ~%.1f", c.w, c.h, c.limit, b.Dx(), want)
			}
		}
	}
}

func TestFitPreservesColour(t *testing.T) {
	// Every source type the box downsampler handles gives the same flat
	// colour back.
	want := color.RGBA{200, 100, 50, 255}
	rect := image.Rect(3, 7, 3+900, 7+500)
	rgba := image.NewRGBA(rect)
	nrgba := image.NewNRGBA(rect)
	ycc := image.NewYCbCr(rect, image.YCbCrSubsampleRatio420)
	cmyk := image.NewCMYK(rect)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			rgba.Set(x, y, want)
			nrgba.Set(x, y, want)
			cmyk.Set(x, y, want)
		}
	}
	yy, cb, cr := color.RGBToYCbCr(want.R, want.G, want.B)
	for i := range ycc.Y {
		ycc.Y[i] = yy
	}
	for i := range ycc.Cb {
		ycc.Cb[i], ycc.Cr[i] = cb, cr
	}
	for name, img := range map[string]image.Image{"rgba": rgba, "nrgba": nrgba, "ycbcr": ycc, "cmyk": cmyk} {
		out := Fit(img, 100)
		b := out.Bounds()
		if b.Dx() != 100 || b.Dy() != 56 {
			t.Errorf("%s: bounds %v", name, b)
		}
		got := color.RGBAModel.Convert(out.At(b.Min.X+50, b.Min.Y+28)).(color.RGBA)
		if d := max(absDiff(got.R, want.R), absDiff(got.G, want.G), absDiff(got.B, want.B)); d > 4 {
			t.Errorf("%s: colour %v, want %v", name, got, want)
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestEncodeJPEGRoundTrip(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			src.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 8), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := EncodeJPEG(&buf, src, 90); err != nil {
		t.Fatal(err)
	}
	img, f := decodeBytes(t, buf.Bytes())
	if f != FormatJPEG || img.Bounds() != src.Bounds() {
		t.Fatalf("round trip: %q %v", f, img.Bounds())
	}
	got := color.RGBAModel.Convert(img.At(20, 15)).(color.RGBA)
	if absDiff(got.R, 120) > 10 || absDiff(got.G, 120) > 10 || absDiff(got.B, 128) > 10 {
		t.Fatalf("pixel %v", got)
	}
}

// scene draws a synthetic photo-like image: a gradient sky, a dark disc
// and a bright bar, at any resolution.
func scene(w, h int, variant int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			var v float64
			switch variant {
			case 0:
				v = 60 + 120*fy
				if dx, dy := fx-0.35, fy-0.45; dx*dx+dy*dy < 0.04 {
					v = 20
				}
				if fx > 0.65 && fx < 0.8 && fy > 0.1 && fy < 0.9 {
					v = 240
				}
			default:
				v = 128 + 100*math.Sin(fx*9)*math.Cos(fy*5)
				if fy > 0.7 {
					v = 255 - 200*fx
				}
			}
			c := uint8(math.Max(0, math.Min(255, v)))
			img.Pix[y*img.Stride+4*x+0] = c
			img.Pix[y*img.Stride+4*x+1] = c
			img.Pix[y*img.Stride+4*x+2] = uint8(255 - int(c)/2)
			img.Pix[y*img.Stride+4*x+3] = 255
		}
	}
	return img
}

func TestPHash(t *testing.T) {
	a := scene(400, 300, 0)
	half := Fit(a, 200)
	other := scene(400, 300, 1)
	ha, hh, ho := PHash(a), PHash(half), PHash(other)
	if d := Distance(ha, hh); d > 6 {
		t.Errorf("image vs 50%% copy: distance %d (hashes %016x %016x)", d, ha, hh)
	}
	if d := Distance(ha, ho); d <= 20 {
		t.Errorf("different images: distance %d (hashes %016x %016x)", d, ha, ho)
	}
	if ha&1 != 0 {
		t.Error("DC bit must be 0")
	}
	if PHash(image.NewRGBA(image.Rect(0, 0, 1, 1))) != 0 && PHash(image.NewRGBA(image.Rectangle{})) != 0 {
		t.Error("degenerate images")
	}
	if Distance(0, ^uint64(0)) != 64 || Distance(5, 5) != 0 {
		t.Error("Distance")
	}
}

// TestJpegliScaleTarget documents what jpegli's ScaleTarget yields: the
// smallest DCT scale N/8 whose output covers the target in both dimensions.
func TestJpegliScaleTarget(t *testing.T) {
	data := testimg.BigJPEG(800, 600)
	cases := []struct{ tw, th, ww, wh int }{
		{100, 75, 100, 75},   // 1/8
		{101, 75, 200, 150},  // 2/8
		{200, 150, 200, 150}, // 2/8
		{256, 192, 300, 225}, // 3/8
		{500, 100, 500, 375}, // 5/8: width decides
		{790, 10, 0, 0},      // no N<8 qualifies: jpegli v0.4.2 fails (DecodeFit never asks for this)
	}
	for _, c := range cases {
		img, err := jpegliScaled(bytes.NewReader(data), c.tw, c.th)
		if c.ww == 0 {
			if err == nil {
				t.Errorf("target %dx%d: expected jpegli to fail, got %v", c.tw, c.th, img.Bounds())
			}
			continue
		}
		if err != nil {
			t.Errorf("target %dx%d: %v", c.tw, c.th, err)
			continue
		}
		if b := img.Bounds(); b.Dx() != c.ww || b.Dy() != c.wh {
			t.Errorf("target %dx%d: got %dx%d, want %dx%d", c.tw, c.th, b.Dx(), b.Dy(), c.ww, c.wh)
		}
	}
}

func TestDecodeFit(t *testing.T) {
	ctx := context.Background()
	// Small sources and non-JPEGs go through Decode unchanged.
	small := testimg.PNG(30, 20, 1)
	img, f, err := DecodeFit(ctx, bytes.NewReader(small), int64(len(small)), 10, Options{})
	if err != nil || f != FormatPNG || img.Bounds().Dx() != 30 {
		t.Fatalf("png: %v %q %v", img.Bounds(), f, err)
	}
	j := testimg.JPEG(testimg.Options{Width: 40, Height: 20, NoExif: true})
	if img, _, err := DecodeFit(ctx, bytes.NewReader(j), int64(len(j)), 20, Options{}); err != nil || img.Bounds().Dx() != 40 {
		t.Fatalf("jpeg at 2x: %v %v", img, err)
	}
	if img, _, err := DecodeFit(ctx, bytes.NewReader(j), int64(len(j)), 8, Options{}); err != nil || img.Bounds().Dx() < 8 || img.Bounds().Dx() >= 40 {
		t.Fatalf("jpeg scaled: %v %v", img.Bounds(), err)
	}

	if testing.Short() {
		t.Skip("24 MP comparison skipped in -short mode")
	}
	big := testimg.BigJPEG(6000, 4000)
	r := bytes.NewReader(big)
	// Warm up jpegli (wazero compilation happens on first use).
	if _, _, err := DecodeFit(ctx, r, int64(len(big)), 1600, Options{}); err != nil {
		t.Fatal(err)
	}
	timed := func(fitMax int) (image.Image, time.Duration) {
		start := time.Now()
		var img image.Image
		var err error
		if fitMax == 0 {
			img, _, err = Decode(ctx, r, int64(len(big)), Options{})
		} else {
			img, _, err = DecodeFit(ctx, r, int64(len(big)), fitMax, Options{})
		}
		if err != nil {
			t.Fatal(err)
		}
		return img, time.Since(start)
	}
	full, tFull := timed(0)
	if full.Bounds().Dx() != 6000 {
		t.Fatalf("Decode bounds %v", full.Bounds())
	}
	for _, fitMax := range []int{1600, 256} {
		img, d := timed(fitMax)
		b := img.Bounds()
		if max(b.Dx(), b.Dy()) < fitMax || b.Dx() >= 6000 {
			t.Fatalf("DecodeFit(%d) bounds %v", fitMax, b)
		}
		t.Logf("24 MP: DecodeFit(%d) %v -> %v; full Decode %v", fitMax, d, b.Size(), tFull)
		// At 1/8 scale (thumbnail size) jpegli must beat a full decode. At
		// 3/8 (fitMax 1600) the WebAssembly entropy decoder is about as
		// fast as image/jpeg's full decode, so only the size is checked.
		if fitMax == 256 && d >= tFull {
			t.Errorf("DecodeFit(%d) (%v) not faster than Decode (%v)", fitMax, d, tFull)
		}
	}
}

func TestConcurrentDecode(t *testing.T) {
	fx := fixtures()
	big := testimg.JPEG(testimg.Options{Width: 200, Height: 100, NoExif: true})
	var wg sync.WaitGroup
	errs := make(chan error, 4*len(fx))
	for i := 0; i < 4; i++ {
		for _, f := range fx {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, err := Decode(context.Background(), bytes.NewReader(f.data), int64(len(f.data)), Options{}); err != nil {
					errs <- fmt.Errorf("%s: %w", f.name, err)
				}
				if _, _, err := DecodeFit(context.Background(), bytes.NewReader(big), int64(len(big)), 40, Options{}); err != nil {
					errs <- fmt.Errorf("DecodeFit: %w", err)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
