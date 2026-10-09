// Package exif is a small, bounded EXIF reader. It extracts only the fields
// picmover uses (dates, offsets, camera, orientation, dimensions, GPS) from
// JPEG, TIFF-based raw formats, Canon CR3 and Fujifilm RAF files.
//
// It is written for hostile input: every read goes through io.ReaderAt with
// an offset and length checked against the file size, nothing is allocated
// from an untrusted size beyond fixed caps, and IFD walking is protected
// against cycles. Malformed parts produce warnings; whatever could be read is
// still returned.
package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Fields are the EXIF values picmover uses. Zero values mean "absent".
type Fields struct {
	DateTimeOriginal, DateTimeDigitized, DateTime       string // raw ASCII as in the file, NUL/space trimmed
	OffsetTimeOriginal, OffsetTimeDigitized, OffsetTime string // "+02:00" style, tags 0x9011, 0x9012, 0x9010
	Make, Model, LensModel                              string
	Orientation                                         int // 1..8, 0 if absent/invalid
	PixelXDimension, PixelYDimension                    int // from the Exif IFD (0xA002/0xA003)
	ImageWidth, ImageLength                             int // of the full-resolution image IFD
	Lat, Lon                                            float64
	HasGPS                                              bool
	Altitude                                            float64
	HasAltitude                                         bool
	Warnings                                            []string
}

// Limits. They bound work and memory for any input.
const (
	maxIFDs      = 32      // IFDs visited per file
	maxEntries   = 512     // entries per IFD
	maxDepth     = 4       // IFD nesting depth (IFD0 = 0)
	maxValueSize = 1 << 20 // bytes of one entry value (count*typeSize)
	maxASCII     = 1 << 10 // bytes read of an ASCII value
	maxBoxes     = 64      // ISO BMFF boxes visited in a CR3
	maxSegments  = 4096    // JPEG segments scanned
	maxWarnings  = 32
	maxRead      = maxValueSize // largest single read
)

// ErrNoExif is returned when no EXIF structure can be located.
var ErrNoExif = errors.New("exif: no EXIF structure found")

var (
	exifHeader = []byte("Exif\x00\x00")
	cr3UUID    = []byte{0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0, 0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48}
	rafMagic   = []byte("FUJIFILMCCD-RAW")
)

// Read parses r, which holds a whole file of size bytes. It detects JPEG,
// TIFF-based files (classic TIFF, CR2, NEF, ARW, DNG, PEF, SRW, plus Olympus
// ORF "IIRO"/"MMOR" and Panasonic RW2 "IIU\x00" headers), Canon CR3 (ISO BMFF)
// and Fujifilm RAF by magic bytes. It never reads more than it needs, never
// allocates based on untrusted sizes, and never loops: it returns an error
// only when no EXIF structure can be located; malformed parts produce
// warnings and the rest is still returned.
func Read(r io.ReaderAt, size int64) (*Fields, error) {
	if size < 8 {
		return nil, fmt.Errorf("%w: file too small", ErrNoExif)
	}
	head, err := readAt(r, size, 0, min(size, 16))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoExif, err)
	}
	s := &state{f: &Fields{}}
	switch {
	case head[0] == 0xFF && head[1] == 0xD8:
		err = s.jpeg(r, size)
	case bytes.HasPrefix(head, exifHeader):
		err = s.tiff(io.NewSectionReader(r, 6, size-6), size-6, tiffMain, false)
	case isTIFF(head):
		err = s.tiff(r, size, tiffMain, true)
	case string(head[4:8]) == "ftyp":
		err = s.cr3(r, size)
	case bytes.HasPrefix(head, rafMagic):
		err = s.raf(r, size)
	default:
		err = ErrNoExif
	}
	if err != nil {
		return nil, err
	}
	s.pickImageSize()
	return s.f, nil
}

func isTIFF(h []byte) bool {
	switch string(h[:4]) {
	case "II*\x00", "MM\x00*", "IIRO", "MMOR", "IIU\x00":
		return true
	}
	return false
}

// readAt reads exactly n bytes at off after checking the range against size.
func readAt(r io.ReaderAt, size, off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || n > maxRead || off > size || n > size-off {
		return nil, fmt.Errorf("range %d+%d outside file of %d bytes", off, n, size)
	}
	b := make([]byte, n)
	m, err := r.ReadAt(b, off)
	if int64(m) < n {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

type imageIFD struct {
	nst    uint32
	hasNST bool
	w, h   int
}

type state struct {
	f     *Fields
	nIFDs int
	imgs  []imageIFD
}

func (s *state) warn(format string, args ...any) {
	switch n := len(s.f.Warnings); {
	case n < maxWarnings:
		s.f.Warnings = append(s.f.Warnings, "exif: "+fmt.Sprintf(format, args...))
	case n == maxWarnings:
		s.f.Warnings = append(s.f.Warnings, "exif: further warnings suppressed")
	}
}

// pickImageSize chooses ImageWidth/ImageLength among the image IFDs seen:
// the largest full-resolution IFD (NewSubfileType 0), else the largest IFD
// that is not marked reduced-resolution.
func (s *state) pickImageSize() {
	best := func(ok func(imageIFD) bool) (int, int, bool) {
		var bw, bh int
		var area int64 = -1
		for _, c := range s.imgs {
			if c.w > 0 && c.h > 0 && ok(c) && int64(c.w)*int64(c.h) > area {
				bw, bh, area = c.w, c.h, int64(c.w)*int64(c.h)
			}
		}
		return bw, bh, area >= 0
	}
	if w, h, ok := best(func(c imageIFD) bool { return c.hasNST && c.nst == 0 }); ok {
		s.f.ImageWidth, s.f.ImageLength = w, h
		return
	}
	if w, h, ok := best(func(c imageIFD) bool { return !c.hasNST || c.nst&1 == 0 }); ok {
		s.f.ImageWidth, s.f.ImageLength = w, h
	}
}

// ---- JPEG ------------------------------------------------------------------

// jpeg walks the segments of a JPEG and parses the first APP1 whose payload
// starts with "Exif\0\0".
func (s *state) jpeg(r io.ReaderAt, size int64) error {
	pos := int64(2)
	for i := 0; i < maxSegments; i++ {
		hdr, err := readAt(r, size, pos, 4)
		if err != nil {
			break
		}
		if hdr[0] != 0xFF {
			s.warn("JPEG: no marker at offset %d", pos)
			break
		}
		marker := hdr[1]
		switch {
		case marker == 0xFF: // fill byte
			pos++
			continue
		case marker == 0xD9 || marker == 0xDA: // EOI, SOS
			return ErrNoExif
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // no length
			pos += 2
			continue
		}
		segLen := int64(binary.BigEndian.Uint16(hdr[2:4]))
		if segLen < 2 {
			s.warn("JPEG: bad segment length %d at offset %d", segLen, pos)
			break
		}
		payloadOff, payloadLen := pos+4, segLen-2
		if marker == 0xE1 && payloadLen >= int64(len(exifHeader)) {
			if id, err := readAt(r, size, payloadOff, int64(len(exifHeader))); err == nil && bytes.Equal(id, exifHeader) {
				payload, err := readAt(r, size, payloadOff, payloadLen)
				if err != nil {
					// Truncated file: parse what is there.
					avail := size - payloadOff
					if payload, err = readAt(r, size, payloadOff, avail); err != nil {
						return fmt.Errorf("%w: %v", ErrNoExif, err)
					}
					s.warn("JPEG: Exif segment truncated")
				}
				tiff := payload[len(exifHeader):]
				return s.tiff(bytes.NewReader(tiff), int64(len(tiff)), tiffMain, false)
			}
		}
		pos += 2 + segLen
	}
	return ErrNoExif
}

// ---- TIFF ------------------------------------------------------------------

type tiffKind int

const (
	tiffMain    tiffKind = iota // IFD0 is the main image IFD
	tiffCMT1                    // CR3 CMT1: IFD0 only, pointers not followed
	tiffExifIFD                 // CR3 CMT2: IFD0 is the Exif IFD
	tiffGPSIFD                  // CR3 CMT4: IFD0 is the GPS IFD
)

type tiffReader struct {
	s       *state
	r       io.ReaderAt
	size    int64
	bo      binary.ByteOrder
	visited map[int64]bool
	follow  bool // follow Exif/GPS pointers from IFD0
}

type entry struct {
	tag, typ uint16
	count    uint32
	raw      [4]byte
}

// tiff parses a TIFF structure that starts at offset 0 of r. chain says
// whether IFDs after IFD0 in the next-IFD chain are image candidates
// (true for TIFF-based files, false for JPEG where IFD1 is the thumbnail).
func (s *state) tiff(r io.ReaderAt, size int64, kind tiffKind, chain bool) error {
	h, err := readAt(r, size, 0, min(size, 8))
	if err != nil || len(h) < 8 {
		return fmt.Errorf("%w: TIFF header truncated", ErrNoExif)
	}
	t := &tiffReader{s: s, r: r, size: size, visited: map[int64]bool{}, follow: kind == tiffMain}
	switch string(h[:2]) {
	case "II":
		t.bo = binary.LittleEndian
	case "MM":
		t.bo = binary.BigEndian
	default:
		return fmt.Errorf("%w: bad TIFF byte order", ErrNoExif)
	}
	switch t.bo.Uint16(h[2:4]) {
	case 42, 0x4F52, 0x5352, 0x55: // TIFF, ORF "RO"/"SR", RW2 "U"
	default:
		return fmt.Errorf("%w: bad TIFF magic", ErrNoExif)
	}
	ifd0 := int64(t.bo.Uint32(h[4:8]))
	if ifd0 < 8 || ifd0 >= size {
		return fmt.Errorf("%w: IFD0 offset %d outside data", ErrNoExif, ifd0)
	}
	switch kind {
	case tiffMain, tiffCMT1:
		t.walkMain(ifd0, chain)
	case tiffExifIFD:
		t.walkExif(ifd0, 1)
	case tiffGPSIFD:
		t.walkGPS(ifd0, 1)
	}
	return nil
}

// readIFD reads the entry table at off. It enforces the visited set and the
// IFD and entry caps.
func (t *tiffReader) readIFD(off int64, depth int) (entries []entry, next int64, ok bool) {
	switch {
	case depth > maxDepth:
		t.s.warn("IFD nesting deeper than %d ignored", maxDepth)
		return nil, 0, false
	case t.visited[off]:
		t.s.warn("IFD loop at offset %d", off)
		return nil, 0, false
	case t.s.nIFDs >= maxIFDs:
		t.s.warn("more than %d IFDs, rest ignored", maxIFDs)
		return nil, 0, false
	}
	t.visited[off] = true
	t.s.nIFDs++
	cb, err := readAt(t.r, t.size, off, 2)
	if err != nil {
		t.s.warn("IFD at offset %d outside data", off)
		return nil, 0, false
	}
	n := int64(t.bo.Uint16(cb))
	if n > maxEntries {
		t.s.warn("IFD at offset %d claims %d entries (max %d), ignored", off, n, maxEntries)
		return nil, 0, false
	}
	tab, err := readAt(t.r, t.size, off+2, 12*n)
	if err != nil {
		t.s.warn("IFD at offset %d truncated", off)
		return nil, 0, false
	}
	entries = make([]entry, n)
	for i := range entries {
		b := tab[12*i:]
		entries[i] = entry{tag: t.bo.Uint16(b[0:]), typ: t.bo.Uint16(b[2:]), count: t.bo.Uint32(b[4:])}
		copy(entries[i].raw[:], b[8:12])
	}
	if nb, err := readAt(t.r, t.size, off+2+12*n, 4); err == nil {
		next = int64(t.bo.Uint32(nb))
	}
	return entries, next, true
}

func typeSize(typ uint16) int64 {
	switch typ {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11, 13: // LONG, SLONG, FLOAT, IFD
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 0
}

// value returns the first min(count, want) elements of e's value (want <= 0
// means all). The full value must be valid: size <= maxValueSize and inside
// the data; otherwise the entry is skipped with a warning.
func (t *tiffReader) value(e entry, want int64) ([]byte, bool) {
	ts := typeSize(e.typ)
	if ts == 0 {
		t.s.warn("tag 0x%04x: unknown type %d", e.tag, e.typ)
		return nil, false
	}
	if e.count == 0 {
		t.s.warn("tag 0x%04x: count 0", e.tag)
		return nil, false
	}
	total := int64(e.count) * ts // count < 2^32, ts <= 8: no overflow in int64
	if total > maxValueSize {
		t.s.warn("tag 0x%04x: value of %d bytes too large", e.tag, total)
		return nil, false
	}
	n := total
	if want > 0 && int64(e.count) > want {
		n = want * ts
	}
	if total <= 4 {
		return e.raw[:n], true
	}
	off := int64(t.bo.Uint32(e.raw[:]))
	if off > t.size || total > t.size-off {
		t.s.warn("tag 0x%04x: value at offset %d outside data", e.tag, off)
		return nil, false
	}
	b, err := readAt(t.r, t.size, off, n)
	if err != nil {
		t.s.warn("tag 0x%04x: %v", e.tag, err)
		return nil, false
	}
	return b, true
}

func (t *tiffReader) str(e entry) string {
	switch e.typ {
	case 1, 2, 7:
	default:
		t.s.warn("tag 0x%04x: type %d is not a string", e.tag, e.typ)
		return ""
	}
	b, ok := t.value(e, maxASCII)
	if !ok {
		return ""
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	s := strings.Trim(string(b), " \x00\t\r\n")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	return s
}

// ints returns up to max integer values of e (BYTE, SHORT, LONG, IFD and
// their signed variants).
func (t *tiffReader) ints(e entry, max int64) []int64 {
	switch e.typ {
	case 1, 3, 4, 6, 8, 9, 13:
	default:
		t.s.warn("tag 0x%04x: type %d is not an integer", e.tag, e.typ)
		return nil
	}
	b, ok := t.value(e, max)
	if !ok {
		return nil
	}
	ts := typeSize(e.typ)
	out := make([]int64, 0, int64(len(b))/ts)
	for i := int64(0); i+ts <= int64(len(b)); i += ts {
		var v int64
		switch e.typ {
		case 1:
			v = int64(b[i])
		case 6:
			v = int64(int8(b[i]))
		case 3:
			v = int64(t.bo.Uint16(b[i:]))
		case 8:
			v = int64(int16(t.bo.Uint16(b[i:])))
		case 4, 13:
			v = int64(t.bo.Uint32(b[i:]))
		case 9:
			v = int64(int32(t.bo.Uint32(b[i:])))
		}
		out = append(out, v)
	}
	return out
}

func (t *tiffReader) int1(e entry) (int64, bool) {
	v := t.ints(e, 1)
	if len(v) == 0 {
		return 0, false
	}
	return v[0], true
}

// rationals returns exactly n rationals of e as float64, or false (with a
// warning) when the type, count or a denominator is wrong.
func (t *tiffReader) rationals(e entry, n int64) ([]float64, bool) {
	if e.typ != 5 && e.typ != 10 {
		t.s.warn("tag 0x%04x: type %d is not a rational", e.tag, e.typ)
		return nil, false
	}
	if int64(e.count) < n {
		t.s.warn("tag 0x%04x: %d rationals, want %d", e.tag, e.count, n)
		return nil, false
	}
	b, ok := t.value(e, n)
	if !ok {
		return nil, false
	}
	out := make([]float64, n)
	for i := range out {
		num, den := t.bo.Uint32(b[8*i:]), t.bo.Uint32(b[8*i+4:])
		if den == 0 {
			t.s.warn("tag 0x%04x: zero denominator", e.tag)
			return nil, false
		}
		if e.typ == 10 {
			out[i] = float64(int32(num)) / float64(int32(den))
		} else {
			out[i] = float64(num) / float64(den)
		}
	}
	return out, true
}

func setStr(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// walkMain walks IFD0 and its next-IFD chain.
func (t *tiffReader) walkMain(off int64, chain bool) {
	for i := 0; off != 0; i++ {
		entries, next, ok := t.readIFD(off, 0)
		if !ok {
			return
		}
		t.imageIFD(entries, 0, i == 0, i == 0 || chain)
		off = next
	}
}

// imageIFD handles IFD0, chained IFDs and SubIFDs.
func (t *tiffReader) imageIFD(entries []entry, depth int, isIFD0, candidate bool) {
	var img imageIFD
	var exifOff, gpsOff int64
	var subs []int64
	for _, e := range entries {
		switch e.tag {
		case 0x00FE:
			if v, ok := t.int1(e); ok {
				img.nst, img.hasNST = uint32(v), true
			}
		case 0x0100:
			if v, ok := t.int1(e); ok && v > 0 && v <= 1<<30 {
				img.w = int(v)
			}
		case 0x0101:
			if v, ok := t.int1(e); ok && v > 0 && v <= 1<<30 {
				img.h = int(v)
			}
		case 0x014A:
			subs = t.ints(e, maxIFDs)
		}
		if !isIFD0 {
			continue
		}
		switch e.tag {
		case 0x010F:
			setStr(&t.s.f.Make, t.str(e))
		case 0x0110:
			setStr(&t.s.f.Model, t.str(e))
		case 0x0132:
			setStr(&t.s.f.DateTime, t.str(e))
		case 0x0112:
			if v, ok := t.int1(e); ok && v >= 1 && v <= 8 && t.s.f.Orientation == 0 {
				t.s.f.Orientation = int(v)
			}
		case 0x8769:
			if v, ok := t.int1(e); ok && t.follow {
				exifOff = v
			}
		case 0x8825:
			if v, ok := t.int1(e); ok && t.follow {
				gpsOff = v
			}
		}
	}
	if candidate {
		t.s.imgs = append(t.s.imgs, img)
	}
	if exifOff > 0 {
		t.walkExif(exifOff, depth+1)
	}
	if gpsOff > 0 {
		t.walkGPS(gpsOff, depth+1)
	}
	for _, sub := range subs {
		if sub <= 0 {
			continue
		}
		if es, _, ok := t.readIFD(sub, depth+1); ok {
			t.imageIFD(es, depth+1, false, true)
		}
	}
}

func (t *tiffReader) walkExif(off int64, depth int) {
	entries, _, ok := t.readIFD(off, depth)
	if !ok {
		return
	}
	f := t.s.f
	for _, e := range entries {
		switch e.tag {
		case 0x9003:
			setStr(&f.DateTimeOriginal, t.str(e))
		case 0x9004:
			setStr(&f.DateTimeDigitized, t.str(e))
		case 0x9010:
			setStr(&f.OffsetTime, t.str(e))
		case 0x9011:
			setStr(&f.OffsetTimeOriginal, t.str(e))
		case 0x9012:
			setStr(&f.OffsetTimeDigitized, t.str(e))
		case 0xA434:
			setStr(&f.LensModel, t.str(e))
		case 0xA002:
			if v, ok := t.int1(e); ok && v > 0 && v <= 1<<30 && f.PixelXDimension == 0 {
				f.PixelXDimension = int(v)
			}
		case 0xA003:
			if v, ok := t.int1(e); ok && v > 0 && v <= 1<<30 && f.PixelYDimension == 0 {
				f.PixelYDimension = int(v)
			}
		}
	}
}

func (t *tiffReader) walkGPS(off int64, depth int) {
	entries, _, ok := t.readIFD(off, depth)
	if !ok {
		return
	}
	var latRef, lonRef string
	var lat, lon, alt []float64
	var altRef int64
	var haveLat, haveLon bool
	for _, e := range entries {
		switch e.tag {
		case 0x0001:
			latRef = t.str(e)
		case 0x0002:
			haveLat = true
			lat, _ = t.rationals(e, 3)
		case 0x0003:
			lonRef = t.str(e)
		case 0x0004:
			haveLon = true
			lon, _ = t.rationals(e, 3)
		case 0x0005:
			altRef, _ = t.int1(e)
		case 0x0006:
			alt, _ = t.rationals(e, 1)
		}
	}
	f := t.s.f
	if alt != nil && !f.HasAltitude {
		f.Altitude, f.HasAltitude = alt[0], true
		if altRef == 1 {
			f.Altitude = -f.Altitude
		}
	}
	if !haveLat || !haveLon || lat == nil || lon == nil || f.HasGPS {
		return
	}
	la, ok1 := signed(lat, latRef, "N", "S")
	lo, ok2 := signed(lon, lonRef, "E", "W")
	if !ok1 || !ok2 {
		t.s.warn("GPS: bad reference %q/%q", latRef, lonRef)
		return
	}
	if la == 0 && lo == 0 {
		return
	}
	if la < -90 || la > 90 || lo < -180 || lo > 180 {
		t.s.warn("GPS: coordinates %v,%v out of range", la, lo)
		return
	}
	f.Lat, f.Lon, f.HasGPS = la, lo, true
}

// signed converts degrees/minutes/seconds and a reference to a signed value.
// A missing reference means positive.
func signed(dms []float64, ref, pos, neg string) (float64, bool) {
	v := dms[0] + dms[1]/60 + dms[2]/3600
	switch strings.ToUpper(strings.TrimSpace(ref)) {
	case "", pos:
		return v, true
	case neg:
		return -v, true
	}
	return 0, false
}

// ---- CR3 -------------------------------------------------------------------

type box struct {
	typ            string
	off, body, end int64 // box start, payload start, box end
}

type boxWalker struct {
	s    *state
	r    io.ReaderAt
	size int64
	n    int
}

// next reads the box header at off within [off, limit).
func (w *boxWalker) next(off, limit int64) (box, bool) {
	if w.n >= maxBoxes {
		w.s.warn("CR3: more than %d boxes, rest ignored", maxBoxes)
		return box{}, false
	}
	w.n++
	if off+8 > limit {
		return box{}, false
	}
	h, err := readAt(w.r, w.size, off, 8)
	if err != nil {
		return box{}, false
	}
	b := box{typ: string(h[4:8]), off: off, body: off + 8}
	sz := int64(binary.BigEndian.Uint32(h[0:4]))
	switch sz {
	case 0:
		b.end = limit
	case 1:
		lh, err := readAt(w.r, w.size, off+8, 8)
		if err != nil {
			w.s.warn("CR3: box %q truncated", b.typ)
			return box{}, false
		}
		u := binary.BigEndian.Uint64(lh)
		if u > uint64(limit) {
			w.s.warn("CR3: box %q larger than its container", b.typ)
			return box{}, false
		}
		b.body, sz = off+16, int64(u)
		b.end = off + sz
	default:
		b.end = off + sz
	}
	if b.end < b.body || b.end > limit {
		w.s.warn("CR3: box %q at %d has invalid size", b.typ, off)
		return box{}, false
	}
	return b, true
}

var cmtKinds = map[string]tiffKind{"CMT1": tiffCMT1, "CMT2": tiffExifIFD, "CMT4": tiffGPSIFD}

func (s *state) cr3(r io.ReaderAt, size int64) error {
	w := &boxWalker{s: s, r: r, size: size}
	find := func(start, end int64, match func(box) bool) (box, bool) {
		for off := start; off < end; {
			b, ok := w.next(off, end)
			if !ok {
				return box{}, false
			}
			if match(b) {
				return b, true
			}
			if b.end <= off {
				return box{}, false
			}
			off = b.end
		}
		return box{}, false
	}
	moov, ok := find(0, size, func(b box) bool { return b.typ == "moov" })
	if !ok {
		return fmt.Errorf("%w: no moov box", ErrNoExif)
	}
	uuid, ok := find(moov.body, moov.end, func(b box) bool {
		if b.typ != "uuid" {
			return false
		}
		id, err := readAt(r, size, b.body, 16)
		return err == nil && b.end-b.body >= 16 && bytes.Equal(id, cr3UUID)
	})
	if !ok {
		return fmt.Errorf("%w: no Canon metadata box", ErrNoExif)
	}
	found := false
	find(uuid.body+16, uuid.end, func(b box) bool {
		k, ok := cmtKinds[b.typ]
		if !ok {
			return false
		}
		if err := s.tiff(io.NewSectionReader(r, b.body, b.end-b.body), b.end-b.body, k, false); err != nil {
			s.warn("CR3: %s: %v", b.typ, err)
		} else {
			found = true
		}
		return false
	})
	if !found {
		return fmt.Errorf("%w: no readable CMT box", ErrNoExif)
	}
	return nil
}

// ---- RAF -------------------------------------------------------------------

func (s *state) raf(r io.ReaderAt, size int64) error {
	h, err := readAt(r, size, 0, 92)
	if err != nil || !bytes.HasPrefix(h, rafMagic) {
		return fmt.Errorf("%w: bad RAF header", ErrNoExif)
	}
	off := int64(binary.BigEndian.Uint32(h[84:88]))
	n := int64(binary.BigEndian.Uint32(h[88:92]))
	if off < 92 || n < 4 || off > size || n > size-off {
		return fmt.Errorf("%w: RAF JPEG at %d+%d outside file", ErrNoExif, off, n)
	}
	sr := io.NewSectionReader(r, off, n)
	if soi, err := readAt(sr, n, 0, 2); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return fmt.Errorf("%w: RAF JPEG has no SOI", ErrNoExif)
	}
	if err := s.jpeg(sr, n); err != nil {
		return err
	}
	// The embedded JPEG is a preview; its sizes are not the raw image size.
	s.imgs = nil
	return nil
}
