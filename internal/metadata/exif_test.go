package metadata

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"picmover/internal/testimg"
)

func TestExtractJPEGExif(t *testing.T) {
	when := time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC)
	data := testimg.JPEG(testimg.Options{
		Width: 24, Height: 16,
		Make: "Canon", Model: "Canon EOS R5", Lens: "RF24-105mm F4 L IS USM",
		DateTime: when, Orientation: 6,
		GPS: true, Lat: 60.1699, Lon: -24.9384, Altitude: 12.5,
	})
	p, _ := writeFile(t, "a.jpg", data)
	info, warns, err := Default{DefaultZone: time.FixedZone("T", 3*3600)}.Extract(context.Background(), p, KindImage)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if !info.TakenAt.Equal(when) || info.TakenAt.Location() != time.UTC {
		t.Errorf("TakenAt = %v, want wall clock %v in UTC placeholder", info.TakenAt, when)
	}
	if info.OffsetKnown {
		t.Error("OffsetKnown should be false for zoneless EXIF date")
	}
	if info.Source != "exif" {
		t.Errorf("Source = %q", info.Source)
	}
	if info.CameraMake != "Canon" || info.CameraModel != "Canon EOS R5" || info.Lens != "RF24-105mm F4 L IS USM" {
		t.Errorf("camera = %q %q %q", info.CameraMake, info.CameraModel, info.Lens)
	}
	if info.CameraType != "camera" {
		t.Errorf("CameraType = %q", info.CameraType)
	}
	if info.Orientation != 6 {
		t.Errorf("Orientation = %d", info.Orientation)
	}
	if !info.HasGPS || math.Abs(info.Lat-60.1699) > 1e-5 || math.Abs(info.Lon+24.9384) > 1e-5 {
		t.Errorf("GPS = %v %v %v", info.HasGPS, info.Lat, info.Lon)
	}
	if !info.HasAltitude || math.Abs(info.Altitude-12.5) > 1e-9 {
		t.Errorf("Altitude = %v %v", info.HasAltitude, info.Altitude)
	}
	// Orientation 6: the catalog gets the display (upright) size.
	if info.Width != 16 || info.Height != 24 {
		t.Errorf("size = %dx%d, want display size 16x24", info.Width, info.Height)
	}
}

// TestExtractDisplayDimensions checks that orientations 5..8 swap the
// stored size and 1..4 keep it.
func TestExtractDisplayDimensions(t *testing.T) {
	for o := 1; o <= 8; o++ {
		data := testimg.JPEG(testimg.Options{Width: 24, Height: 18, Orientation: o})
		p, _ := writeFile(t, fmt.Sprintf("o%d.jpg", o), data)
		info, _, err := Default{DefaultZone: time.UTC}.Extract(context.Background(), p, KindImage)
		if err != nil {
			t.Fatal(err)
		}
		wantW, wantH := 24, 18
		if o >= 5 {
			wantW, wantH = 18, 24
		}
		if info.Orientation != o || info.Width != wantW || info.Height != wantH {
			t.Errorf("orientation %d: got orientation %d size %dx%d, want %dx%d",
				o, info.Orientation, info.Width, info.Height, wantW, wantH)
		}
	}
}

func TestExtractJPEGNoExif(t *testing.T) {
	zone := time.FixedZone("Z", -5*3600)
	p, mt := writeFile(t, "b.jpg", testimg.JPEG(testimg.Options{Width: 20, Height: 10, NoExif: true}))
	info, warns, err := Default{DefaultZone: zone}.Extract(context.Background(), p, KindImage)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 {
		t.Errorf("want exactly one warning, got %v", warns)
	}
	if info.Width != 20 || info.Height != 10 {
		t.Errorf("size = %dx%d", info.Width, info.Height)
	}
	checkMtimeFallback(t, info, mt, zone)
	if info.CameraType != "" {
		t.Errorf("CameraType = %q, want empty", info.CameraType)
	}
}

func TestExtractPNG(t *testing.T) {
	p, mt := writeFile(t, "c.PNG", testimg.PNG(7, 5, 1))
	info, _, err := Default{DefaultZone: time.UTC}.Extract(context.Background(), p, KindImage)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 7 || info.Height != 5 {
		t.Errorf("size = %dx%d", info.Width, info.Height)
	}
	checkMtimeFallback(t, info, mt, time.UTC)
}

func TestExtractRawUsesExifDimensions(t *testing.T) {
	// A JPEG-with-EXIF named .dng: the raw path skips DecodeConfig and must
	// not use the Exif PixelX/YDimension (often a preview size for raws).
	data := testimg.JPEG(testimg.Options{Width: 12, Height: 8, Make: "SONY", Model: "ILCE-7M3"})
	p, _ := writeFile(t, "d.dng", data)
	info, _, err := Default{}.Extract(context.Background(), p, KindRaw)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 0 || info.Height != 0 {
		t.Errorf("size = %dx%d, want unknown", info.Width, info.Height)
	}
	if info.CameraType != "camera" {
		t.Errorf("CameraType = %q", info.CameraType)
	}

	// A TIFF-based raw with the size in IFD0.
	tiff := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		testimg.Long(0x0100, 6000), testimg.Long(0x0101, 4000), testimg.ASCII(0x010F, "SONY")}})
	p, _ = writeFile(t, "e.arw", tiff)
	if info, _, _ = (Default{}).Extract(context.Background(), p, KindRaw); info.Width != 6000 || info.Height != 4000 {
		t.Errorf("ARW size = %dx%d", info.Width, info.Height)
	}
}

func TestExtractGarbage(t *testing.T) {
	full := testimg.JPEG(testimg.Options{Make: "Apple", Model: "iPhone 12", DateTime: time.Now()})
	cases := map[string][]byte{
		"garbage.jpg":   []byte("this is definitely not a jpeg file at all"),
		"truncated.jpg": full[:40],
		"empty.jpg":     {},
		"tiffhdr.jpg":   []byte("II*\x00\xff\xff\xff\xff\x00\x00"),
		"badexif.jpg":   append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x20}, []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00\xff\xff\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p, mt := writeFile(t, name, data)
			for _, kind := range []Kind{KindImage} {
				info, warns, err := Default{DefaultZone: time.UTC}.Extract(context.Background(), p, kind)
				if err != nil {
					t.Fatalf("%s: unexpected error %v", kind, err)
				}
				if len(warns) == 0 {
					t.Errorf("%s: expected warnings", kind)
				}
				checkMtimeFallback(t, info, mt, time.UTC)
			}
		})
	}
}

func TestParseExifDate(t *testing.T) {
	tests := []struct {
		in        string
		ok, zoned bool
		want      time.Time
	}{
		{"2020:01:05 12:34:56", true, false, time.Date(2020, 1, 5, 12, 34, 56, 0, time.UTC)},
		{"2020:01:05 12:34:56\x00", true, false, time.Date(2020, 1, 5, 12, 34, 56, 0, time.UTC)},
		{"2020:01:05 12:34:56+02:00", true, true, time.Date(2020, 1, 5, 10, 34, 56, 0, time.UTC)},
		{"2020:01:05 12:34:56Z", true, true, time.Date(2020, 1, 5, 12, 34, 56, 0, time.UTC)},
		{"2020-01-05 12:34:56", true, false, time.Date(2020, 1, 5, 12, 34, 56, 0, time.UTC)},
		{"2020-01-05T12:34:56", true, false, time.Date(2020, 1, 5, 12, 34, 56, 0, time.UTC)},
		{"2020-01-05T12:34:56-03:00", true, true, time.Date(2020, 1, 5, 15, 34, 56, 0, time.UTC)},
		{"    :  :     :  :  ", false, false, time.Time{}},
		{"0000:00:00 00:00:00", false, false, time.Time{}},
		{"1899:12:31 23:59:59", false, false, time.Time{}},
		{"1900:01:01 00:00:00", true, false, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2015:06:07 08:09:10xyz", false, false, time.Time{}},
		{"", false, false, time.Time{}},
		{"garbage", false, false, time.Time{}},
	}
	for _, tt := range tests {
		got, zoned, ok := parseExifDate(tt.in)
		if ok != tt.ok || zoned != tt.zoned || (ok && !got.Equal(tt.want)) {
			t.Errorf("parseExifDate(%q) = %v,%v,%v; want %v,%v,%v", tt.in, got, zoned, ok, tt.want, tt.zoned, tt.ok)
		}
	}
}

func TestParseExifOffset(t *testing.T) {
	for in, want := range map[string]int{"+02:00": 7200, "-05:30": -19800, "+00:00": 0, " +01:00\x00": 3600} {
		loc, ok := parseExifOffset(in)
		if !ok {
			t.Errorf("parseExifOffset(%q) failed", in)
			continue
		}
		if _, off := time.Date(2020, 1, 1, 0, 0, 0, 0, loc).Zone(); off != want {
			t.Errorf("parseExifOffset(%q) = %d, want %d", in, off, want)
		}
	}
	for _, in := range []string{"", "02:00", "+2:00", "+02:0x", "+15:00", "+02:60", "Z", "    :  "} {
		if _, ok := parseExifOffset(in); ok {
			t.Errorf("parseExifOffset(%q) accepted", in)
		}
	}
}

// extract writes data to a file and extracts it, failing if Extract hangs.
func extract(t *testing.T, name string, data []byte, kind Kind) (Info, []string) {
	t.Helper()
	p, _ := writeFile(t, name, data)
	type res struct {
		info  Info
		warns []string
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		info, warns, err := Default{FFProbe: "/nonexistent", DefaultZone: time.UTC}.Extract(context.Background(), p, kind)
		ch <- res{info, warns, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.info, r.warns
	case <-time.After(5 * time.Second):
		t.Fatalf("Extract(%s) did not return", name)
	}
	return Info{}, nil
}

func TestExtractDatePrecedenceAndOffsets(t *testing.T) {
	d := func(h int) time.Time { return time.Date(2020, 7, 1, h, 0, 0, 0, time.UTC) }
	tests := []struct {
		name     string
		o        testimg.Options
		wantWall time.Time
		wantOff  int
		offKnown bool
	}{
		{"original with offset", testimg.Options{DateTime: d(10), OffsetTimeOriginal: "+02:00", OffsetTime: "+05:00"}, d(10), 7200, true},
		{"original without offset", testimg.Options{DateTime: d(10), OffsetTime: "+05:00"}, d(10), 0, false},
		{"digitized only", testimg.Options{DateTimeDigitized: d(11), OffsetTimeDigitized: "-03:00"}, d(11), -3 * 3600, true},
		{"bad offset ignored", testimg.Options{DateTime: d(12), OffsetTimeOriginal: "local"}, d(12), 0, false},
	}
	for _, tt := range tests {
		info, warns := extract(t, "p.jpg", testimg.JPEG(tt.o), KindImage)
		_, off := info.TakenAt.Zone()
		wall := time.Date(info.TakenAt.Year(), info.TakenAt.Month(), info.TakenAt.Day(), info.TakenAt.Hour(), info.TakenAt.Minute(), info.TakenAt.Second(), 0, time.UTC)
		if info.Source != "exif" || !wall.Equal(tt.wantWall) || off != tt.wantOff || info.OffsetKnown != tt.offKnown {
			t.Errorf("%s: TakenAt=%v OffsetKnown=%v Source=%s warns=%v", tt.name, info.TakenAt, info.OffsetKnown, info.Source, warns)
		}
	}

	// DateTime (IFD0) is used only when the Exif dates are absent, with OffsetTime.
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x0132, "2023:05:05 10:00:00"), testimg.Ptr(0x8769, 2)}},
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x9010, "+01:00"), testimg.ASCII(0x9003, "    :  :     :  :  ")}},
	)
	info, warns := extract(t, "dt.jpg", testimg.JPEGWithTIFF(tiff), KindImage)
	if _, off := info.TakenAt.Zone(); info.TakenAt.Hour() != 10 || off != 3600 || !info.OffsetKnown || len(warns) != 0 {
		t.Errorf("DateTime fallback: %v %v warns=%v", info.TakenAt, info.OffsetKnown, warns)
	}
}

func TestExtractOrientationRange(t *testing.T) {
	tiff := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0112, 9)}})
	if info, _ := extract(t, "o.jpg", testimg.JPEGWithTIFF(tiff), KindImage); info.Orientation != 0 {
		t.Errorf("Orientation = %d", info.Orientation)
	}
}

// ---- regressions from the adversarial review ---------------------------------

// An IFD chain A -> B -> A used to loop forever.
func TestExtractIFDLoop(t *testing.T) {
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0112, 6), testimg.ASCII(0x010F, "Canon")}, Next: 2},
		testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0112, 1)}, Next: 1},
	)
	info, warns := extract(t, "loop.jpg", testimg.JPEGWithTIFF(tiff), KindImage)
	if info.Orientation != 6 || info.CameraMake != "Canon" || len(warns) == 0 {
		t.Errorf("info %+v warns %v", info, warns)
	}
}

// A SHORT tag with count 0x80000002 used to allocate 16 GiB.
func TestExtractCountOverflow(t *testing.T) {
	v := uint32(0x00010001)
	tiff := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		{Tag: 0x0112, Type: testimg.TypeShort, Count: 0x80000002, SetCount: true, Value: &v},
		testimg.ASCII(0x010F, "Canon"),
	}})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	info, warns := extract(t, "oom.jpg", testimg.JPEGWithTIFF(tiff), KindImage)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 4<<20 {
		t.Errorf("allocated %d bytes", alloc)
	}
	if info.CameraMake != "Canon" || len(warns) == 0 {
		t.Errorf("info %+v warns %v", info, warns)
	}
}

// The old EXIF library used the first APP1, so an XMP APP1 before the Exif APP1 lost all EXIF.
func TestExtractXMPBeforeExif(t *testing.T) {
	when := time.Date(2015, 6, 7, 8, 9, 10, 0, time.UTC)
	data := testimg.JPEG(testimg.Options{Width: 16, Height: 16, Make: "Canon", Model: "Canon EOS 5D", DateTime: when, XMPBeforeExif: true})
	info, warns := extract(t, "xmp_first.jpg", data, KindImage)
	if info.Source != "exif" || !info.TakenAt.Equal(when) || info.CameraMake != "Canon" {
		t.Errorf("Source=%q TakenAt=%v make=%q warns=%v", info.Source, info.TakenAt, info.CameraMake, warns)
	}
}

// DNG: IFD0 is a reduced-resolution preview; the main image is in a SubIFD.
func TestExtractDNGDimensions(t *testing.T) {
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{
			testimg.Long(0x00FE, 1), testimg.Long(0x0100, 256), testimg.Long(0x0101, 171),
			testimg.ASCII(0x010F, "Canon"), testimg.ASCII(0x0110, "Canon EOS R5"),
			testimg.Ptr(0x014A, 2),
		}},
		testimg.IFD{Entries: []testimg.Entry{testimg.Long(0x00FE, 0), testimg.Long(0x0100, 8192), testimg.Long(0x0101, 5464)}},
	)
	info, _ := extract(t, "x.dng", tiff, KindRaw)
	if info.Width != 8192 || info.Height != 5464 {
		t.Errorf("DNG dims = %dx%d, want 8192x5464", info.Width, info.Height)
	}
	// Without the SubIFD the size is unknown, never the preview size.
	only := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		testimg.Long(0x00FE, 1), testimg.Long(0x0100, 256), testimg.Long(0x0101, 171)}})
	if info, _ := extract(t, "y.dng", only, KindRaw); info.Width != 0 || info.Height != 0 {
		t.Errorf("preview-only DNG dims = %dx%d, want 0x0", info.Width, info.Height)
	}
}

// The old EXIF library read a whole TIFF-based raw into memory up to three times.
func TestExtractLargeTIFFMemory(t *testing.T) {
	const size = 200 << 20
	tiff := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		testimg.Long(0x0100, 100), testimg.Long(0x0101, 100), testimg.ASCII(0x0132, "2020:01:02 03:04:05")}})
	p := filepath.Join(t.TempDir(), "big.cr2")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(tiff); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	info, _, err := Default{DefaultZone: time.UTC}.Extract(context.Background(), p, KindRaw)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 2<<20 {
		t.Errorf("Extract of a 200 MiB TIFF-based raw allocated %d bytes", alloc)
	}
	if info.Source != "exif" || info.Width != 100 {
		t.Errorf("info %+v", info)
	}
}

// An unparsable DateTimeOriginal used to fall back to DateTime silently.
func TestExtractGarbageDateWarns(t *testing.T) {
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x0132, "2023:05:05 10:00:00"), testimg.Ptr(0x8769, 2)}},
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x9003, "2015:06:07 08:09:10xyz")}},
	)
	info, warns := extract(t, "g.jpg", testimg.JPEGWithTIFF(tiff), KindImage)
	if len(warns) == 0 {
		t.Errorf("no warning for unparsable DateTimeOriginal; TakenAt=%v", info.TakenAt)
	}
	if info.TakenAt.Year() != 2023 || info.Source != "exif" {
		t.Errorf("TakenAt=%v Source=%s", info.TakenAt, info.Source)
	}
}

// A malformed maker note used to discard all EXIF.
func TestExtractBadMakerNoteKeepsExif(t *testing.T) {
	mn := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{{Tag: 0x0001, Type: testimg.TypeShort, SetCount: true}}})[8:]
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x010F, "Canon"), testimg.ASCII(0x0110, "Canon EOS R5"), testimg.Ptr(0x8769, 2)}},
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x9003, "2015:06:07 08:09:10"), testimg.Undefined(0x927C, mn)}},
	)
	info, warns := extract(t, "canon.jpg", testimg.JPEGWithTIFF(tiff), KindImage)
	if info.Source != "exif" || info.CameraMake != "Canon" {
		t.Errorf("Source=%s make=%q warns=%v", info.Source, info.CameraMake, warns)
	}
}

// A bad individual tag (count 0, unknown type) costs only that tag.
func TestExtractBadTagKeepsRest(t *testing.T) {
	tiff := testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{
			{Tag: 0x0112, Type: testimg.TypeShort, SetCount: true},
			{Tag: 0x0110, Type: 99, Count: 4, SetCount: true},
			testimg.ASCII(0x010F, "Canon"), testimg.Ptr(0x8769, 2)}},
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x9003, "2015:06:07 08:09:10")}},
	)
	info, warns := extract(t, "badtag.jpg", testimg.JPEGWithTIFF(tiff), KindImage)
	if info.Source != "exif" || info.CameraMake != "Canon" || info.CameraModel != "" || len(warns) < 2 {
		t.Errorf("info %+v warns %v", info, warns)
	}
}
