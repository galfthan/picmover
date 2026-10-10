package exif_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"picmover/internal/exif"
	"picmover/internal/testimg"
)

// appleTIFF is an iPhone-like EXIF block; orientation 0 omits the tag.
func appleTIFF(orientation int) []byte {
	ifd0 := []testimg.Entry{testimg.ASCII(0x010F, "Apple"), testimg.ASCII(0x0110, "iPhone 15 Pro")}
	if orientation > 0 {
		ifd0 = append(ifd0, testimg.Short(0x0112, uint32(orientation)))
	}
	ifd0 = append(ifd0, testimg.ASCII(0x0132, "2024:07:01 12:00:05"), testimg.Ptr(0x8769, 2), testimg.Ptr(0x8825, 3))
	return testimg.TIFF(be,
		testimg.IFD{Entries: ifd0},
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x9003, "2024:07:01 12:00:00"), testimg.ASCII(0x9011, "+03:00"),
			testimg.Long(0xA002, 4032), testimg.Long(0xA003, 3024), testimg.ASCII(0xA434, "iPhone 15 Pro back camera"),
		}},
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x0001, "N"), testimg.Rational(0x0002, [2]uint32{60, 1}, [2]uint32{10, 1}, [2]uint32{12, 1}),
			testimg.ASCII(0x0003, "E"), testimg.Rational(0x0004, [2]uint32{24, 1}, [2]uint32{56, 1}, [2]uint32{24, 1}),
		}},
	)
}

func TestHEIFExif(t *testing.T) {
	base := testimg.HEIFOptions{Exif: appleTIFF(0), Width: 4032, Height: 3024}
	variants := map[string]func(o *testimg.HEIFOptions){
		"default":          func(o *testimg.HEIFOptions) {},
		"apple prefix":     func(o *testimg.HEIFOptions) { o.ExifPrefix = true },
		"avif":             func(o *testimg.HEIFOptions) { o.Brand = "avif" },
		"mif1":             func(o *testimg.HEIFOptions) { o.Brand = "mif1" },
		"iloc v0":          func(o *testimg.HEIFOptions) { o.IlocVersion = 0 },
		"iloc 64-bit":      func(o *testimg.HEIFOptions) { o.OffsetSize = 8 },
		"iloc base 4":      func(o *testimg.HEIFOptions) { o.BaseOffsetSize = 4 },
		"iloc base 8":      func(o *testimg.HEIFOptions) { o.BaseOffsetSize = 8; o.OffsetSize = 8 },
		"large ids":        func(o *testimg.HEIFOptions) { o.Large = true },
		"no cdsc":          func(o *testimg.HEIFOptions) { o.NoCdsc = true },
		"extra items":      func(o *testimg.HEIFOptions) { o.ExtraItems = 40 },
		"with thumb":       func(o *testimg.HEIFOptions) { o.Thumb = jpg(16, 12, 1); o.ThumbW, o.ThumbH = 16, 12 },
		"with transforms":  func(o *testimg.HEIFOptions) { o.Transforms = []testimg.HEIFTransform{{Value: 0}} },
		"large + prefix":   func(o *testimg.HEIFOptions) { o.Large = true; o.ExifPrefix = true; o.BaseOffsetSize = 4 },
		"claimed exactly":  func(o *testimg.HEIFOptions) { o.ClaimItems = 2 },
		"claimed 10k item": func(o *testimg.HEIFOptions) { o.ClaimItems = 10000 },
	}
	for name, mod := range variants {
		o := base
		mod(&o)
		data := testimg.HEIF(o)
		f, err := exif.Read(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.Make != "Apple" || f.Model != "iPhone 15 Pro" || f.DateTimeOriginal != "2024:07:01 12:00:00" ||
			f.OffsetTimeOriginal != "+03:00" || f.DateTime != "2024:07:01 12:00:05" || f.PixelXDimension != 4032 ||
			f.LensModel != "iPhone 15 Pro back camera" {
			t.Errorf("%s: fields %+v", name, f)
		}
		if !f.HasGPS || math.Abs(f.Lat-(60+10/60.+12/3600.)) > 1e-9 || math.Abs(f.Lon-(24+56/60.+24/3600.)) > 1e-9 {
			t.Errorf("%s: GPS %v %v %v", name, f.HasGPS, f.Lat, f.Lon)
		}
		if f.Orientation != 0 && name != "with transforms" {
			t.Errorf("%s: orientation %d without irot/imir", name, f.Orientation)
		}
		if len(f.Warnings) != 0 && name != "claimed 10k item" {
			t.Errorf("%s: warnings %v", name, f.Warnings)
		}
	}
}

// grid applies HEIF transforms to a w x h grid of labels y*w+x, following
// the HEIF definitions directly (irot: counter-clockwise; imir axis 0:
// left-right, axis 1: top-bottom).
func applyHEIF(w, h int, ops []testimg.HEIFTransform) [][]int {
	g := make([][]int, h)
	for y := range g {
		g[y] = make([]int, w)
		for x := range g[y] {
			g[y][x] = y*w + x
		}
	}
	for _, op := range ops {
		h, w := len(g), len(g[0])
		switch {
		case op.Mirror && op.Value == 0:
			n := make([][]int, h)
			for y := range n {
				n[y] = make([]int, w)
				for x := range n[y] {
					n[y][x] = g[y][w-1-x]
				}
			}
			g = n
		case op.Mirror:
			n := make([][]int, h)
			for y := range n {
				n[y] = append([]int{}, g[h-1-y]...)
			}
			g = n
		default:
			for i := 0; i < op.Value; i++ { // rotate 90 counter-clockwise
				h, w := len(g), len(g[0])
				n := make([][]int, w)
				for y := range n {
					n[y] = make([]int, h)
					for x := range n[y] {
						n[y][x] = g[x][w-1-y]
					}
				}
				g = n
			}
		}
	}
	return g
}

// exifFromDisplay derives the EXIF orientation from where the stored
// image's 0th row and 0th column ended up, per the EXIF definition.
func exifFromDisplay(g [][]int, w, h int) int {
	pos := func(label int) (int, int) {
		for y := range g {
			for x := range g[y] {
				if g[y][x] == label {
					return x, y
				}
			}
		}
		panic("label not found")
	}
	dh, dw := len(g), len(g[0])
	edge := func(a, b int) string {
		ax, ay := pos(a)
		bx, by := pos(b)
		switch {
		case ay == 0 && by == 0:
			return "top"
		case ay == dh-1 && by == dh-1:
			return "bottom"
		case ax == 0 && bx == 0:
			return "left"
		case ax == dw-1 && bx == dw-1:
			return "right"
		}
		panic("not an edge")
	}
	row0, col0 := edge(0, w-1), edge(0, (h-1)*w)
	return map[[2]string]int{
		{"top", "left"}: 1, {"top", "right"}: 2, {"bottom", "right"}: 3, {"bottom", "left"}: 4,
		{"left", "top"}: 5, {"right", "top"}: 6, {"right", "bottom"}: 7, {"left", "bottom"}: 8,
	}[[2]string{row0, col0}]
}

func TestHEIFOrientationMapping(t *testing.T) {
	rot := func(a int) testimg.HEIFTransform { return testimg.HEIFTransform{Value: a} }
	mir := func(a int) testimg.HEIFTransform { return testimg.HEIFTransform{Mirror: true, Value: a} }
	type tc struct {
		ops  []testimg.HEIFTransform
		want int
	}
	// The documented table.
	cases := []tc{
		{[]testimg.HEIFTransform{rot(0)}, 1}, {[]testimg.HEIFTransform{rot(1)}, 8},
		{[]testimg.HEIFTransform{rot(2)}, 3}, {[]testimg.HEIFTransform{rot(3)}, 6},
		{[]testimg.HEIFTransform{mir(0)}, 2}, {[]testimg.HEIFTransform{mir(1)}, 4},
		{[]testimg.HEIFTransform{rot(1), mir(0)}, 7}, {[]testimg.HEIFTransform{rot(1), mir(1)}, 5},
		{[]testimg.HEIFTransform{rot(2), mir(0)}, 4}, {[]testimg.HEIFTransform{rot(2), mir(1)}, 2},
		{[]testimg.HEIFTransform{rot(3), mir(0)}, 5}, {[]testimg.HEIFTransform{rot(3), mir(1)}, 7},
		{[]testimg.HEIFTransform{mir(0), rot(1)}, 5}, {[]testimg.HEIFTransform{mir(0), rot(3)}, 7},
		{[]testimg.HEIFTransform{mir(1), rot(1)}, 7}, {[]testimg.HEIFTransform{mir(1), rot(3)}, 5},
		{[]testimg.HEIFTransform{mir(0), rot(2)}, 4}, {[]testimg.HEIFTransform{mir(1), rot(2)}, 2},
		{[]testimg.HEIFTransform{rot(1), rot(1)}, 3}, {[]testimg.HEIFTransform{mir(0), mir(0)}, 1},
	}
	for i, c := range cases {
		// The table agrees with a direct simulation on a 3x2 grid.
		if got := exifFromDisplay(applyHEIF(3, 2, c.ops), 3, 2); got != c.want {
			t.Errorf("case %d %+v: table says %d, simulation %d", i, c.ops, c.want, got)
		}
		for _, withExif := range []bool{true, false} {
			o := testimg.HEIFOptions{Transforms: c.ops, Width: 3, Height: 2}
			if withExif {
				o.Exif = appleTIFF(0)
			}
			data := testimg.HEIF(o)
			f := read(t, data)
			if f.Orientation != c.want {
				t.Errorf("case %d %+v exif=%v: orientation %d, want %d", i, c.ops, withExif, f.Orientation, c.want)
			}
		}
	}
	// irot/imir win over a disagreeing EXIF tag, with a warning.
	data := testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(6), Transforms: []testimg.HEIFTransform{rot(1)}})
	if f := read(t, data); f.Orientation != 8 || !hasWarning(f.Warnings, "disagrees") {
		t.Errorf("EXIF orientation 6 with irot 1: got %d, warnings %q", f.Orientation, f.Warnings)
	}
	// Agreement (the iPhone case: EXIF 6 and irot 3) is silent.
	data = testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(6), Transforms: []testimg.HEIFTransform{rot(3)}})
	if f := read(t, data); f.Orientation != 6 || len(f.Warnings) != 0 {
		t.Errorf("EXIF orientation 6 with irot 3: got %d, warnings %q", f.Orientation, f.Warnings)
	}
	// Without irot/imir the EXIF tag is used.
	data = testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(5)})
	if f := read(t, data); f.Orientation != 5 {
		t.Errorf("EXIF orientation 5 without transforms: got %d", f.Orientation)
	}
	// No Exif item and no transforms: no EXIF.
	data = testimg.HEIF(testimg.HEIFOptions{Width: 8, Height: 8})
	if _, err := exif.Read(bytes.NewReader(data), int64(len(data))); !errors.Is(err, exif.ErrNoExif) {
		t.Errorf("no Exif, no transforms: err %v", err)
	}
}

func TestHEIFPreviews(t *testing.T) {
	thumb := jpg(32, 24, 3)
	for _, large := range []bool{false, true} {
		data := testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(1), Thumb: thumb, ThumbW: 32, ThumbH: 24, Large: large, BaseOffsetSize: 4})
		wantPreviews(t, fmt.Sprintf("large=%v", large), previews(t, data), []exif.PreviewRef{ref(t, data, thumb, 32, 24)})
	}
	// HEVC-coded thumbnails are not JPEG previews, whatever their bytes.
	data := testimg.HEIF(testimg.HEIFOptions{Thumb: thumb, ThumbType: "hvc1"})
	wantPreviews(t, "hvc1 thumb", previews(t, data), nil)
	// A jpeg item that is not referenced as thmb is not a preview either.
	data = testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(1)})
	wantPreviews(t, "no thumb", previews(t, data), nil)
}

func TestNotHEIF(t *testing.T) {
	// A CR3-branded file is not parsed as HEIF and vice versa.
	data := testimg.CR3(testimg.CR3Options{CMT1: testimg.TIFF(nil, testimg.IFD{Entries: ifd0With()})})
	if f := read(t, data); f.Make != "Canon" {
		t.Errorf("CR3: %+v", f)
	}
	data = append(testimg.Box("ftyp", []byte("isom\x00\x00\x00\x00isommp41")), testimg.Box("moov")...)
	if _, err := exif.Read(bytes.NewReader(data), int64(len(data))); !errors.Is(err, exif.ErrNoExif) {
		t.Errorf("mp4: %v", err)
	}
}

// ---- hostile HEIF ----------------------------------------------------------

// patchMeta returns a copy of data with the meta box's children replaced by
// kids (the meta box keeps its version/flags).
func heifWithMeta(kids ...[]byte) []byte {
	return append(testimg.Box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic")), testimg.FullBox("meta", 0, 0, kids...)...)
}

func TestHEIFHostile(t *testing.T) {
	good := testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(0), Thumb: jpg(8, 8, 1), ThumbW: 8, ThumbH: 8})
	size := uint64(len(good))
	sized := func(typ string, n uint32, payload ...byte) []byte {
		return append(append(binary.BigEndian.AppendUint32(nil, n), typ...), payload...)
	}
	metaAt := bytes.Index(good, []byte("meta")) - 4
	zeroMeta := append([]byte{}, good...)
	binary.BigEndian.PutUint32(zeroMeta[metaAt:], 0)
	manyInfe := [][]byte{be.AppendUint16(nil, 0xFFFF)}
	for i := 0; i < 3000; i++ {
		manyInfe = append(manyInfe, testimg.FullBox("infe", 2, 0, be.AppendUint16(nil, uint16(i)), []byte("\x00\x00Exif\x00")))
	}
	manyTopLevel := testimg.Box("ftyp", []byte("heic"))
	for i := 0; i < 10000; i++ {
		manyTopLevel = append(manyTopLevel, testimg.Box("free")...)
	}
	bigIpma := []byte{0, 0, 0, 0}
	for i := 0; i < 2000; i++ {
		bigIpma = append(bigIpma, 0, 1, 255)
		bigIpma = append(bigIpma, bytes.Repeat([]byte{0x81}, 255)...)
	}
	bigIpma = append(be.AppendUint32(nil, 2000), bigIpma[4:]...)
	bigIref := []byte{}
	for i := 0; i < 2000; i++ {
		bigIref = append(bigIref, testimg.Box("thmb", []byte{0, 3, 0xFF, 0xFF})...)
	}

	cases := map[string][]byte{
		"iloc extents past EOF": testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(0), Thumb: jpg(8, 8, 1),
			ExifExtent: &[2]uint64{size + 100, 50}, ThumbExtent: &[2]uint64{100, 1 << 31}}),
		"iloc 64-bit overflow": testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(0), Thumb: jpg(8, 8, 1), OffsetSize: 8, BaseOffsetSize: 8,
			ExifExtent: &[2]uint64{math.MaxUint64 - 10, 100}, ThumbExtent: &[2]uint64{8, math.MaxUint64}}),
		"10k items claimed":         testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(0), Thumb: jpg(8, 8, 1), ClaimItems: 10000, Large: true}),
		"2000 items present":        testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(0), Thumb: jpg(8, 8, 1), ExtraItems: 2000}),
		"3000 infe boxes":           heifWithMeta(testimg.FullBox("iinf", 0, 0, manyInfe...)),
		"meta size zero":            zeroMeta,
		"zero-size child box":       heifWithMeta(sized("pitm", 0), testimg.FullBox("iinf", 0, 0)),
		"child smaller than header": heifWithMeta(sized("iinf", 4, make([]byte, 32)...)),
		"meta larger than file":     append(testimg.Box("ftyp", []byte("heic")), sized("meta", 0xFFFFFFF0)...),
		"meta largesize huge":       append(testimg.Box("ftyp", []byte("heic")), append(sized("meta", 1), be.AppendUint64(nil, 1<<62)...)...),
		"meta largesize tiny":       append(testimg.Box("ftyp", []byte("heic")), append(sized("meta", 1), be.AppendUint64(nil, 3)...)...),
		"many top-level boxes":      manyTopLevel,
		"iloc bad field sizes":      heifWithMeta(testimg.FullBox("pitm", 0, 0, []byte{0, 1}), testimg.FullBox("iloc", 1, 0, []byte{0x33, 0x30, 0, 1})),
		"iloc huge extent count":    heifWithMeta(testimg.FullBox("iloc", 1, 0, []byte{0, 0, 0xFF, 0xFF}, bytes.Repeat([]byte{0, 1, 0, 0, 0, 0, 0xFF, 0xFF}, 4000))),
		"ipma 2000x255":             heifWithMeta(testimg.FullBox("pitm", 0, 0, []byte{0, 1}), testimg.Box("iprp", testimg.Box("ipco"), testimg.FullBox("ipma", 0, 0, bigIpma))),
		"iref 2000 thmb":            heifWithMeta(testimg.FullBox("iref", 0, 0, bigIref)),
		"iref entry size zero":      heifWithMeta(testimg.FullBox("iref", 0, 0, sized("thmb", 0, 0, 1, 0, 1, 0, 2))),
		"exif tiff offset huge":     testimg.HEIF(testimg.HEIFOptions{Exif: append([]byte{0xFF, 0xFF, 0xFF, 0xF0}, appleTIFF(0)...)}),
		"exif item garbage":         testimg.HEIF(testimg.HEIFOptions{Exif: bytes.Repeat([]byte{0xFF}, 40)}),
		"hdlr not pict":             heifWithMeta(testimg.FullBox("hdlr", 0, 0, []byte("\x00\x00\x00\x00vide"))),
		"truncated":                 good[:len(good)/2],
		"ftyp only":                 testimg.Box("ftyp", []byte("heic")),
		"ftyp lying size":           append([]byte("\xff\xff\xff\xffftypheic"), make([]byte, 16)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			f, err, d, alloc := measure(bytes.NewReader(data), int64(len(data)))
			t.Logf("Read: %v, %d bytes allocated, err=%v", d, alloc, err)
			if d > maxDuration || alloc > maxAlloc {
				t.Errorf("Read took %v and allocated %d bytes", d, alloc)
			}
			if err == nil && f == nil {
				t.Error("nil Fields without error")
			}
			if err != nil && !errors.Is(err, exif.ErrNoExif) {
				t.Errorf("err %v is not ErrNoExif", err)
			}
			p := checkPreviewsBounded(t, name, bytes.NewReader(data), int64(len(data)))
			switch name {
			case "iloc extents past EOF", "iloc 64-bit overflow":
				if err == nil || len(p) != 0 {
					t.Errorf("got %+v %v, want nothing", p, err)
				}
			case "10k items claimed", "meta size zero":
				if err != nil || f.Make != "Apple" || len(p) != 1 {
					t.Errorf("got %v %+v %+v, want the Exif and one preview", err, f, p)
				}
			}
		})
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
