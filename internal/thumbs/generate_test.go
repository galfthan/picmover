package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/imaging"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
)

func newGen(t *testing.T) *Generator {
	t.Helper()
	s, _ := openTestStore(t)
	return &Generator{Store: s, FFmpeg: filepath.Join(t.TempDir(), "no-ffmpeg")}
}

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// smooth returns a low-frequency test scene that survives scaling, so
// perceptual hashes of scaled copies stay close.
func smooth(w, h int, seed float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			v := 128 + 60*math.Sin(2*math.Pi*(fx*1.5+seed)) + 60*math.Cos(2*math.Pi*(fy*(1+seed)+fx*seed))
			v = max(0, min(255, v))
			img.SetRGBA(x, y, color.RGBA{uint8(v), uint8(255 - v), uint8(fx * 255), 255})
		}
	}
	return img
}

func encPNG(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func encJPEG(img image.Image) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// stored decodes the stored preview and thumbnail of hash.
func stored(t *testing.T, s *Store, hash uint64) (preview, thumb image.Image) {
	t.Helper()
	pb, err := os.ReadFile(s.PreviewPath(hash))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview, err = jpeg.Decode(bytes.NewReader(pb)); err != nil {
		t.Fatalf("preview decode: %v", err)
	}
	tb, _, _, ok, err := s.Thumb(context.Background(), hash)
	if err != nil || !ok {
		t.Fatalf("thumb: ok=%v err=%v", ok, err)
	}
	if thumb, err = jpeg.Decode(bytes.NewReader(tb)); err != nil {
		t.Fatalf("thumb decode: %v", err)
	}
	return preview, thumb
}

func size(img image.Image) string { return fmt.Sprintf("%dx%d", img.Bounds().Dx(), img.Bounds().Dy()) }

func TestGenerateImageFormats(t *testing.T) {
	g := newGen(t)
	avifData := testimg.AVIF(60, 40, 3)
	cases := []struct {
		name         string
		data         []byte
		orientation  int
		wantW, wantH int // oriented full size = preview = thumb (all small)
	}{
		{"a.jpg", testimg.JPEG(testimg.Options{Width: 64, Height: 32, Orientation: 6}), 6, 32, 64},
		{"b.jpg", testimg.JPEG(testimg.Options{Width: 64, Height: 32}), 1, 64, 32},
		{"c.png", testimg.PNG(60, 40, 1), 0, 60, 40},
		{"d.webp", testimg.WebP(60, 40, 2), 0, 60, 40},
		{"e.avif", avifData, 0, 60, 40},
		{"e8.avif", avifData, 8, 40, 60}, // gen2brain/avif does not auto-rotate: we orient
		{"f.gif", testimg.GIF(60, 40, 4), 0, 60, 40},
		{"g.bmp", testimg.BMP(60, 40, 5), 0, 60, 40},
		{"h.tif", testimg.TIFFImage(60, 40, 6), 0, 60, 40},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hash := uint64(0x1000 + i)
			res := g.Generate(context.Background(), Source{Path: write(t, c.name, c.data), Kind: metadata.KindImage, Hash: hash, Orientation: c.orientation}, false)
			if res.State != StateOK {
				t.Fatalf("result %+v", res)
			}
			if res.Width != c.wantW || res.Height != c.wantH || res.ThumbW != c.wantW || res.ThumbH != c.wantH {
				t.Errorf("result %+v, want %dx%d", res, c.wantW, c.wantH)
			}
			if res.PHash == 0 {
				t.Error("PHash = 0")
			}
			p, th := stored(t, g.Store, hash)
			want := fmt.Sprintf("%dx%d", c.wantW, c.wantH)
			if size(p) != want || size(th) != want {
				t.Errorf("preview %s thumb %s, want %s", size(p), size(th), want)
			}
		})
	}
}

func TestGenerateScalesAndPHash(t *testing.T) {
	g := newGen(t)
	src := smooth(2400, 1200, 0.3)
	res := g.Generate(context.Background(), Source{Path: write(t, "big.png", encPNG(src)), Kind: metadata.KindImage, Hash: 7}, false)
	if res.State != StateOK || res.Width != 2400 || res.Height != 1200 || res.ThumbW != 256 || res.ThumbH != 128 {
		t.Fatalf("result %+v", res)
	}
	p, th := stored(t, g.Store, 7)
	if size(p) != "1600x800" || size(th) != "256x128" {
		t.Errorf("preview %s thumb %s", size(p), size(th))
	}
	want := imaging.PHash(src)
	if d := imaging.Distance(res.PHash, want); d > 6 {
		t.Errorf("pHash of preview differs from source by %d bits", d)
	}
	if d := imaging.Distance(imaging.PHash(p), want); d > 6 {
		t.Errorf("pHash of stored preview differs from source by %d bits", d)
	}
	if d := imaging.Distance(res.PHash, imaging.PHash(smooth(2400, 1200, 0.71))); d < 10 {
		t.Errorf("pHash of a different scene only %d bits away", d)
	}

	// A large JPEG goes through jpegli's reduced-scale decode and comes out
	// the same.
	res2 := g.Generate(context.Background(), Source{Path: write(t, "big.jpg", encJPEG(src)), Kind: metadata.KindImage, Hash: 8, Orientation: 8}, false)
	if res2.State != StateOK || res2.Width != 1200 || res2.Height != 2400 || res2.ThumbW != 128 || res2.ThumbH != 256 {
		t.Fatalf("jpeg result %+v", res2)
	}
	if p, _ := stored(t, g.Store, 8); size(p) != "800x1600" {
		t.Errorf("oriented preview %s", size(p))
	}
}

func TestGenerateOrientationPixels(t *testing.T) {
	// Left half red, right half blue; orientation 6 (rotate 90 CW) puts red
	// on top. The large source is scaled before it is oriented.
	g := newGen(t)
	for i, sz := range []struct {
		w, h int
		want string
	}{{80, 40, "40x80"}, {3400, 1700, "800x1600"}} {
		img := image.NewRGBA(image.Rect(0, 0, sz.w, sz.h))
		for y := 0; y < sz.h; y++ {
			for x := 0; x < sz.w; x++ {
				c := color.RGBA{220, 20, 20, 255}
				if x >= sz.w/2 {
					c = color.RGBA{20, 20, 220, 255}
				}
				img.SetRGBA(x, y, c)
			}
		}
		hash := uint64(9 + 100*i)
		res := g.Generate(context.Background(), Source{Path: write(t, "o.jpg", encJPEG(img)), Kind: metadata.KindImage, Hash: hash, Orientation: 6}, false)
		if res.State != StateOK || res.Width != sz.h || res.Height != sz.w {
			t.Fatalf("%+v", res)
		}
		p, _ := stored(t, g.Store, hash)
		if size(p) != sz.want {
			t.Fatalf("preview %s, want portrait %s", size(p), sz.want)
		}
		pb := p.Bounds()
		if r, _, b, _ := p.At(pb.Dx()/2, pb.Dy()/8).RGBA(); r < b {
			t.Errorf("%dx%d: top of the oriented preview is not red", sz.w, sz.h)
		}
		if r, _, b, _ := p.At(pb.Dx()/2, pb.Dy()*7/8).RGBA(); r > b {
			t.Errorf("%dx%d: bottom of the oriented preview is not blue", sz.w, sz.h)
		}
	}
}

func TestGenerateRawPreview(t *testing.T) {
	g := newGen(t)
	prev := encJPEG(smooth(160, 120, 0.2))
	raw := testimg.TIFFWithBlobs(nil, [][]byte{prev},
		testimg.IFD{Entries: []testimg.Entry{
			testimg.Long(0x0100, 6000), testimg.Long(0x0101, 4000),
			testimg.ASCII(0x010F, "Canon"),
		}, Next: 2},
		testimg.IFD{Entries: testimg.JPEGInterchange(1)},
	)
	res := g.Generate(context.Background(), Source{Path: write(t, "x.cr2", raw), Kind: metadata.KindRaw, Hash: 10, Orientation: 6}, false)
	if res.State != StateOK {
		t.Fatalf("%+v", res)
	}
	if res.Width != 4000 || res.Height != 6000 || res.ThumbW != 120 || res.ThumbH != 160 {
		t.Errorf("result %+v", res)
	}
	if p, _ := stored(t, g.Store, 10); size(p) != "120x160" {
		t.Errorf("preview %s, want oriented 120x160", size(p))
	}

	// No previews at all.
	bare := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{testimg.Long(0x0100, 6000), testimg.Long(0x0101, 4000)}})
	res = g.Generate(context.Background(), Source{Path: write(t, "y.arw", bare), Kind: metadata.KindRaw, Hash: 11}, false)
	if res.State != StateUnsupported {
		t.Errorf("no-preview raw: %+v", res)
	}

	// A preview whose bytes are not a decodable JPEG.
	broken := append([]byte{0xFF, 0xD8, 0xFF}, bytes.Repeat([]byte{0x42}, 200)...)
	rawBad := testimg.TIFFWithBlobs(nil, [][]byte{broken}, testimg.IFD{Entries: testimg.JPEGInterchange(1)})
	res = g.Generate(context.Background(), Source{Path: write(t, "z.nef", rawBad), Kind: metadata.KindRaw, Hash: 12}, false)
	if res.State != StateUnsupported || !strings.Contains(res.Message, "1 embedded") {
		t.Errorf("broken-preview raw: %+v", res)
	}
	if th, pv, _ := g.Store.Has(context.Background(), 12); th || pv {
		t.Error("unsupported raw left renditions")
	}
}

// hugePNG is a PNG header claiming w x h pixels, with a valid IHDR CRC and
// no image data.
func hugePNG(w, h uint32) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	ihdr := binary.BigEndian.AppendUint32([]byte("IHDR"), w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 13)
	b = append(b, ihdr...)
	return binary.BigEndian.AppendUint32(b, crc32IEEE(ihdr))
}

func crc32IEEE(b []byte) uint32 {
	crc := ^uint32(0)
	for _, v := range b {
		crc ^= uint32(v)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xEDB88320
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

func TestGenerateTooLargeAndGarbage(t *testing.T) {
	g := newGen(t)
	ctx := context.Background()
	p := write(t, "huge.png", hugePNG(20000, 10000))
	_ = g.Generate(ctx, Source{Path: p, Kind: metadata.KindImage, Hash: 20}, true) // warm up
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res := g.Generate(ctx, Source{Path: p, Kind: metadata.KindImage, Hash: 20}, true)
	runtime.ReadMemStats(&after)
	if res.State != StateUnsupported || !strings.Contains(res.Message, "200000000") {
		t.Errorf("200 MP: %+v", res)
	}
	if a := after.TotalAlloc - before.TotalAlloc; a > 4<<20 {
		t.Errorf("200 MP header allocated %d bytes", a)
	}

	for name, data := range map[string][]byte{
		"garbage.jpg":   []byte("definitely not an image, just some bytes of text"),
		"truncated.jpg": testimg.JPEG(testimg.Options{Width: 64, Height: 64, NoExif: true})[:200],
		"empty.png":     {},
	} {
		res := g.Generate(ctx, Source{Path: write(t, name, data), Kind: metadata.KindImage, Hash: 21}, false)
		if res.State != StateError || res.Message == "" {
			t.Errorf("%s: %+v", name, res)
		}
	}
	res = g.Generate(ctx, Source{Path: filepath.Join(t.TempDir(), "missing.jpg"), Kind: metadata.KindImage, Hash: 22}, false)
	if res.State != StateError {
		t.Errorf("missing file: %+v", res)
	}
	res = g.Generate(ctx, Source{Path: write(t, "x.txt", []byte("x")), Kind: "document", Hash: 23}, false)
	if res.State != StateUnsupported {
		t.Errorf("unknown kind: %+v", res)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	res = g.Generate(cctx, Source{Path: write(t, "c.png", testimg.PNG(8, 8, 1)), Kind: metadata.KindImage, Hash: 24}, false)
	if res.State != StateError || !strings.Contains(res.Message, "canceled") {
		t.Errorf("cancelled: %+v", res)
	}
}

func TestGenerateHEICSample(t *testing.T) {
	p := filepath.Join("..", "testimg", "testdata", "sample.heic")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("HEIC sample %s not present (user-supplied fixture): %v", p, err)
	}
	g := newGen(t)
	// Orientation 6 must be ignored: libheif already applied irot/imir.
	res := g.Generate(context.Background(), Source{Path: p, Kind: metadata.KindImage, Hash: 30, Orientation: 6}, false)
	if res.State != StateOK || res.Width == 0 {
		t.Fatalf("%+v", res)
	}
	if pv, _ := stored(t, g.Store, 30); (pv.Bounds().Dx() >= pv.Bounds().Dy()) != (res.Width >= res.Height) {
		t.Errorf("preview %s does not match full size %dx%d", size(pv), res.Width, res.Height)
	}
}

func TestGenerateForceAndShortCircuit(t *testing.T) {
	g := newGen(t)
	ctx := context.Background()
	src := Source{Path: write(t, "a.png", encPNG(smooth(50, 30, 0.4))), Kind: metadata.KindImage, Hash: 40}
	first := g.Generate(ctx, src, false)
	if first.State != StateOK || first.PHash == 0 {
		t.Fatalf("%+v", first)
	}
	stored1, err := os.ReadFile(g.Store.PreviewPath(40))
	if err != nil {
		t.Fatal(err)
	}
	// A cache hit decodes the stored preview: same pHash, no rewrite, no
	// full size.
	again := g.Generate(ctx, src, false)
	if again.State != StateOK || again.PHash != first.PHash || again.ThumbW != 50 || again.ThumbH != 30 || again.Width != 0 {
		t.Errorf("cache hit %+v, first %+v", again, first)
	}
	if b, _ := os.ReadFile(g.Store.PreviewPath(40)); !bytes.Equal(b, stored1) {
		t.Error("cache hit rewrote the preview")
	}
	// A preview that does not decode is regenerated without force.
	if err := os.WriteFile(g.Store.PreviewPath(40), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	repaired := g.Generate(ctx, src, false)
	if repaired.State != StateOK || repaired.PHash != first.PHash || repaired.Width != 50 {
		t.Errorf("repaired %+v", repaired)
	}
	stored(t, g.Store, 40) // decodes again
	forced := g.Generate(ctx, src, true)
	if forced.State != StateOK || forced.PHash != first.PHash {
		t.Errorf("forced %+v", forced)
	}
	// A missing preview makes Generate redo the work without force.
	os.Remove(g.Store.PreviewPath(40))
	if res := g.Generate(ctx, src, false); res.State != StateOK || res.PHash != first.PHash || res.Width != 50 {
		t.Errorf("after preview loss %+v", res)
	}
	// A cancelled context on a cache hit is an error with ctx.Err().
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if res := g.Generate(cctx, src, false); res.State != StateError || res.Message != context.Canceled.Error() {
		t.Errorf("cancelled cache hit %+v", res)
	}
}

func TestGenerateSourceChanged(t *testing.T) {
	g := newGen(t)
	ctx := context.Background()
	data := testimg.PNG(40, 20, 1)
	p := write(t, "s.png", data)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	ok := Source{Path: p, Kind: metadata.KindImage, Hash: 45, Size: st.Size(), MTime: st.ModTime().Add(time.Second)}
	if res := g.Generate(ctx, ok, false); res.State != StateOK {
		t.Fatalf("unchanged file: %+v", res)
	}
	for name, src := range map[string]Source{
		"size":  {Path: p, Kind: metadata.KindImage, Hash: 46, Size: st.Size() + 1},
		"mtime": {Path: p, Kind: metadata.KindImage, Hash: 46, MTime: st.ModTime().Add(-3 * time.Second)},
	} {
		res := g.Generate(ctx, src, false)
		if res.State != StateError || res.Message != "file changed since import; run verify or rebuild" {
			t.Errorf("%s: %+v", name, res)
		}
		if th, pv, _ := g.Store.Has(ctx, 46); th || pv {
			t.Errorf("%s: renditions stored for a changed file", name)
		}
	}
	// Also refused when the old renditions are cached.
	if res := g.Generate(ctx, Source{Path: p, Kind: metadata.KindImage, Hash: 45, Size: 1}, false); res.State != StateError {
		t.Errorf("changed file with cached renditions: %+v", res)
	}
}

func TestGenerateHEIFSequenceUnsupported(t *testing.T) {
	// A HEIF with a moov box is refused before the HEIF decoder runs.
	ftyp := testimg.Box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))
	data := append(ftyp, testimg.Box("moov", testimg.Box("trak"))...)
	g := newGen(t)
	res := g.Generate(context.Background(), Source{Path: write(t, "seq.heic", data), Kind: metadata.KindImage, Hash: 47}, false)
	if res.State != StateUnsupported || !strings.Contains(res.Message, "HEIF image sequence") {
		t.Errorf("%+v", res)
	}
}

func TestGeneratorDefaults(t *testing.T) {
	g := newGen(t)
	o := g.opts(nil)
	if o.Budget == nil || o.HEIFSlots == nil || cap(o.HEIFSlots) != DefaultHEIFSlots {
		t.Fatalf("defaults %+v", o)
	}
	if o2 := g.opts(nil); o2.Budget != o.Budget || o2.HEIFSlots != o.HEIFSlots {
		t.Error("defaults not shared")
	}
	b := imaging.NewMemBudget(1)
	slots := make(chan struct{}, 1)
	g2 := &Generator{Store: g.Store, Budget: b, HEIFSlots: slots}
	if o := g2.opts(nil); o.Budget != b || o.HEIFSlots != slots {
		t.Error("explicit limits not used")
	}
	// A 1-byte budget still lets one decode through at a time.
	res := g2.Generate(context.Background(), Source{Path: write(t, "b.png", testimg.PNG(30, 20, 2)), Kind: metadata.KindImage, Hash: 48}, false)
	if res.State != StateOK || b.Held() != 0 {
		t.Errorf("%+v, held %d", res, b.Held())
	}
}

func TestGenerateConcurrent(t *testing.T) {
	g := newGen(t)
	var wg sync.WaitGroup
	results := make([]Result, 8)
	paths := make([]string, 8)
	for i := range paths {
		paths[i] = write(t, fmt.Sprintf("c%d.png", i), encPNG(smooth(300+i*10, 200, float64(i)/10)))
	}
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = g.Generate(context.Background(), Source{Path: paths[i], Kind: metadata.KindImage, Hash: uint64(i+1) << 56}, false)
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.State != StateOK || r.Width != 300+i*10 {
			t.Errorf("%d: %+v", i, r)
		}
	}
	if n, _, err := g.Store.Stats(context.Background()); n != 8 || err != nil {
		t.Errorf("Stats = %d, %v", n, err)
	}
}

// fakeFFmpeg writes a shell script that records its arguments and prints
// frame (or fails with stderr when frame is nil).
func fakeFFmpeg(t *testing.T, frame []byte) (script, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\n", argsFile)
	if frame != nil {
		fp := filepath.Join(dir, "frame.jpg")
		if err := os.WriteFile(fp, frame, 0o644); err != nil {
			t.Fatal(err)
		}
		body += fmt.Sprintf("cat '%s'\n", fp)
	} else {
		body += "echo 'first line' >&2\necho 'file:/x: Invalid data found when processing input' >&2\nexit 1\n"
	}
	script = filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, argsFile
}

func TestGenerateVideoFakeFFmpeg(t *testing.T) {
	ff, args := fakeFFmpeg(t, encJPEG(smooth(320, 240, 0.4)))
	g := newGen(t)
	g.FFmpeg = ff
	video := write(t, "v.mp4", []byte("not really a video"))
	res := g.Generate(context.Background(), Source{Path: video, Kind: metadata.KindVideo, Hash: 50, DurationMS: 5000, Orientation: 6}, false)
	if res.State != StateOK || res.ThumbW != 256 || res.ThumbH != 192 || res.Width != 0 {
		t.Fatalf("%+v", res)
	}
	if p, _ := stored(t, g.Store, 50); size(p) != "320x240" {
		t.Errorf("preview %s (video frames are not re-oriented)", size(p))
	}
	b, _ := os.ReadFile(args)
	got := strings.Fields(string(b))
	want := []string{"-hide_banner", "-loglevel", "error", "-ss", "0.500", "-i", "file:" + video,
		"-frames:v", "1", "-vf", "scale='min(1600,iw)':-2", "-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "3", "-"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ffmpeg args\n got %q\nwant %q", got, want)
	}

	res = g.Generate(context.Background(), Source{Path: video, Kind: metadata.KindVideo, Hash: 51, DurationMS: 60000}, false)
	if b, _ := os.ReadFile(args); res.State != StateOK || !strings.Contains(string(b), "\n1.000\n") {
		t.Errorf("long video: %+v args %q", res, b)
	}

	failing, _ := fakeFFmpeg(t, nil)
	g.FFmpeg = failing
	res = g.Generate(context.Background(), Source{Path: video, Kind: metadata.KindVideo, Hash: 52}, false)
	if res.State != StateError || res.Message != "ffmpeg: file:/x: Invalid data found when processing input" {
		t.Errorf("failing ffmpeg: %+v", res)
	}
}

func TestGenerateVideoNoFFmpeg(t *testing.T) {
	g := newGen(t) // FFmpeg points at a missing file
	video := write(t, "v.mp4", []byte("x"))
	res := g.Generate(context.Background(), Source{Path: video, Kind: metadata.KindVideo, Hash: 60}, false)
	if res.State != StateUnsupported || !strings.Contains(res.Message, "ffmpeg not found") {
		t.Errorf("explicit missing ffmpeg: %+v", res)
	}

	t.Setenv("PATH", t.TempDir())
	g.FFmpeg = ""
	if _, err := metadata.FindTool("ffmpeg"); err == nil {
		t.Skip("ffmpeg found next to the test binary or cached; cannot test the lookup failure")
	}
	res = g.Generate(context.Background(), Source{Path: video, Kind: metadata.KindVideo, Hash: 60}, false)
	if res.State != StateUnsupported || res.Message != "ffmpeg not found" {
		t.Errorf("no ffmpeg on PATH: %+v", res)
	}
}

func TestGenerateVideoRealFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mp4")
	out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=10:duration=2",
		"-pix_fmt", "yuv420p", video).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot synthesise a test video: %v %s", err, out)
	}
	g := newGen(t)
	g.FFmpeg = ff
	res := g.Generate(context.Background(), Source{Path: video, Kind: metadata.KindVideo, Hash: 70, DurationMS: 2000}, false)
	if res.State != StateOK || res.ThumbW != 256 || res.ThumbH != 144 {
		t.Errorf("%+v", res)
	}
}
