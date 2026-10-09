package exif_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"picmover/internal/exif"
	"picmover/internal/testimg"
)

var (
	le = binary.LittleEndian
	be = binary.BigEndian
)

func read(t *testing.T, data []byte) *exif.Fields {
	t.Helper()
	f, err := exif.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return f
}

func TestJPEGRoundTrip(t *testing.T) {
	for _, bigEndian := range []bool{false, true} {
		data := testimg.JPEG(testimg.Options{
			Width: 24, Height: 16, Make: "Canon", Model: "Canon EOS R5", Lens: "RF24-105mm",
			DateTime:            time.Date(2021, 3, 14, 15, 9, 26, 0, time.UTC),
			DateTimeDigitized:   time.Date(2021, 3, 14, 15, 9, 27, 0, time.UTC),
			OffsetTimeOriginal:  "+02:00",
			OffsetTimeDigitized: "+03:00",
			OffsetTime:          "-05:30",
			Orientation:         8,
			GPS:                 true, Lat: -33.8688, Lon: 151.2093, Altitude: 42.25,
			BigEndian: bigEndian,
		})
		f := read(t, data)
		want := exif.Fields{
			DateTimeOriginal: "2021:03:14 15:09:26", DateTimeDigitized: "2021:03:14 15:09:27", DateTime: "2021:03:14 15:09:26",
			OffsetTimeOriginal: "+02:00", OffsetTimeDigitized: "+03:00", OffsetTime: "-05:30",
			Make: "Canon", Model: "Canon EOS R5", LensModel: "RF24-105mm",
			Orientation: 8, PixelXDimension: 24, PixelYDimension: 16,
			HasGPS: true, HasAltitude: true, Altitude: 42.25,
		}
		got := *f
		if math.Abs(got.Lat+33.8688) > 1e-5 || math.Abs(got.Lon-151.2093) > 1e-5 {
			t.Errorf("bigEndian=%v: lat/lon %v,%v", bigEndian, got.Lat, got.Lon)
		}
		got.Lat, got.Lon = 0, 0
		if got.Warnings != nil {
			t.Errorf("bigEndian=%v: warnings %v", bigEndian, got.Warnings)
		}
		got.Warnings = nil
		if !reflect.DeepEqual(got, want) {
			t.Errorf("bigEndian=%v:\n got %+v\nwant %+v", bigEndian, got, want)
		}
	}
}

func TestXMPBeforeExif(t *testing.T) {
	data := testimg.JPEG(testimg.Options{Make: "Canon", DateTime: time.Date(2015, 6, 7, 8, 9, 10, 0, time.UTC), XMPBeforeExif: true})
	if !bytes.Contains(data[:200], []byte("http://ns.adobe.com/xap/1.0/")) {
		t.Fatal("test JPEG has no XMP segment")
	}
	f := read(t, data)
	if f.Make != "Canon" || f.DateTimeOriginal != "2015:06:07 08:09:10" {
		t.Errorf("got %+v", f)
	}
}

func TestBareExifPayload(t *testing.T) {
	tiff := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x010F, "Sony")}})
	f := read(t, append([]byte("Exif\x00\x00"), tiff...))
	if f.Make != "Sony" {
		t.Errorf("Make = %q", f.Make)
	}
}

func TestTIFFFile(t *testing.T) {
	for _, bo := range []testimg.ByteOrder{le, be} {
		tiff := testimg.TIFF(bo,
			testimg.IFD{Entries: []testimg.Entry{
				testimg.Short(0x0100, 4000), testimg.Long(0x0101, 3000),
				testimg.ASCII(0x010F, "NIKON CORPORATION"), testimg.ASCII(0x0110, "NIKON D850"),
				testimg.Short(0x0112, 3), testimg.ASCII(0x0132, "2019:01:02 03:04:05"),
				testimg.Ptr(0x8769, 3), testimg.Ptr(0x8825, 4),
			}, Next: 2},
			testimg.IFD{Entries: []testimg.Entry{testimg.Long(0x0100, 160), testimg.Long(0x0101, 120)}},
			testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x9003, "2019:01:02 03:04:00"), testimg.ASCII(0x9011, "+01:00")}},
			testimg.IFD{Entries: []testimg.Entry{
				testimg.ASCII(0x0001, "n "), testimg.Rational(0x0002, [2]uint32{10, 1}, [2]uint32{30, 1}, [2]uint32{0, 1}),
				testimg.ASCII(0x0003, "w"), testimg.Rational(0x0004, [2]uint32{20, 1}, [2]uint32{15, 1}, [2]uint32{36, 1}),
				testimg.Byte(0x0005, 1), testimg.Rational(0x0006, [2]uint32{5, 2}),
			}},
		)
		f := read(t, tiff)
		if f.ImageWidth != 4000 || f.ImageLength != 3000 {
			t.Errorf("image size %dx%d, want largest 4000x3000", f.ImageWidth, f.ImageLength)
		}
		if f.Make != "NIKON CORPORATION" || f.Model != "NIKON D850" || f.Orientation != 3 ||
			f.DateTime != "2019:01:02 03:04:05" || f.DateTimeOriginal != "2019:01:02 03:04:00" || f.OffsetTimeOriginal != "+01:00" {
			t.Errorf("fields %+v", f)
		}
		if !f.HasGPS || math.Abs(f.Lat-10.5) > 1e-9 || math.Abs(f.Lon+20.26) > 1e-9 || !f.HasAltitude || f.Altitude != -2.5 {
			t.Errorf("GPS %v %v %v alt %v %v", f.HasGPS, f.Lat, f.Lon, f.HasAltitude, f.Altitude)
		}
		if len(f.Warnings) != 0 {
			t.Errorf("warnings %v", f.Warnings)
		}
	}
}

func TestRawHeaderVariants(t *testing.T) {
	base := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x010F, "OLYMPUS")}})
	for _, magic := range []string{"IIRO", "IIU\x00"} {
		b := append([]byte(magic), base[4:]...)
		if f := read(t, b); f.Make != "OLYMPUS" {
			t.Errorf("%q: Make = %q", magic, f.Make)
		}
	}
	mm := testimg.TIFF(be, testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x010F, "OLYMPUS")}})
	if f := read(t, append([]byte("MMOR"), mm[4:]...)); f.Make != "OLYMPUS" {
		t.Errorf("MMOR: Make = %q", f.Make)
	}
}

// dngLike has IFD0 = 256x171 preview, SubIFDs = [small JPEG preview, main image].
func dngLike(bo testimg.ByteOrder, mainNST bool) []byte {
	main := []testimg.Entry{testimg.Long(0x0100, 8192), testimg.Long(0x0101, 5464)}
	if mainNST {
		main = append([]testimg.Entry{testimg.Long(0x00FE, 0)}, main...)
	}
	return testimg.TIFF(bo,
		testimg.IFD{Entries: []testimg.Entry{
			testimg.Long(0x00FE, 1), testimg.Long(0x0100, 256), testimg.Long(0x0101, 171),
			testimg.ASCII(0x010F, "Canon"), testimg.ASCII(0x0110, "Canon EOS R5"),
			testimg.Ptr(0x014A, 2, 3),
		}},
		testimg.IFD{Entries: []testimg.Entry{testimg.Long(0x00FE, 1), testimg.Long(0x0100, 1024), testimg.Long(0x0101, 683)}},
		testimg.IFD{Entries: main},
	)
}

func TestDNGMainImageSize(t *testing.T) {
	for _, bo := range []testimg.ByteOrder{le, be} {
		for _, nst := range []bool{true, false} {
			f := read(t, dngLike(bo, nst))
			if f.ImageWidth != 8192 || f.ImageLength != 5464 {
				t.Errorf("nst=%v: size %dx%d, want 8192x5464", nst, f.ImageWidth, f.ImageLength)
			}
			if f.Make != "Canon" || f.Model != "Canon EOS R5" {
				t.Errorf("make/model %q %q", f.Make, f.Model)
			}
		}
	}
	// Only a reduced-resolution IFD: size unknown, never the preview size.
	only := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		testimg.Long(0x00FE, 1), testimg.Long(0x0100, 256), testimg.Long(0x0101, 171)}})
	if f := read(t, only); f.ImageWidth != 0 || f.ImageLength != 0 {
		t.Errorf("preview-only: size %dx%d, want 0x0", f.ImageWidth, f.ImageLength)
	}
}

func bmff(typ string, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	b := be.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

var canonUUID = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}

func cr3File() []byte {
	cmt1 := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		testimg.Long(0x0100, 6000), testimg.Long(0x0101, 4000),
		testimg.ASCII(0x010F, "Canon"), testimg.ASCII(0x0110, "Canon EOS R6"),
		testimg.Short(0x0112, 1), testimg.ASCII(0x0132, "2022:02:02 10:00:00"),
		testimg.Long(0x8769, 0xFFFF), // CMT1 pointers are not followed
	}})
	cmt2 := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
		testimg.ASCII(0x9003, "2022:02:02 09:59:58"), testimg.ASCII(0x9011, "+09:00"),
		testimg.Long(0xA002, 6000), testimg.Long(0xA003, 4000), testimg.ASCII(0xA434, "RF50mm F1.8 STM"),
	}})
	cmt4 := testimg.TIFF(be, testimg.IFD{Entries: []testimg.Entry{
		testimg.ASCII(0x0001, "N"), testimg.Rational(0x0002, [2]uint32{35, 1}, [2]uint32{30, 1}, [2]uint32{0, 1}),
		testimg.ASCII(0x0003, "E"), testimg.Rational(0x0004, [2]uint32{139, 1}, [2]uint32{45, 1}, [2]uint32{0, 1}),
	}})
	return append(bmff("ftyp", []byte("crx \x00\x00\x00\x01crx isom")),
		bmff("moov",
			bmff("mvhd", make([]byte, 100)),
			bmff("uuid", canonUUID, bmff("CNCV", []byte("CanonCR3_001/01.09.00/00.00.00")),
				bmff("CMT1", cmt1), bmff("CMT2", cmt2), bmff("CMT4", cmt4)),
		)...)
}

func TestCR3(t *testing.T) {
	data := append(cr3File(), bmff("mdat", make([]byte, 4096))...)
	f := read(t, data)
	if f.Make != "Canon" || f.Model != "Canon EOS R6" || f.DateTimeOriginal != "2022:02:02 09:59:58" ||
		f.OffsetTimeOriginal != "+09:00" || f.DateTime != "2022:02:02 10:00:00" || f.LensModel != "RF50mm F1.8 STM" ||
		f.ImageWidth != 6000 || f.ImageLength != 4000 || f.PixelXDimension != 6000 || f.Orientation != 1 {
		t.Errorf("fields %+v", f)
	}
	if !f.HasGPS || math.Abs(f.Lat-35.5) > 1e-9 || math.Abs(f.Lon-139.75) > 1e-9 {
		t.Errorf("GPS %v %v %v", f.HasGPS, f.Lat, f.Lon)
	}
	if len(f.Warnings) != 0 {
		t.Errorf("warnings %v", f.Warnings)
	}
}

func rafFile(jpg []byte, off, n uint32) []byte {
	h := make([]byte, 160)
	copy(h, "FUJIFILMCCD-RAW 0201FF383501")
	be.PutUint32(h[84:], off)
	be.PutUint32(h[88:], n)
	return append(h, jpg...)
}

func TestRAF(t *testing.T) {
	jpg := testimg.JPEG(testimg.Options{Width: 32, Height: 24, Make: "FUJIFILM", Model: "X-T4",
		DateTime: time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)})
	f := read(t, append(rafFile(jpg, 160, uint32(len(jpg))), make([]byte, 1000)...))
	if f.Make != "FUJIFILM" || f.Model != "X-T4" || f.DateTimeOriginal != "2020:05:06 07:08:09" {
		t.Errorf("fields %+v", f)
	}
	if f.ImageWidth != 0 {
		t.Errorf("RAF ImageWidth %d from the preview JPEG", f.ImageWidth)
	}
}

func TestNoExif(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":   nil,
		"text":    []byte("hello world, not an image"),
		"bareJPG": testimg.BareJPEG(),
		"png":     testimg.PNG(4, 4, 1),
		"badTIFF": []byte("II*\x00\xff\xff\xff\xff\x00\x00"),
		"ftyp":    bmff("ftyp", []byte("isom")),
	} {
		if _, err := exif.Read(bytes.NewReader(data), int64(len(data))); !errors.Is(err, exif.ErrNoExif) {
			t.Errorf("%s: err = %v, want ErrNoExif", name, err)
		}
	}
}

// ---- hostile input ---------------------------------------------------------

const (
	maxDuration = 100 * time.Millisecond
	maxAlloc    = 2 << 20
)

// measure runs exif.Read and returns its result, wall time and bytes allocated.
func measure(r interface {
	ReadAt([]byte, int64) (int, error)
}, size int64) (*exif.Fields, error, time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	f, err := exif.Read(r, size)
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	return f, err, d, after.TotalAlloc - before.TotalAlloc
}

func u32(v uint32) *uint32 { return &v }

var (
	goodIFD0 = []testimg.Entry{testimg.ASCII(0x010F, "Canon"), testimg.ASCII(0x0110, "Canon EOS 5D")}
	goodDate = testimg.ASCII(0x9003, "2015:06:07 08:09:10")
)

func ifd0With(extra ...testimg.Entry) []testimg.Entry {
	return append(append([]testimg.Entry{}, goodIFD0...), extra...)
}

func TestHostileTIFF(t *testing.T) {
	chain := make([]testimg.IFD, 40)
	nest := make([]testimg.IFD, 10)
	for i := range chain {
		chain[i] = testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0112, 1)}, Next: i + 2}
	}
	chain[0].Entries = ifd0With(testimg.Ptr(0x8769, 40))
	chain[39] = testimg.IFD{Entries: []testimg.Entry{goodDate}}
	for i := range nest {
		nest[i] = testimg.IFD{Entries: []testimg.Entry{testimg.Ptr(0x014A, i+2)}}
	}
	nest[0].Entries = ifd0With(testimg.Ptr(0x014A, 2), testimg.Ptr(0x8769, 10))
	nest[9] = testimg.IFD{Entries: []testimg.Entry{goodDate}}

	cases := []struct {
		name     string
		ifds     []testimg.IFD
		wantDate bool // DateTimeOriginal must survive
		wantWarn bool
	}{
		{"ifd loop A-B-A", []testimg.IFD{
			{Entries: ifd0With(testimg.Ptr(0x8769, 3)), Next: 2},
			{Entries: []testimg.Entry{testimg.Short(0x0112, 1)}, Next: 1},
			{Entries: []testimg.Entry{goodDate}},
		}, true, true},
		{"exif ifd points to itself and to ifd0", []testimg.IFD{
			{Entries: ifd0With(testimg.Ptr(0x8769, 2), testimg.Ptr(0x8825, 1), testimg.Ptr(0x014A, 1, 2))},
			{Entries: []testimg.Entry{goodDate, testimg.Ptr(0xA005, 2)}, Next: 1},
		}, true, true},
		{"count overflow", []testimg.IFD{
			{Entries: ifd0With(testimg.Entry{Tag: 0x0112, Type: testimg.TypeShort, Count: 0x80000002, SetCount: true, Value: u32(0x00010001)}, testimg.Ptr(0x8769, 2))},
			{Entries: []testimg.Entry{goodDate}},
		}, true, true},
		{"huge ascii", []testimg.IFD{
			{Entries: ifd0With(testimg.Entry{Tag: 0x0132, Type: testimg.TypeASCII, Count: 0x7FFFFFFF, SetCount: true, Value: u32(8)}, testimg.Ptr(0x8769, 2))},
			{Entries: []testimg.Entry{goodDate}},
		}, true, true},
		{"value offset past EOF", []testimg.IFD{
			{Entries: ifd0With(testimg.Entry{Tag: 0xA434, Type: testimg.TypeASCII, Count: 100, SetCount: true, Value: u32(0xFFFFFF00)}, testimg.Ptr(0x8769, 2))},
			{Entries: []testimg.Entry{goodDate, testimg.Entry{Tag: 0xA434, Type: testimg.TypeASCII, Count: 100, SetCount: true, Value: u32(0xFFFFFF00)}}},
		}, true, true},
		{"zero denominator", []testimg.IFD{
			{Entries: ifd0With(testimg.Ptr(0x8769, 2), testimg.Ptr(0x8825, 3))},
			{Entries: []testimg.Entry{goodDate}},
			{Entries: []testimg.Entry{
				testimg.ASCII(0x0001, "N"), testimg.Rational(0x0002, [2]uint32{1, 0}, [2]uint32{2, 1}, [2]uint32{3, 1}),
				testimg.ASCII(0x0003, "E"), testimg.Rational(0x0004, [2]uint32{1, 1}, [2]uint32{2, 1}, [2]uint32{3, 1}),
				testimg.Rational(0x0006, [2]uint32{7, 0}),
			}},
		}, true, true},
		{"count 0", []testimg.IFD{
			{Entries: ifd0With(testimg.Entry{Tag: 0x0112, Type: testimg.TypeShort, SetCount: true}, testimg.Ptr(0x8769, 2))},
			{Entries: []testimg.Entry{goodDate}},
		}, true, true},
		{"unknown type", []testimg.IFD{
			{Entries: ifd0With(testimg.Entry{Tag: 0x0132, Type: 99, Count: 20, SetCount: true, Value: u32(8)}, testimg.Ptr(0x8769, 2))},
			{Entries: []testimg.Entry{goodDate}},
		}, true, true},
		{"maker note junk", []testimg.IFD{
			{Entries: ifd0With(testimg.Ptr(0x8769, 2))},
			{Entries: []testimg.Entry{goodDate,
				testimg.Undefined(0x927C, append([]byte("Nikon\x00\x02\x10\x00\x00MM\x00*\x00\x00\x00\x08\xff\xff"), bytes.Repeat([]byte{0xFF}, 64)...)),
				{Tag: 0x927C, Type: testimg.TypeUndefined, Count: 0xFFFFFFFF, SetCount: true, Value: u32(0)}}},
		}, true, false},
		{"pointer past EOF", []testimg.IFD{
			{Entries: ifd0With(testimg.Long(0x8769, 0x7FFFFFF0), testimg.Long(0x8825, 3), testimg.Long(0x014A, 0xFFFFFFFF))},
		}, false, true},
		{"chain of 40 IFDs", chain, false, true},
		{"subifd nesting of 10", nest, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, wrap := range []string{"tiff", "jpeg"} {
				data := testimg.TIFF(nil, c.ifds...)
				if wrap == "jpeg" {
					data = testimg.JPEGWithTIFF(data, testimg.XMPSegment())
				}
				f, err, d, alloc := measure(bytes.NewReader(data), int64(len(data)))
				t.Logf("%s: %v, %d bytes allocated", wrap, d, alloc)
				if err != nil {
					t.Fatalf("%s: Read: %v", wrap, err)
				}
				if d > maxDuration || alloc > maxAlloc {
					t.Errorf("%s: took %v and allocated %d bytes", wrap, d, alloc)
				}
				if f.Make != "Canon" || f.Model != "Canon EOS 5D" {
					t.Errorf("%s: lost make/model: %+v", wrap, f)
				}
				if c.wantDate && f.DateTimeOriginal != "2015:06:07 08:09:10" {
					t.Errorf("%s: lost DateTimeOriginal: %+v", wrap, f)
				}
				if c.wantWarn && len(f.Warnings) == 0 {
					t.Errorf("%s: no warning", wrap)
				}
				if f.HasGPS || f.HasAltitude {
					t.Errorf("%s: GPS from hostile data: %+v", wrap, f)
				}
			}
		})
	}
}

func TestHostileContainers(t *testing.T) {
	good := testimg.TIFF(nil, testimg.IFD{Entries: ifd0With()})
	jpegSeg := func(marker byte, length uint16, payload []byte) []byte {
		return append([]byte{0xFF, marker, byte(length >> 8), byte(length)}, payload...)
	}
	bigBox := func(typ string, size uint32) []byte { return append(be.AppendUint32(nil, size), typ...) }
	largeBox := func(typ string, size uint64) []byte {
		return append(append(be.AppendUint32(nil, 1), typ...), be.AppendUint64(nil, size)...)
	}
	manyBoxes := bmff("ftyp", []byte("crx "))
	for i := 0; i < 10000; i++ {
		manyBoxes = append(manyBoxes, bmff("free")...)
	}
	cases := map[string][]byte{
		"jpeg length 0":            {0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x00, 0xFF, 0xE0, 0x00, 0x00},
		"jpeg length 1":            {0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01},
		"jpeg truncated exif":      append([]byte{0xFF, 0xD8}, jpegSeg(0xE1, 0xFFFF, append([]byte("Exif\x00\x00"), good...))...),
		"jpeg fill bytes":          append(append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0xFF}, 100000)...), 0xD9),
		"jpeg many segments":       append([]byte{0xFF, 0xD8}, bytes.Repeat(jpegSeg(0xE2, 2, nil), 50000)...),
		"jpeg exif bad tiff":       testimg.JPEGWithTIFF([]byte("XX*\x00\x08\x00\x00\x00")),
		"cr3 box larger than file": append(bmff("ftyp", []byte("crx ")), bigBox("moov", 0xFFFFFFF0)...),
		"cr3 largesize huge":       append(bmff("ftyp", []byte("crx ")), largeBox("moov", 1<<62)...),
		"cr3 box size 4":           append(bmff("ftyp", []byte("crx ")), bigBox("moov", 4)...),
		"cr3 many boxes":           manyBoxes,
		"cr3 uuid without cmt":     append(bmff("ftyp", []byte("crx ")), bmff("moov", bmff("uuid", canonUUID, bigBox("CMT1", 0xFFFF)))...),
		"cr3 cmt bad tiff":         append(bmff("ftyp", []byte("crx ")), bmff("moov", bmff("uuid", canonUUID, bmff("CMT1", []byte("garbage!garbage!"))))...),
		"raf offset past EOF":      rafFile(testimg.JPEGWithTIFF(good), 0xFFFFFF00, 0xFFFF),
		"raf length past EOF":      rafFile(testimg.JPEGWithTIFF(good), 160, 0xFFFFFFFF),
		"raf not jpeg":             rafFile(good, 160, uint32(len(good))),
		"tiff ifd0 0xffff entries": append([]byte("II*\x00\x08\x00\x00\x00\xff\xff"), make([]byte, 100)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			f, err, d, alloc := measure(bytes.NewReader(data), int64(len(data)))
			t.Logf("%v, %d bytes allocated, err=%v", d, alloc, err)
			if d > maxDuration || alloc > maxAlloc {
				t.Errorf("took %v and allocated %d bytes", d, alloc)
			}
			if err == nil && f == nil {
				t.Error("nil Fields without error")
			}
		})
	}
	// A truncated Exif segment still yields what it holds.
	data := cases["jpeg truncated exif"]
	if f, err := exif.Read(bytes.NewReader(data), int64(len(data))); err != nil || f.Make != "Canon" {
		t.Errorf("truncated exif: %v %+v", err, f)
	}
}

// A large file is never read beyond its metadata.
func TestLargeSparseFile(t *testing.T) {
	const size = 64 << 20
	for _, name := range []string{"big.cr2", "big.jpg"} {
		data := dngLike(le, true)
		if strings.HasSuffix(name, ".jpg") {
			data = testimg.JPEG(testimg.Options{Make: "Canon", Model: "Canon EOS R5", DateTime: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)})
		}
		p := filepath.Join(t.TempDir(), name)
		fh, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fh.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := fh.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f, err, d, alloc := measure(fh, size)
		fh.Close()
		t.Logf("%s (64 MiB): %v, %d bytes allocated", name, d, alloc)
		if err != nil {
			t.Fatal(err)
		}
		if alloc > maxAlloc {
			t.Errorf("%s: allocated %d bytes", name, alloc)
		}
		if f.Make != "Canon" {
			t.Errorf("%s: Make = %q", name, f.Make)
		}
	}
}
