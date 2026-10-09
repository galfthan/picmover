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
	if e.Value != nil {
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
func TIFF(bo ByteOrder, ifds ...IFD) []byte {
	if bo == nil {
		bo = binary.LittleEndian
	}
	offs := make([]uint32, len(ifds))
	o := uint32(8)
	for i, d := range ifds {
		offs[i] = o
		o += ifdLen(d)
	}
	var out []byte
	if bo == binary.BigEndian {
		out = []byte{'M', 'M', 0, 0x2A}
	} else {
		out = []byte{'I', 'I', 0x2A, 0}
	}
	if len(ifds) > 0 {
		out = bo.AppendUint32(out, offs[0])
	} else {
		out = bo.AppendUint32(out, 0)
	}
	for i, d := range ifds {
		out = bo.AppendUint16(out, uint16(len(d.Entries)))
		dataOff := offs[i] + uint32(2+12*len(d.Entries)+4)
		var data []byte
		for _, e := range d.Entries {
			out = bo.AppendUint16(out, e.Tag)
			out = bo.AppendUint16(out, e.Type)
			out = bo.AppendUint32(out, e.count())
			if e.Value != nil {
				out = bo.AppendUint32(out, *e.Value)
				continue
			}
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
	return out
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
