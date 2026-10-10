// Package testimg builds small JPEG, PNG and TIFF files, optionally with
// synthetic EXIF data, for use in tests. The TIFF builder (TIFF, Entry, IFD)
// can also produce deliberately malformed structures for robustness tests.
package testimg

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"sort"
	"time"
)

// Options controls the generated image and its EXIF block.
type Options struct {
	Width, Height int
	Seed          byte // varies the pixel data so different seeds give different bytes
	NoExif        bool

	Make, Model, Lens string
	DateTime          time.Time // wall-clock time written to DateTimeOriginal and DateTime; zero omits both
	DateTimeDigitized time.Time // wall-clock time written to DateTimeDigitized; zero omits it
	// OffsetTime tags (0x9011, 0x9012, 0x9010), e.g. "+02:00"; "" omits them.
	OffsetTimeOriginal, OffsetTimeDigitized, OffsetTime string
	Orientation                                         int
	GPS                                                 bool
	Lat, Lon                                            float64
	Altitude                                            float64

	BigEndian     bool // write the TIFF structure inside the Exif APP1 big-endian
	XMPBeforeExif bool // insert an XMP APP1 segment before the Exif APP1
}

func fill(img *image.RGBA, seed byte) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.Set(x, y, color.RGBA{R: seed + byte(x*7), G: byte(y*5) ^ seed, B: seed * 3, A: 255})
		}
	}
}

// JPEG returns a JPEG file with the given options.
func JPEG(o Options) []byte {
	if o.Width == 0 {
		o.Width = 16
	}
	if o.Height == 0 {
		o.Height = 16
	}
	img := image.NewRGBA(image.Rect(0, 0, o.Width, o.Height))
	fill(img, o.Seed)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
		panic(err)
	}
	b := buf.Bytes()
	if o.NoExif {
		return b
	}
	var segs [][]byte
	if o.XMPBeforeExif {
		segs = append(segs, XMPSegment())
	}
	segs = append(segs, ExifSegment(exifTIFF(o)))
	return InsertSegments(b, segs...)
}

// PNG returns a PNG file of the given size.
func PNG(w, h int, seed byte) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, seed)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// BareJPEG returns a small valid JPEG without any APP segments.
func BareJPEG() []byte { return JPEG(Options{Width: 16, Height: 16, NoExif: true}) }

// InsertSegments inserts complete JPEG segments directly after the SOI
// marker of jpg, in the given order.
func InsertSegments(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

// APP1 returns an APP1 segment (marker, length, payload).
func APP1(payload []byte) []byte {
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	return append(seg, payload...)
}

// ExifSegment wraps a TIFF structure in an "Exif\0\0" APP1 segment.
func ExifSegment(tiff []byte) []byte {
	return APP1(append([]byte("Exif\x00\x00"), tiff...))
}

// XMPSegment returns a minimal XMP APP1 segment.
func XMPSegment() []byte {
	return APP1([]byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta xmlns:x='adobe:ns:meta/'/>"))
}

// JPEGWithTIFF returns a small real JPEG carrying tiff in an Exif APP1,
// preceded by the extra segments given.
func JPEGWithTIFF(tiff []byte, before ...[]byte) []byte {
	return InsertSegments(BareJPEG(), append(append([][]byte{}, before...), ExifSegment(tiff))...)
}

// TIFF field types.
const (
	TypeByte      = 1
	TypeASCII     = 2
	TypeShort     = 3
	TypeLong      = 4
	TypeRational  = 5
	TypeUndefined = 7
)

// ByteOrder is a byte order the TIFF builder can write
// (binary.LittleEndian or binary.BigEndian).
type ByteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// Entry is one IFD entry for the TIFF builder.
type Entry struct {
	Tag, Type uint16
	// Count overrides the element count derived from the value when
	// SetCount is true (used to build hostile entries).
	Count    uint32
	SetCount bool
	Data     []byte      // BYTE/ASCII/UNDEFINED bytes
	Ints     []uint32    // SHORT/LONG values
	Rats     [][2]uint32 // RATIONAL values
	Ptrs     []int       // LONG values that are offsets of IFDs (1-based index into the IFD list)
	// Value, when non-nil, is written verbatim as the 4-byte value/offset
	// field and no out-of-line data is emitted.
	Value *uint32
	// Blob references (1-based indexes into the blobs given to
	// TIFFWithBlobs or IFDs). OffsetOf writes the blob's offset as one
	// LONG; LengthOf writes its length as one LONG; DataOf makes the blob
	// the entry's out-of-line value (count = blob length unless SetCount).
	OffsetOf, LengthOf, DataOf int
}

// IFD is one image file directory for the TIFF builder.
type IFD struct {
	Entries []Entry
	Next    int // >0: next-IFD pointer to IFD Next (1-based); 0: none
}

// ASCII returns an ASCII entry with a terminating NUL.
func ASCII(tag uint16, s string) Entry {
	return Entry{Tag: tag, Type: TypeASCII, Data: append([]byte(s), 0)}
}

// Short returns a SHORT entry.
func Short(tag uint16, v ...uint32) Entry { return Entry{Tag: tag, Type: TypeShort, Ints: v} }

// Long returns a LONG entry.
func Long(tag uint16, v ...uint32) Entry { return Entry{Tag: tag, Type: TypeLong, Ints: v} }

// Byte returns a BYTE entry.
func Byte(tag uint16, v ...byte) Entry { return Entry{Tag: tag, Type: TypeByte, Data: v} }

// Undefined returns an UNDEFINED entry.
func Undefined(tag uint16, b []byte) Entry { return Entry{Tag: tag, Type: TypeUndefined, Data: b} }

// Rational returns a RATIONAL entry.
func Rational(tag uint16, v ...[2]uint32) Entry { return Entry{Tag: tag, Type: TypeRational, Rats: v} }

// Ptr returns a LONG entry pointing at the given IFDs (1-based indexes).
func Ptr(tag uint16, ifds ...int) Entry { return Entry{Tag: tag, Type: TypeLong, Ptrs: ifds} }

func (e Entry) count() uint32 {
	if e.SetCount {
		return e.Count
	}
	switch {
	case e.Ptrs != nil:
		return uint32(len(e.Ptrs))
	case e.Rats != nil:
		return uint32(len(e.Rats))
	case e.Ints != nil:
		return uint32(len(e.Ints))
	}
	return uint32(len(e.Data))
}

func (e Entry) encode(bo ByteOrder, offs []uint32) []byte {
	var b []byte
	switch {
	case e.Ptrs != nil:
		for _, p := range e.Ptrs {
			b = bo.AppendUint32(b, offs[p-1])
		}
	case e.Rats != nil:
		for _, r := range e.Rats {
			b = bo.AppendUint32(b, r[0])
			b = bo.AppendUint32(b, r[1])
		}
	case e.Ints != nil:
		for _, v := range e.Ints {
			if e.Type == TypeShort {
				b = bo.AppendUint16(b, uint16(v))
			} else {
				b = bo.AppendUint32(b, v)
			}
		}
	default:
		b = e.Data
	}
	return b
}

func (e Entry) dataLen() uint32 {
	if e.Value != nil || e.OffsetOf > 0 || e.LengthOf > 0 || e.DataOf > 0 {
		return 0
	}
	n := uint32(len(e.encode(binary.LittleEndian, make([]uint32, maxPtr(e)))))
	if n <= 4 {
		return 0
	}
	return n + n%2
}

func maxPtr(e Entry) int {
	m := 0
	for _, p := range e.Ptrs {
		if p > m {
			m = p
		}
	}
	return m
}

func ifdLen(d IFD) uint32 {
	n := uint32(2 + 12*len(d.Entries) + 4)
	for _, e := range d.Entries {
		n += e.dataLen()
	}
	return n
}

// TIFF serialises ifds as a TIFF structure in byte order bo (little-endian
// when nil). The first IFD is IFD0; IFDs are laid out in order, each
// followed by its out-of-line values. Entries are written in the order given.
func TIFF(bo ByteOrder, ifds ...IFD) []byte { return TIFFWithBlobs(bo, nil, ifds...) }

// TIFFWithBlobs is TIFF followed by blobs (each starting at an even offset)
// that entries reference with OffsetOf, LengthOf and DataOf.
func TIFFWithBlobs(bo ByteOrder, blobs [][]byte, ifds ...IFD) []byte {
	if bo == nil {
		bo = binary.LittleEndian
	}
	var out []byte
	if bo == binary.BigEndian {
		out = []byte{'M', 'M', 0, 0x2A}
	} else {
		out = []byte{'I', 'I', 0x2A, 0}
	}
	if len(ifds) > 0 {
		out = bo.AppendUint32(out, 8)
	} else {
		out = bo.AppendUint32(out, 0)
	}
	return append(out, IFDs(bo, 8, blobs, ifds...)...)
}

// IFDs serialises ifds and then blobs without a TIFF header, with every
// offset computed as if the first returned byte were at offset base of the
// enclosing structure. It builds maker notes whose offsets are relative to
// a point other than the TIFF header.
func IFDs(bo ByteOrder, base uint32, blobs [][]byte, ifds ...IFD) []byte {
	if bo == nil {
		bo = binary.LittleEndian
	}
	offs := make([]uint32, len(ifds))
	o := base
	for i, d := range ifds {
		offs[i] = o
		o += ifdLen(d)
	}
	blobOffs := make([]uint32, len(blobs))
	for i, b := range blobs {
		o += o % 2
		blobOffs[i] = o
		o += uint32(len(b))
	}
	var out []byte
	for i, d := range ifds {
		out = bo.AppendUint16(out, uint16(len(d.Entries)))
		dataOff := offs[i] + uint32(2+12*len(d.Entries)+4)
		var data []byte
		for _, e := range d.Entries {
			out = bo.AppendUint16(out, e.Tag)
			out = bo.AppendUint16(out, e.Type)
			switch {
			case e.Value != nil:
				out = bo.AppendUint32(out, e.count())
				out = bo.AppendUint32(out, *e.Value)
				continue
			case e.OffsetOf > 0, e.LengthOf > 0:
				n := uint32(1)
				if e.SetCount {
					n = e.Count
				}
				out = bo.AppendUint32(out, n)
				if e.OffsetOf > 0 {
					out = bo.AppendUint32(out, blobOffs[e.OffsetOf-1])
				} else {
					out = bo.AppendUint32(out, uint32(len(blobs[e.LengthOf-1])))
				}
				continue
			case e.DataOf > 0:
				n := uint32(len(blobs[e.DataOf-1]))
				if e.SetCount {
					n = e.Count
				}
				out = bo.AppendUint32(out, n)
				out = bo.AppendUint32(out, blobOffs[e.DataOf-1])
				continue
			}
			out = bo.AppendUint32(out, e.count())
			v := e.encode(bo, offs)
			if len(v) <= 4 {
				pad := make([]byte, 4)
				copy(pad, v)
				out = append(out, pad...)
				continue
			}
			out = bo.AppendUint32(out, dataOff+uint32(len(data)))
			data = append(data, v...)
			if len(v)%2 == 1 {
				data = append(data, 0)
			}
		}
		if d.Next > 0 {
			out = bo.AppendUint32(out, offs[d.Next-1])
		} else {
			out = bo.AppendUint32(out, 0)
		}
		out = append(out, data...)
	}
	for i, b := range blobs {
		for base+uint32(len(out)) < blobOffs[i] {
			out = append(out, 0)
		}
		out = append(out, b...)
	}
	return out
}

// BlobOffset returns a LONG entry holding the offset of blob n (1-based).
func BlobOffset(tag uint16, n int) Entry { return Entry{Tag: tag, Type: TypeLong, OffsetOf: n} }

// BlobLength returns a LONG entry holding the length of blob n (1-based).
func BlobLength(tag uint16, n int) Entry { return Entry{Tag: tag, Type: TypeLong, LengthOf: n} }

// BlobData returns an entry of type typ whose out-of-line value is blob n.
func BlobData(tag, typ uint16, n int) Entry { return Entry{Tag: tag, Type: typ, DataOf: n} }

// JPEGInterchange returns JPEGInterchangeFormat (0x0201) and
// JPEGInterchangeFormatLength (0x0202) entries pointing at blob n.
func JPEGInterchange(n int) []Entry {
	return []Entry{BlobOffset(0x0201, n), BlobLength(0x0202, n)}
}

// JPEGStrip returns the entries of a single-strip IFD of the given size
// whose strip is blob n, with Compression comp (6 = old JPEG, 7 = JPEG)
// and PhotometricInterpretation 6 (YCbCr), sorted by tag.
func JPEGStrip(n, width, height int, comp uint32) []Entry {
	return []Entry{
		Long(0x0100, uint32(width)), Long(0x0101, uint32(height)),
		Short(0x0103, comp), Short(0x0106, 6),
		BlobOffset(0x0111, n), BlobLength(0x0117, n),
	}
}

func dms(v float64) [3][2]uint32 {
	v = math.Abs(v)
	deg := math.Floor(v)
	minF := (v - deg) * 60
	min := math.Floor(minF)
	sec := (minF - min) * 60
	return [3][2]uint32{{uint32(deg), 1}, {uint32(min), 1}, {uint32(math.Round(sec * 10000)), 10000}}
}

func sortEntries(es []Entry) []Entry {
	sort.SliceStable(es, func(i, j int) bool { return es[i].Tag < es[j].Tag })
	return es
}

// exifTIFF builds the TIFF structure for o: IFD0, Exif IFD and GPS IFD.
func exifTIFF(o Options) []byte {
	const layout = "2006:01:02 15:04:05"
	ifd0 := []Entry{}
	if o.Make != "" {
		ifd0 = append(ifd0, ASCII(0x010F, o.Make))
	}
	if o.Model != "" {
		ifd0 = append(ifd0, ASCII(0x0110, o.Model))
	}
	if o.Orientation > 0 {
		ifd0 = append(ifd0, Short(0x0112, uint32(o.Orientation)))
	}
	exifIFD := []Entry{Undefined(0x9000, []byte("0232")),
		Long(0xA002, uint32(o.Width)), Long(0xA003, uint32(o.Height))}
	if !o.DateTime.IsZero() {
		s := o.DateTime.Format(layout)
		ifd0 = append(ifd0, ASCII(0x0132, s))
		exifIFD = append(exifIFD, ASCII(0x9003, s))
	}
	if !o.DateTimeDigitized.IsZero() {
		exifIFD = append(exifIFD, ASCII(0x9004, o.DateTimeDigitized.Format(layout)))
	}
	for _, ot := range []struct {
		tag uint16
		v   string
	}{{0x9010, o.OffsetTime}, {0x9011, o.OffsetTimeOriginal}, {0x9012, o.OffsetTimeDigitized}} {
		if ot.v != "" {
			exifIFD = append(exifIFD, ASCII(ot.tag, ot.v))
		}
	}
	if o.Lens != "" {
		exifIFD = append(exifIFD, ASCII(0xA434, o.Lens))
	}
	ifds := []IFD{{}, {Entries: sortEntries(exifIFD)}}
	ifd0 = append(ifd0, Ptr(0x8769, 2))
	if o.GPS {
		latRef, lonRef := "N", "E"
		if o.Lat < 0 {
			latRef = "S"
		}
		if o.Lon < 0 {
			lonRef = "W"
		}
		lat, lon := dms(o.Lat), dms(o.Lon)
		ifds = append(ifds, IFD{Entries: []Entry{
			Byte(0x0000, 2, 3, 0, 0),
			ASCII(0x0001, latRef), Rational(0x0002, lat[0], lat[1], lat[2]),
			ASCII(0x0003, lonRef), Rational(0x0004, lon[0], lon[1], lon[2]),
			Byte(0x0005, 0), Rational(0x0006, [2]uint32{uint32(math.Round(o.Altitude * 100)), 100}),
		}})
		ifd0 = append(ifd0, Ptr(0x8825, 3))
	}
	ifds[0].Entries = sortEntries(ifd0)
	var bo ByteOrder = binary.LittleEndian
	if o.BigEndian {
		bo = binary.BigEndian
	}
	return TIFF(bo, ifds...)
}

// ---- ISO BMFF containers ---------------------------------------------------

// Box returns an ISO BMFF box of type typ (4 characters) holding the
// concatenated payloads.
func Box(typ string, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	return append(append(b, typ...), body...)
}

// FullBox returns a box whose payload starts with version and 24-bit flags.
func FullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	vf := binary.BigEndian.AppendUint32(nil, uint32(version)<<24|flags&0xFFFFFF)
	return Box(typ, append([][]byte{vf}, payload...)...)
}

func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func be64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// CanonUUID is the uuid of the CR3 metadata box inside moov.
var CanonUUID = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}

// CanonPreviewUUID is the uuid of the top-level CR3 box holding PRVW.
var CanonPreviewUUID = []byte{0xea, 0xf4, 0x2b, 0x5e, 0x1c, 0x98, 0x4b, 0x88, 0xb9, 0xfb, 0xb7, 0xdc, 0x40, 0x6e, 0x4d, 0x16}

// CR3Options describes a synthetic Canon CR3.
type CR3Options struct {
	CMT1 []byte // TIFF for the CMT1 box (IFD0); nil omits it

	Preview      []byte // JPEG in the PRVW box; nil omits the preview uuid box
	PreviewW     int    // PRVW header width/height
	PreviewH     int
	FullSize     []byte // JPEG sample of a CRAW track; nil omits the track
	Width        int    // CRAW sample entry width/height
	Height       int
	RawFirst     bool // add a track whose sample is not a JPEG before the JPEG track
	Co64         bool // use co64 instead of stco for chunk offsets
	SampleSize32 bool // give the sample size in stsz.sample_size instead of the entry table
}

func cr3Trak(w, h int, sample int, off uint64, co64, fixed bool) []byte {
	entry := append(make([]byte, 6), be16(1)...) // reserved, data_reference_index
	entry = append(entry, make([]byte, 16)...)
	entry = append(entry, be16(uint16(w))...)
	entry = append(entry, be16(uint16(h))...)
	entry = append(entry, make([]byte, 50)...)
	var stsz []byte
	if fixed {
		stsz = FullBox("stsz", 0, 0, be32(uint32(sample)), be32(1))
	} else {
		stsz = FullBox("stsz", 0, 0, be32(0), be32(1), be32(uint32(sample)))
	}
	co := FullBox("stco", 0, 0, be32(1), be32(uint32(off)))
	if co64 {
		co = FullBox("co64", 0, 0, be32(1), be64(off))
	}
	return Box("trak",
		FullBox("tkhd", 0, 3, make([]byte, 80)),
		Box("mdia",
			FullBox("mdhd", 0, 0, make([]byte, 20)),
			FullBox("hdlr", 0, 0, be32(0), []byte("vide"), make([]byte, 13)),
			Box("minf",
				FullBox("vmhd", 0, 1, make([]byte, 8)),
				Box("stbl",
					FullBox("stsd", 0, 0, be32(1), Box("CRAW", entry)),
					FullBox("stts", 0, 0, be32(1), be32(1), be32(1)),
					FullBox("stsc", 0, 0, be32(1), be32(1), be32(1), be32(1)),
					stsz, co))))
}

// CR3 builds a minimal CR3: ftyp "crx ", moov (mvhd, the Canon metadata
// uuid with CMT1, then the tracks), the preview uuid box with PRVW (8 bytes
// of unknown data, then the PRVW box: 4 zero bytes, u16 1, width, height,
// u16 1, u32 JPEG length, JPEG), and mdat with the track samples.
func CR3(o CR3Options) []byte {
	build := func(mdatStart uint64) ([]byte, uint64) {
		moov := [][]byte{FullBox("mvhd", 0, 0, make([]byte, 96))}
		meta := [][]byte{CanonUUID, Box("CNCV", []byte("CanonCR3_001/01.09.00/00.00.00"))}
		if o.CMT1 != nil {
			meta = append(meta, Box("CMT1", o.CMT1))
		}
		moov = append(moov, Box("uuid", meta...))
		raw := []byte("\x00\x00not a jpeg, raw data")
		pos := mdatStart
		var mdat []byte
		if o.RawFirst {
			moov = append(moov, cr3Trak(o.Width, o.Height, len(raw), pos, o.Co64, o.SampleSize32))
			mdat = append(mdat, raw...)
			pos += uint64(len(raw))
		}
		if o.FullSize != nil {
			moov = append(moov, cr3Trak(o.Width, o.Height, len(o.FullSize), pos, o.Co64, o.SampleSize32))
			mdat = append(mdat, o.FullSize...)
		}
		out := Box("ftyp", []byte("crx \x00\x00\x00\x01crx isom"))
		out = append(out, Box("moov", moov...)...)
		if o.Preview != nil {
			prvw := Box("PRVW", be32(0), be16(1), be16(uint16(o.PreviewW)), be16(uint16(o.PreviewH)),
				be16(1), be32(uint32(len(o.Preview))), o.Preview)
			out = append(out, Box("uuid", CanonPreviewUUID, make([]byte, 8), prvw)...)
		}
		start := uint64(len(out) + 8)
		return append(out, Box("mdat", mdat)...), start
	}
	_, start := build(0)
	out, _ := build(start)
	return out
}

// RAF returns a minimal Fujifilm RAF whose header points at jpg, stored
// right after the 160-byte header.
func RAF(jpg []byte) []byte { return RAFAt(jpg, 160, uint32(len(jpg))) }

// RAFAt is RAF with explicit header offset/length values (for hostile tests).
func RAFAt(jpg []byte, off, n uint32) []byte {
	h := make([]byte, 160)
	copy(h, "FUJIFILMCCD-RAW 0201FF383501")
	binary.BigEndian.PutUint32(h[84:], off)
	binary.BigEndian.PutUint32(h[88:], n)
	return append(h, jpg...)
}

// HEIFTransform is one transformative property of the primary item, in
// association order: irot with Value = angle (0..3, units of 90 degrees
// counter-clockwise) or, when Mirror, imir with Value = axis.
type HEIFTransform struct {
	Mirror bool
	Value  int
}

// HEIFOptions describes a synthetic HEIF/AVIF file. Its pixel data is fake;
// only the container structure is real.
type HEIFOptions struct {
	Brand      string          // major brand, default "heic"
	Exif       []byte          // TIFF structure of the Exif item; nil omits the item
	ExifPrefix bool            // payload is offset 6, "Exif\0\0", TIFF (as Apple writes it) instead of offset 0, TIFF
	NoCdsc     bool            // omit the cdsc reference from the Exif item to the primary item
	Transforms []HEIFTransform // irot/imir properties of the primary item
	Width      int             // primary ispe
	Height     int

	Thumb     []byte // data of a thmb item referencing the primary; nil omits it
	ThumbType string // item type of the thumbnail, default "jpeg"
	ThumbW    int    // thumbnail ispe
	ThumbH    int

	Large          bool // 32-bit item IDs (>= 0x10001): infe v3, iloc v2, pitm/iref/ipma v1, 16-bit property indexes
	IlocVersion    int  // 0 or 1 (2 when Large); default 1
	OffsetSize     int  // iloc offset_size and length_size: 4 (default) or 8
	BaseOffsetSize int  // iloc base_offset_size: 0 (default, absolute extents), 4 or 8 (base = mdat payload)
	ExtraItems     int  // dummy hvc1 items listed before the real ones

	// Hostile overrides.
	ClaimItems  uint32     // when > 0, the entry/item count written in iinf, iloc and ipma
	ExifExtent  *[2]uint64 // absolute offset/length written for the Exif item
	ThumbExtent *[2]uint64
}

// HEIF builds ftyp + meta (hdlr, pitm, iinf, iref, iloc, iprp with ipco and
// ipma) + mdat. Item 1 is the primary (hvc1, or av01 for AVIF brands),
// item 2 the Exif item and item 3 the thumbnail (IDs offset by 0x10000 when
// Large).
func HEIF(o HEIFOptions) []byte {
	if o.Brand == "" {
		o.Brand = "heic"
	}
	if o.ThumbType == "" {
		o.ThumbType = "jpeg"
	}
	if o.IlocVersion == 0 && !o.Large {
		o.IlocVersion = 1
	}
	if o.Large {
		o.IlocVersion = 2
	}
	if o.OffsetSize == 0 {
		o.OffsetSize = 4
	}
	var idBase uint32 = 0
	if o.Large {
		idBase = 0x10000
	}
	idw := func(v uint32) []byte { // item ID in pitm/iref/ipma/iloc width
		if o.Large {
			return be32(v)
		}
		return be16(uint16(v))
	}
	sized := func(n int, v uint64) []byte {
		switch n {
		case 4:
			return be32(uint32(v))
		case 8:
			return be64(v)
		}
		return nil
	}
	primType := "hvc1"
	if o.Brand == "avif" || o.Brand == "avis" {
		primType = "av01"
	}
	prim, exifID, thumbID := idBase+1, idBase+2, idBase+3

	var exifPayload []byte
	if o.Exif != nil {
		if o.ExifPrefix {
			exifPayload = append(append(be32(6), "Exif\x00\x00"...), o.Exif...)
		} else {
			exifPayload = append(be32(0), o.Exif...)
		}
	}
	primData := []byte("fake coded image data")

	type item struct {
		id   uint32
		typ  string
		data []byte
		ext  *[2]uint64
	}
	var items []item
	for i := 0; i < o.ExtraItems; i++ {
		items = append(items, item{id: idBase + 100 + uint32(i), typ: "hvc1"})
	}
	items = append(items, item{id: prim, typ: primType, data: primData})
	if o.Exif != nil {
		items = append(items, item{id: exifID, typ: "Exif", data: exifPayload, ext: o.ExifExtent})
	}
	if o.Thumb != nil {
		items = append(items, item{id: thumbID, typ: o.ThumbType, data: o.Thumb, ext: o.ThumbExtent})
	}
	count := func(n int) uint32 {
		if o.ClaimItems > 0 {
			return o.ClaimItems
		}
		return uint32(n)
	}

	build := func(mdatStart uint64) []byte {
		var infes [][]byte
		for _, it := range items {
			if o.Large {
				infes = append(infes, FullBox("infe", 3, 0, be32(it.id), be16(0), []byte(it.typ), []byte("\x00")))
			} else {
				infes = append(infes, FullBox("infe", 2, 0, be16(uint16(it.id)), be16(0), []byte(it.typ), []byte("\x00")))
			}
		}
		var iinf []byte
		if o.Large {
			iinf = FullBox("iinf", 1, 0, append([][]byte{be32(count(len(items)))}, infes...)...)
		} else {
			iinf = FullBox("iinf", 0, 0, append([][]byte{be16(uint16(count(len(items))))}, infes...)...)
		}

		// iloc
		pos := mdatStart
		iloc := []byte{byte(o.OffsetSize<<4 | o.OffsetSize), byte(o.BaseOffsetSize << 4)}
		if o.IlocVersion < 2 {
			iloc = append(iloc, be16(uint16(count(len(items))))...)
		} else {
			iloc = append(iloc, be32(count(len(items)))...)
		}
		var mdat []byte
		for _, it := range items {
			iloc = append(iloc, idw(it.id)...)
			if o.IlocVersion >= 1 {
				iloc = append(iloc, be16(0)...) // construction_method 0
			}
			iloc = append(iloc, be16(0)...) // data_reference_index
			iloc = append(iloc, sized(o.BaseOffsetSize, mdatStart)...)
			if it.data == nil {
				iloc = append(iloc, be16(0)...)
				continue
			}
			iloc = append(iloc, be16(1)...)
			off, n := pos, uint64(len(it.data))
			if it.ext != nil {
				off, n = it.ext[0], it.ext[1]
			}
			if o.BaseOffsetSize > 0 {
				off -= mdatStart
			}
			iloc = append(iloc, sized(o.OffsetSize, off)...)
			iloc = append(iloc, sized(o.OffsetSize, n)...)
			mdat = append(mdat, it.data...)
			pos += uint64(len(it.data))
		}
		ilocBox := FullBox("iloc", byte(o.IlocVersion), 0, iloc)

		// iref
		var refs [][]byte
		refBox := func(typ string, from, to uint32) []byte {
			return Box(typ, idw(from), be16(1), idw(to))
		}
		if o.Exif != nil && !o.NoCdsc {
			refs = append(refs, refBox("cdsc", exifID, prim))
		}
		if o.Thumb != nil {
			refs = append(refs, refBox("thmb", thumbID, prim))
		}
		refVer := byte(0)
		if o.Large {
			refVer = 1
		}
		iref := FullBox("iref", refVer, 0, refs...)

		// iprp: property 1 = primary ispe, then transforms, then thumb ispe.
		props := [][]byte{FullBox("ispe", 0, 0, be32(uint32(o.Width)), be32(uint32(o.Height)))}
		primAssoc := []int{1}
		for _, t := range o.Transforms {
			if t.Mirror {
				props = append(props, Box("imir", []byte{byte(t.Value)}))
			} else {
				props = append(props, Box("irot", []byte{byte(t.Value)}))
			}
			primAssoc = append(primAssoc, len(props))
		}
		var thumbAssoc []int
		if o.Thumb != nil {
			props = append(props, FullBox("ispe", 0, 0, be32(uint32(o.ThumbW)), be32(uint32(o.ThumbH))))
			thumbAssoc = []int{len(props)}
		}
		assoc := func(id uint32, idx []int) []byte {
			b := append(idw(id), byte(len(idx)))
			for _, i := range idx {
				if o.Large {
					b = append(b, be16(uint16(i)|0x8000)...)
				} else {
					b = append(b, byte(i)|0x80)
				}
			}
			return b
		}
		n := 1
		ipma := [][]byte{nil, assoc(prim, primAssoc)}
		if o.Thumb != nil {
			ipma = append(ipma, assoc(thumbID, thumbAssoc))
			n++
		}
		ipma[0] = be32(count(n))
		var ipmaBox []byte
		if o.Large {
			ipmaBox = FullBox("ipma", 1, 1, ipma...)
		} else {
			ipmaBox = FullBox("ipma", 0, 0, ipma...)
		}
		iprp := Box("iprp", Box("ipco", props...), ipmaBox)

		var pitm []byte
		if o.Large {
			pitm = FullBox("pitm", 1, 0, be32(prim))
		} else {
			pitm = FullBox("pitm", 0, 0, be16(uint16(prim)))
		}
		meta := FullBox("meta", 0, 0,
			FullBox("hdlr", 0, 0, be32(0), []byte("pict"), make([]byte, 13)),
			pitm, iinf, iref, ilocBox, iprp)
		ftyp := Box("ftyp", []byte(o.Brand), be32(0), []byte("mif1"), []byte(o.Brand))
		out := append(ftyp, meta...)
		return append(out, Box("mdat", mdat)...)
	}
	first := build(0)
	start := uint64(len(first)) // mdat payload start = file length - payload length
	for _, it := range items {
		if it.data != nil {
			start -= uint64(len(it.data))
		}
	}
	return build(start)
}
