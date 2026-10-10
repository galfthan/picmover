package exif_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"picmover/internal/exif"
	"picmover/internal/testimg"
)

// cost runs f and returns its wall time and bytes allocated.
func cost(f func()) (time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	return d, after.TotalAlloc - before.TotalAlloc
}

func previews(t *testing.T, data []byte) []exif.PreviewRef {
	t.Helper()
	p, err := exif.Previews(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Previews: %v", err)
	}
	return p
}

// jpg returns a small real JPEG of the given size.
func jpg(w, h int, seed byte) []byte {
	return testimg.JPEG(testimg.Options{Width: w, Height: h, Seed: seed, NoExif: true})
}

// ref locates the bytes of j inside data.
func ref(t *testing.T, data, j []byte, w, h int) exif.PreviewRef {
	t.Helper()
	i := bytes.Index(data, j)
	if i < 0 {
		t.Fatal("JPEG not found in test file")
	}
	return exif.PreviewRef{Offset: int64(i), Length: int64(len(j)), Width: w, Height: h}
}

func wantPreviews(t *testing.T, name string, got, want []exif.PreviewRef) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
	}
}

// lossless is a fake lossless-JPEG raw strip: it starts with FF D8 FF but
// must never be reported.
var lossless = append([]byte{0xFF, 0xD8, 0xFF, 0xC4}, make([]byte, 60)...)

// cr2Like: IFD0 = full-size old-style JPEG strip (with size), IFD1 =
// JPEGInterchangeFormat thumbnail, IFD2 = uncompressed strip, IFD3 = raw
// with slices.
func cr2Like(bo testimg.ByteOrder, full, thumb []byte) []byte {
	ifd0 := append(testimg.JPEGStrip(1, 96, 64, 6), testimg.ASCII(0x010F, "Canon"))
	return testimg.TIFFWithBlobs(bo, [][]byte{full, thumb, make([]byte, 48), lossless},
		testimg.IFD{Entries: ifd0, Next: 2},
		testimg.IFD{Entries: testimg.JPEGInterchange(2), Next: 3},
		testimg.IFD{Entries: []testimg.Entry{testimg.Long(0x0100, 4), testimg.Long(0x0101, 4), testimg.Short(0x0103, 1),
			testimg.BlobOffset(0x0111, 3), testimg.BlobLength(0x0117, 3)}, Next: 4},
		testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0103, 6), testimg.BlobOffset(0x0111, 4), testimg.BlobLength(0x0117, 4),
			testimg.Short(0xC640, 1, 2, 3)}},
	)
}

func TestPreviewsCR2Like(t *testing.T) {
	full, thumb := jpg(96, 64, 1), jpg(16, 12, 2)
	for _, bo := range []testimg.ByteOrder{le, be} {
		data := cr2Like(bo, full, thumb)
		// One size unknown: ordered by length, full-size first.
		wantPreviews(t, "cr2", previews(t, data), []exif.PreviewRef{ref(t, data, full, 96, 64), ref(t, data, thumb, 0, 0)})
		if f := read(t, data); f.Make != "Canon" {
			t.Errorf("Read after blobs: Make %q", f.Make)
		}
	}
}

func TestPreviewsDNG(t *testing.T) {
	big, small, thumb := jpg(64, 48, 1), jpg(32, 24, 2), jpg(8, 8, 3)
	for _, bo := range []testimg.ByteOrder{le, be} {
		data := testimg.TIFFWithBlobs(bo, [][]byte{make([]byte, 48), big, small, lossless, thumb},
			testimg.IFD{Entries: []testimg.Entry{
				testimg.Long(0x00FE, 1), testimg.Long(0x0100, 4), testimg.Long(0x0101, 4), testimg.Short(0x0103, 1),
				testimg.BlobOffset(0x0111, 1), testimg.ASCII(0x010F, "Leica"), testimg.BlobLength(0x0117, 1),
				testimg.Ptr(0x014A, 2, 3, 4),
			}},
			// small preview first in the file: ordering must be by area
			testimg.IFD{Entries: append(testimg.JPEGStrip(3, 32, 24, 7), testimg.Long(0x00FE, 1))},
			testimg.IFD{Entries: append(testimg.JPEGStrip(2, 64, 48, 7), testimg.Long(0x00FE, 1), testimg.Ptr(0x014A, 5))},
			testimg.IFD{Entries: []testimg.Entry{
				testimg.Long(0x00FE, 0), testimg.Long(0x0100, 6000), testimg.Long(0x0101, 4000), testimg.Short(0x0103, 7),
				testimg.Short(0x0106, 32803), testimg.BlobOffset(0x0111, 4), testimg.BlobLength(0x0117, 4),
			}},
			// nested SubIFD with a strip sized 8x8
			testimg.IFD{Entries: testimg.JPEGStrip(5, 8, 8, 7)},
		)
		wantPreviews(t, "dng", previews(t, data), []exif.PreviewRef{
			ref(t, data, big, 64, 48), ref(t, data, small, 32, 24), ref(t, data, thumb, 8, 8)})
	}
}

func TestPreviewsRW2(t *testing.T) {
	j := jpg(40, 30, 4)
	data := testimg.TIFFWithBlobs(nil, [][]byte{j}, testimg.IFD{Entries: []testimg.Entry{
		testimg.ASCII(0x010F, "Panasonic"), testimg.BlobData(0x002E, testimg.TypeUndefined, 1)}})
	copy(data, "IIU\x00")
	wantPreviews(t, "rw2", previews(t, data), []exif.PreviewRef{ref(t, data, j, 0, 0)})
}

// olympusMakerNote builds a new-style Olympus maker note whose
// CameraSettings IFD points at j (offsets relative to the maker note).
func olympusMakerNote(bo testimg.ByteOrder, header string, j []byte) []byte {
	ifds := testimg.IFDs(bo, uint32(len(header)), [][]byte{j},
		testimg.IFD{Entries: []testimg.Entry{testimg.Short(0x0000, 0x30), testimg.Ptr(0x2020, 2)}},
		testimg.IFD{Entries: []testimg.Entry{testimg.Long(0x0100, 1), testimg.BlobOffset(0x0101, 1), testimg.BlobLength(0x0102, 1)}},
	)
	return append([]byte(header), ifds...)
}

func TestPreviewsORF(t *testing.T) {
	j := jpg(48, 36, 5)
	for _, c := range []struct {
		name   string
		bo     testimg.ByteOrder
		header string
	}{
		{"OLYMPUS II", le, "OLYMPUS\x00II\x03\x00"},
		{"OLYMPUS MM", be, "OLYMPUS\x00MM\x00\x03"},
		{"OM SYSTEM", le, "OM SYSTEM\x00\x00\x00II\x04\x00"},
	} {
		mn := olympusMakerNote(c.bo, c.header, j)
		data := testimg.TIFFWithBlobs(le, [][]byte{mn},
			testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x010F, "OLYMPUS CORPORATION"), testimg.Ptr(0x8769, 2)}},
			testimg.IFD{Entries: []testimg.Entry{goodDate, testimg.BlobData(0x927C, testimg.TypeUndefined, 1)}},
		)
		copy(data, "IIRO")
		wantPreviews(t, c.name, previews(t, data), []exif.PreviewRef{ref(t, data, j, 0, 0)})
		if f := read(t, data); f.DateTimeOriginal != "2015:06:07 08:09:10" {
			t.Errorf("%s: Read: %+v", c.name, f)
		}
	}
}

func TestPreviewsCR3(t *testing.T) {
	full, prvw := jpg(96, 64, 6), jpg(32, 24, 7)
	cmt1 := testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x010F, "Canon"), testimg.ASCII(0x0110, "Canon EOS R7")}})
	for _, o := range []testimg.CR3Options{
		{CMT1: cmt1, Preview: prvw, PreviewW: 32, PreviewH: 24, FullSize: full, Width: 96, Height: 64},
		{CMT1: cmt1, Preview: prvw, PreviewW: 32, PreviewH: 24, FullSize: full, Width: 96, Height: 64, RawFirst: true, Co64: true},
		{CMT1: cmt1, Preview: prvw, PreviewW: 32, PreviewH: 24, FullSize: full, Width: 96, Height: 64, RawFirst: true, SampleSize32: true},
	} {
		data := testimg.CR3(o)
		wantPreviews(t, "cr3", previews(t, data), []exif.PreviewRef{ref(t, data, full, 96, 64), ref(t, data, prvw, 32, 24)})
		if f := read(t, data); f.Model != "Canon EOS R7" {
			t.Errorf("Read: %+v", f)
		}
	}
	// Only the preview, or only the full-size JPEG.
	data := testimg.CR3(testimg.CR3Options{CMT1: cmt1, Preview: prvw, PreviewW: 32, PreviewH: 24})
	wantPreviews(t, "prvw only", previews(t, data), []exif.PreviewRef{ref(t, data, prvw, 32, 24)})
	data = testimg.CR3(testimg.CR3Options{CMT1: cmt1, FullSize: full, Width: 96, Height: 64, RawFirst: true})
	wantPreviews(t, "full only", previews(t, data), []exif.PreviewRef{ref(t, data, full, 96, 64)})
	data = testimg.CR3(testimg.CR3Options{CMT1: cmt1, RawFirst: true})
	wantPreviews(t, "none", previews(t, data), nil)

	// A different header before the JPEG (no PRVW box at all): found by
	// scanning; length runs to the end of the uuid box, size unknown.
	odd := testimg.Box("uuid", testimg.CanonPreviewUUID, make([]byte, 21), prvw)
	data = append(testimg.CR3(testimg.CR3Options{CMT1: cmt1}), odd...)
	wantPreviews(t, "odd prvw", previews(t, data), []exif.PreviewRef{ref(t, data, prvw, 0, 0)})
}

func TestPreviewsRAF(t *testing.T) {
	j := jpg(40, 30, 8)
	data := append(testimg.RAF(j), make([]byte, 100)...)
	wantPreviews(t, "raf", previews(t, data), []exif.PreviewRef{ref(t, data, j, 0, 0)})
}

func TestPreviewsNone(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty": nil,
		"text":  []byte("hello world, not an image"),
		"jpeg":  testimg.JPEG(testimg.Options{Make: "Canon"}),
		"png":   testimg.PNG(4, 4, 1),
		"tiff":  testimg.TIFF(nil, testimg.IFD{Entries: ifd0With()}),
		"mp4":   append(testimg.Box("ftyp", []byte("isom\x00\x00\x00\x00isom")), testimg.Box("moov")...),
	} {
		p, err := exif.Previews(bytes.NewReader(data), int64(len(data)))
		if err != nil || p != nil {
			t.Errorf("%s: %v %v", name, p, err)
		}
	}
}

func TestPreviewsAtMost8(t *testing.T) {
	var blobs [][]byte
	ifds := []testimg.IFD{{}}
	var subs []int
	for i := 0; i < 12; i++ {
		blobs = append(blobs, jpg(8+i, 8+i, byte(i)))
		ifds = append(ifds, testimg.IFD{Entries: testimg.JPEGStrip(i+1, 8+i, 8+i, 7)})
		subs = append(subs, i+2)
	}
	ifds[0].Entries = []testimg.Entry{testimg.Ptr(0x014A, subs...)}
	data := testimg.TIFFWithBlobs(nil, blobs, ifds...)
	got := previews(t, data)
	var want []exif.PreviewRef
	for i := 11; i >= 4; i-- {
		want = append(want, ref(t, data, blobs[i], 8+i, 8+i))
	}
	wantPreviews(t, "12 previews", got, want)
}

// ---- hostile input ---------------------------------------------------------

func checkPreviewsBounded(t *testing.T, name string, r interface {
	ReadAt([]byte, int64) (int, error)
}, size int64) []exif.PreviewRef {
	t.Helper()
	var p []exif.PreviewRef
	var err error
	d, alloc := cost(func() { p, err = exif.Previews(r, size) })
	t.Logf("%s: %v, %d bytes allocated, %d previews", name, d, alloc, len(p))
	if err != nil {
		t.Errorf("%s: %v", name, err)
	}
	if d > maxDuration || alloc > maxAlloc {
		t.Errorf("%s: took %v and allocated %d bytes", name, d, alloc)
	}
	if len(p) > 8 {
		t.Errorf("%s: %d previews", name, len(p))
	}
	for _, q := range p {
		if q.Offset < 0 || q.Length <= 0 || q.Offset+q.Length > size {
			t.Errorf("%s: preview %+v outside file of %d bytes", name, q, size)
		}
	}
	return p
}

func TestPreviewsHostile(t *testing.T) {
	j := jpg(16, 16, 9)
	chain := make([]testimg.IFD, 40)
	for i := range chain {
		chain[i] = testimg.IFD{Entries: testimg.JPEGInterchange(1), Next: i + 2}
	}
	chain[39].Next = 1
	manyBoxes := testimg.Box("ftyp", []byte("crx "))
	for i := 0; i < 10000; i++ {
		manyBoxes = append(manyBoxes, testimg.Box("free")...)
	}
	sizedBox := func(typ string, size uint32, payload ...byte) []byte {
		return append(append(binary.BigEndian.AppendUint32(nil, size), typ...), payload...)
	}
	cr3 := testimg.CR3(testimg.CR3Options{FullSize: j, Width: 16, Height: 16})
	ftypCR3 := testimg.Box("ftyp", []byte("crx \x00\x00\x00\x01crx isom"))
	// A PRVW box whose JPEG starts beyond the 64 KiB scan window.
	farPRVW := append(append([]byte{}, ftypCR3...), testimg.Box("uuid", testimg.CanonPreviewUUID, make([]byte, 70000), j)...)
	trak := func(off uint64, n uint32) []byte {
		return testimg.Box("moov", testimg.Box("trak", testimg.Box("mdia", testimg.Box("minf", testimg.Box("stbl",
			testimg.FullBox("stsz", 0, 0, []byte{0, 0, 0, 0, 0, 0, 0, 1}, binary.BigEndian.AppendUint32(nil, n)),
			testimg.FullBox("co64", 0, 0, []byte{0, 0, 0, 1}, binary.BigEndian.AppendUint64(nil, off)))))))
	}
	deep := testimg.Box("stbl")
	for _, typ := range []string{"minf", "mdia", "trak", "moov", "moov", "moov", "moov"} {
		deep = testimg.Box(typ, deep)
	}

	cases := map[string][]byte{
		"subifd loop": testimg.TIFFWithBlobs(nil, [][]byte{j},
			testimg.IFD{Entries: []testimg.Entry{testimg.Ptr(0x014A, 2)}},
			testimg.IFD{Entries: append(testimg.JPEGStrip(1, 16, 16, 7), testimg.Ptr(0x014A, 1, 2, 3))},
			testimg.IFD{Entries: []testimg.Entry{testimg.Ptr(0x014A, 2, 3)}, Next: 1}),
		"ifd chain loop of 40": testimg.TIFFWithBlobs(nil, [][]byte{j}, chain...),
		"preview range past EOF": testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
			testimg.Long(0x0201, 0xFFFFFF00), testimg.Long(0x0202, 0xFFFF),
			testimg.Short(0x0103, 7), testimg.Long(0x0111, 8), testimg.Long(0x0117, 0xFFFFFFFF),
			{Tag: 0x002E, Type: testimg.TypeUndefined, Count: 0x7FFFFFFF, SetCount: true, Value: u32(8)},
		}}),
		"preview not jpeg": testimg.TIFFWithBlobs(nil, [][]byte{make([]byte, 64)}, testimg.IFD{Entries: testimg.JPEGInterchange(1)}),
		"strip count 2": testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
			testimg.Short(0x0103, 7), testimg.Long(0x0111, 8, 8), testimg.Long(0x0117, 4, 4)}}),
		"orf makernote garbage": testimg.TIFFWithBlobs(nil, [][]byte{append([]byte("OLYMPUS\x00II\x03\x00\xff\xff"), bytes.Repeat([]byte{0xFF}, 64)...)},
			testimg.IFD{Entries: []testimg.Entry{testimg.Ptr(0x8769, 2)}},
			testimg.IFD{Entries: []testimg.Entry{testimg.BlobData(0x927C, testimg.TypeUndefined, 1)}}),
		"orf makernote past EOF": testimg.TIFF(nil,
			testimg.IFD{Entries: []testimg.Entry{testimg.Ptr(0x8769, 2)}},
			testimg.IFD{Entries: []testimg.Entry{{Tag: 0x927C, Type: testimg.TypeUndefined, Count: 0xFFFFFFF0, SetCount: true, Value: u32(0x10)}}}),
		"cr3 box larger than file":     append(append([]byte{}, ftypCR3...), sizedBox("moov", 0xFFFFFFF0)...),
		"cr3 box smaller than header":  append(append([]byte{}, ftypCR3...), sizedBox("moov", 4, make([]byte, 64)...)...),
		"cr3 zero-size boxes":          append(append([]byte{}, ftypCR3...), sizedBox("moov", 0, sizedBox("trak", 0, sizedBox("mdia", 0)...)...)...),
		"cr3 largesize huge":           append(append([]byte{}, ftypCR3...), append(sizedBox("moov", 1), binary.BigEndian.AppendUint64(nil, 1<<62)...)...),
		"cr3 many boxes":               manyBoxes,
		"cr3 prvw beyond scan window":  farPRVW,
		"cr3 sample past EOF":          append(append([]byte{}, ftypCR3...), trak(1<<62, 100)...),
		"cr3 sample length past EOF":   append(append([]byte{}, ftypCR3...), trak(8, 0xFFFFFFFF)...),
		"cr3 deep nesting":             append(append([]byte{}, ftypCR3...), deep...),
		"cr3 truncated":                cr3[:len(cr3)-len(j)/2],
		"raf offset past EOF":          testimg.RAFAt(j, 0xFFFFFF00, 0xFFFF),
		"raf length past EOF":          testimg.RAFAt(j, 160, 0xFFFFFFFF),
		"raf not jpeg":                 testimg.RAFAt(make([]byte, 64), 160, 64),
		"tiff ifd0 0xffff entries":     append([]byte("II*\x00\x08\x00\x00\x00\xff\xff"), make([]byte, 100)...),
		"tiff subifd nesting of 10":    nestedSubIFDs(j, 10),
		"tiff 32 subifds of 1024 refs": manySubIFDRefs(j),
		"truncated header ftyp":        []byte("\x00\x00\x00\x10ftyp"),
		"jpeg interchange count overflow": testimg.TIFF(nil, testimg.IFD{Entries: []testimg.Entry{
			{Tag: 0x0201, Type: testimg.TypeLong, Count: 0x80000000, SetCount: true, Value: u32(8)}, testimg.Long(0x0202, 4)}}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := checkPreviewsBounded(t, name, bytes.NewReader(data), int64(len(data)))
			switch name {
			case "subifd loop", "ifd chain loop of 40":
				if len(p) != 1 || p[0] != ref(t, data, j, p[0].Width, p[0].Height) {
					t.Errorf("got %+v, want the one JPEG", p)
				}
			case "cr3 truncated", "tiff subifd nesting of 10", "tiff 32 subifds of 1024 refs":
			default:
				if len(p) != 0 {
					t.Errorf("got %+v, want none", p)
				}
			}
			// Read must stay bounded on the same input.
			_, _, d, alloc := measure(bytes.NewReader(data), int64(len(data)))
			if d > maxDuration || alloc > maxAlloc {
				t.Errorf("Read took %v and allocated %d bytes", d, alloc)
			}
		})
	}
}

func nestedSubIFDs(j []byte, n int) []byte {
	ifds := make([]testimg.IFD, n)
	for i := range ifds {
		ifds[i] = testimg.IFD{Entries: append(testimg.JPEGInterchange(1), testimg.Ptr(0x014A, (i+1)%n+1))}
	}
	return testimg.TIFFWithBlobs(nil, [][]byte{j}, ifds...)
}

func manySubIFDRefs(j []byte) []byte {
	ptrs := make([]int, 1024)
	for i := range ptrs {
		ptrs[i] = 2
	}
	return testimg.TIFFWithBlobs(nil, [][]byte{j},
		testimg.IFD{Entries: []testimg.Entry{testimg.Ptr(0x014A, ptrs...)}},
		testimg.IFD{Entries: append(testimg.JPEGInterchange(1), testimg.Ptr(0x014A, ptrs...))})
}

// A large file is never read beyond its headers.
func TestPreviewsLargeSparseFile(t *testing.T) {
	const size = 64 << 20
	full, thumb := jpg(96, 64, 1), jpg(16, 12, 2)
	for name, data := range map[string][]byte{
		"big.cr2":  cr2Like(le, full, thumb),
		"big.cr3":  testimg.CR3(testimg.CR3Options{Preview: thumb, PreviewW: 16, PreviewH: 12, FullSize: full, Width: 96, Height: 64}),
		"big.heic": testimg.HEIF(testimg.HEIFOptions{Exif: appleTIFF(0), Thumb: thumb, ThumbW: 16, ThumbH: 12}),
	} {
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
		got := checkPreviewsBounded(t, name, fh, size)
		fh.Close()
		if len(got) == 0 {
			t.Errorf("%s: no previews", name)
		}
	}
}
