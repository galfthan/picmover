package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ---- ISO BMFF: HEIF/AVIF meta, shared by Read and Previews ----------------

// Limits for ISO BMFF parsing in this file and in the CR3 preview locator.
// (The CR3 metadata reader in exif.go keeps its own global maxBoxes cap.)
const (
	maxBoxesPerLevel = 64       // boxes visited in one container (except item lists)
	maxBoxDepth      = 6        // nesting depth; top-level boxes are depth 1
	maxBoxesTotal    = 4096     // boxes visited per file
	maxItems         = 1024     // items in iinf/iloc/ipma/iref, properties in ipco
	maxTableRead     = 64 << 10 // bytes read of one iloc, ipma or iref payload
	maxTransforms    = 8        // irot/imir properties applied to the primary item
	maxBrands        = 64       // compatible brands examined in ftyp
)

var heifBrands = map[string]bool{
	"heic": true, "heix": true, "hevc": true, "mif1": true, "msf1": true, "avif": true, "avis": true,
}

// isHEIF reports whether the ftyp box at offset 0 has a HEIF/AVIF major or
// compatible brand.
func isHEIF(r io.ReaderAt, size int64) bool {
	h, err := readAt(r, size, 0, min(size, 8))
	if err != nil || len(h) < 8 || string(h[4:8]) != "ftyp" {
		return false
	}
	n := min(int64(binary.BigEndian.Uint32(h[0:4])), size, 16+4*maxBrands)
	if n < 12 {
		return false
	}
	b, err := readAt(r, size, 0, n)
	if err != nil {
		return false
	}
	if heifBrands[string(b[8:12])] {
		return true
	}
	for i := 16; i+4 <= len(b); i += 4 {
		if heifBrands[string(b[i:i+4])] {
			return true
		}
	}
	return false
}

// bmff walks ISO BMFF boxes with per-level, depth and total caps. Every read
// goes through readAt.
type bmff struct {
	r     io.ReaderAt
	size  int64
	total int
	warn  func(format string, args ...any)
}

// header reads the box header at off within [off, end).
func (m *bmff) header(off, end int64) (box, bool) {
	if off+8 > end {
		return box{}, false
	}
	h, err := readAt(m.r, m.size, off, 8)
	if err != nil {
		m.warn("BMFF: box header at %d outside file", off)
		return box{}, false
	}
	b := box{typ: string(h[4:8]), off: off, body: off + 8}
	switch sz := int64(binary.BigEndian.Uint32(h[0:4])); {
	case sz == 0: // extends to the end of the container
		b.end = end
	case sz == 1:
		lh, err := readAt(m.r, m.size, off+8, 8)
		if err != nil {
			m.warn("BMFF: box %q truncated", b.typ)
			return box{}, false
		}
		u := binary.BigEndian.Uint64(lh)
		if u < 16 || u > uint64(end-off) {
			m.warn("BMFF: box %q at %d has invalid size %d", b.typ, off, u)
			return box{}, false
		}
		b.body, b.end = off+16, off+int64(u)
	case sz < 8:
		m.warn("BMFF: box %q at %d smaller than its header", b.typ, off)
		return box{}, false
	default:
		b.end = off + sz
	}
	if b.end > end {
		m.warn("BMFF: box %q at %d larger than its container", b.typ, off)
		return box{}, false
	}
	return b, true
}

// children calls fn for each box in [start, end) at nesting depth (1 for
// top-level boxes), visiting at most limit boxes; fn returns false to stop.
func (m *bmff) children(start, end int64, depth, limit int, fn func(box) bool) {
	if depth > maxBoxDepth {
		m.warn("BMFF: boxes nested deeper than %d ignored", maxBoxDepth)
		return
	}
	for off, n := start, 0; off < end; n++ {
		if n >= limit {
			m.warn("BMFF: more than %d boxes in one container, rest ignored", limit)
			return
		}
		if m.total >= maxBoxesTotal {
			m.warn("BMFF: more than %d boxes, rest ignored", maxBoxesTotal)
			return
		}
		m.total++
		b, ok := m.header(off, end)
		if !ok || !fn(b) {
			return
		}
		off = b.end // b.end >= off+8: always progresses
	}
}

// find returns the first child of type typ in [start, end).
func (m *bmff) find(start, end int64, depth int, typ string) (box, bool) {
	var found box
	var ok bool
	m.children(start, end, depth, maxBoxesPerLevel, func(b box) bool {
		if b.typ == typ {
			found, ok = b, true
			return false
		}
		return true
	})
	return found, ok
}

// payload reads at most max bytes of b's payload, starting skip bytes in.
func (m *bmff) payload(b box, skip, max int64) ([]byte, bool) {
	start := b.body + skip
	if start > b.end {
		return nil, false
	}
	n := min(b.end-start, max)
	p, err := readAt(m.r, m.size, start, n)
	if err != nil {
		m.warn("BMFF: box %q: %v", b.typ, err)
		return nil, false
	}
	if n < b.end-start && max == maxTableRead {
		m.warn("BMFF: box %q larger than %d bytes, truncated", b.typ, maxTableRead)
	}
	return p, true
}

// cursor reads big-endian integers from an in-memory payload; once a read
// runs past the end it returns zeros and bad is set.
type cursor struct {
	b   []byte
	bad bool
}

func (c *cursor) take(n int) []byte {
	if c.bad || n < 0 || n > len(c.b) {
		c.bad = true
		return nil
	}
	v := c.b[:n]
	c.b = c.b[n:]
	return v
}

// uint reads an n-byte big-endian unsigned integer, n in {0, 1, 2, 4, 8}.
func (c *cursor) uint(n int) uint64 {
	b := c.take(n)
	if b == nil {
		return 0
	}
	switch n {
	case 1:
		return uint64(b[0])
	case 2:
		return uint64(binary.BigEndian.Uint16(b))
	case 4:
		return uint64(binary.BigEndian.Uint32(b))
	case 8:
		return binary.BigEndian.Uint64(b)
	}
	return 0
}

func (c *cursor) u8() uint64  { return c.uint(1) }
func (c *cursor) u16() uint64 { return c.uint(2) }
func (c *cursor) u32() uint64 { return c.uint(4) }

// xform is one transformative property: irot (rotate Value*90 degrees
// counter-clockwise) or imir (mirror; Value 0 = about the vertical axis,
// i.e. left-right, 1 = about the horizontal axis, i.e. top-bottom).
type xform struct {
	mirror bool
	v      int
}

// mat is a 2x2 integer matrix [a b; c d] acting on display coordinates
// (x to the right, y down, origin at the image centre):
// x' = a*x + b*y, y' = c*x + d*y.
type mat [4]int

func (m mat) mul(n mat) mat { // m·n: apply n first, then m
	return mat{m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3], m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3]}
}

// exifOrientations[o-1] is the transform that EXIF orientation o asks a
// viewer to apply to the stored image to display it.
var exifOrientations = [8]mat{
	{1, 0, 0, 1},   // 1 identity
	{-1, 0, 0, 1},  // 2 mirror left-right
	{-1, 0, 0, -1}, // 3 rotate 180
	{1, 0, 0, -1},  // 4 mirror top-bottom
	{0, 1, 1, 0},   // 5 transpose (mirror left-right, then rotate 90 CCW)
	{0, -1, 1, 0},  // 6 rotate 90 CW
	{0, -1, -1, 0}, // 7 transverse (mirror left-right, then rotate 90 CW)
	{0, 1, -1, 0},  // 8 rotate 90 CCW
}

// heifOrientation maps HEIF transformative properties, applied in ipma
// association order, to the EXIF orientation describing the same display
// transform, by composing them as matrices. The resulting table:
//
//	irot alone:  0 -> 1, 1 (90 CCW) -> 8, 2 -> 3, 3 (270 CCW = 90 CW) -> 6
//	imir alone:  axis 0 (left-right) -> 2, axis 1 (top-bottom) -> 4
//	irot then imir:  axis 0: angle 0 -> 2, 1 -> 7, 2 -> 4, 3 -> 5
//	                 axis 1: angle 0 -> 4, 1 -> 5, 2 -> 2, 3 -> 7
//	imir then irot:  axis 0: angle 0 -> 2, 1 -> 5, 2 -> 4, 3 -> 7
//	                 axis 1: angle 0 -> 4, 1 -> 7, 2 -> 2, 3 -> 5
//
// It returns 0 for an empty list. Read reports this value as
// Fields.Orientation whenever it is non-zero, overriding the Exif item's
// Orientation tag (see state.heif).
func heifOrientation(ops []xform) int {
	if len(ops) == 0 {
		return 0
	}
	m := mat{1, 0, 0, 1}
	for _, op := range ops {
		var t mat
		switch {
		case op.mirror && op.v&1 == 0:
			t = mat{-1, 0, 0, 1}
		case op.mirror:
			t = mat{1, 0, 0, -1}
		default:
			t = [4]mat{{1, 0, 0, 1}, {0, 1, -1, 0}, {-1, 0, 0, -1}, {0, -1, 1, 0}}[op.v&3]
		}
		m = t.mul(m)
	}
	for i, e := range exifOrientations {
		if e == m {
			return i + 1
		}
	}
	return 0 // unreachable: the dihedral group has exactly these 8 elements
}

// heifInfo is what parseHEIF extracts from the meta box.
type heifInfo struct {
	exifOff, exifLen int64 // extent of the primary item's Exif item; exifLen 0 = none
	orientation      int   // from irot/imir of the primary item; 0 = none
	thumbs           []PreviewRef
}

type heifLoc struct{ off, n int64 }

// parseHEIF parses the top-level meta box. ok is false when there is none.
func parseHEIF(r io.ReaderAt, size int64, warn func(string, ...any)) (info heifInfo, ok bool) {
	m := &bmff{r: r, size: size, warn: warn}
	meta, ok := m.find(0, size, 1, "meta")
	if !ok {
		return info, false
	}
	// meta is a full box: its children start after version/flags.
	kids := map[string]box{}
	m.children(meta.body+4, meta.end, 2, maxBoxesPerLevel, func(b box) bool {
		if _, dup := kids[b.typ]; !dup {
			kids[b.typ] = b
		}
		return true
	})
	if b, ok := kids["hdlr"]; ok {
		if p, ok := m.payload(b, 0, 12); ok && len(p) == 12 && string(p[8:12]) != "pict" {
			warn("HEIF: meta handler %q is not pict", p[8:12])
			return info, false
		}
	}

	var primary uint64
	hasPrimary := false
	if b, ok := kids["pitm"]; ok {
		if p, ok := m.payload(b, 0, 8); ok {
			c := &cursor{b: p}
			if c.u8() == 0 {
				c.take(3)
				primary = c.u16()
			} else {
				c.take(3)
				primary = c.u32()
			}
			hasPrimary = !c.bad
		}
	}
	isPrimary := func(id uint64) bool { return !hasPrimary || id == primary }

	// iinf: item types of Exif and jpeg items.
	var exifIDs []uint64
	jpegItems := map[uint64]bool{}
	if b, ok := kids["iinf"]; ok {
		if p, ok := m.payload(b, 0, 8); ok {
			c := &cursor{b: p}
			ver := c.u8()
			c.take(3)
			var count uint64
			skip := int64(6)
			if ver == 0 {
				count = c.u16()
			} else {
				count, skip = c.u32(), 8
			}
			if !c.bad {
				if count > maxItems {
					warn("HEIF: iinf claims %d items, reading at most %d", count, maxItems)
				}
				badVer := false
				m.children(b.body+skip, b.end, 3, int(min(count, maxItems)), func(e box) bool {
					if e.typ != "infe" {
						return true
					}
					p, ok := m.payload(e, 0, 14)
					if !ok {
						return true
					}
					c := &cursor{b: p}
					v := c.u8()
					c.take(3)
					var id uint64
					switch v {
					case 2:
						id = c.u16()
					case 3:
						id = c.u32()
					default:
						badVer = true
						return true
					}
					c.take(2) // item_protection_index
					typ := string(c.take(4))
					if c.bad {
						return true
					}
					switch typ {
					case "Exif":
						if len(exifIDs) < maxPreviews {
							exifIDs = append(exifIDs, id)
						}
					case "jpeg":
						jpegItems[id] = true
					}
					return true
				})
				if badVer {
					warn("HEIF: infe versions other than 2 and 3 ignored")
				}
			}
		}
	}

	// iref: cdsc (Exif item -> primary) and thmb (thumbnail -> primary).
	var exifID uint64
	hasExif := false
	var thumbIDs []uint64
	if b, ok := kids["iref"]; ok {
		if p, ok := m.payload(b, 0, maxTableRead); ok {
			c := &cursor{b: p}
			ver := c.u8()
			c.take(3)
			idSize := 2
			if ver != 0 {
				idSize = 4
			}
			for n := 0; n < maxItems && len(c.b) >= 8 && !c.bad; n++ {
				sz := int(binary.BigEndian.Uint32(c.b[0:4]))
				typ := string(c.b[4:8])
				if sz == 0 {
					sz = len(c.b)
				}
				if sz < 8 || sz > len(c.b) {
					warn("HEIF: iref entry %q has invalid size %d", typ, sz)
					break
				}
				rc := &cursor{b: c.take(sz)[8:]}
				from := rc.uint(idSize)
				cnt := rc.u16()
				to := false
				for i := uint64(0); i < cnt && i < maxItems && !rc.bad; i++ {
					if id := rc.uint(idSize); !rc.bad && isPrimary(id) {
						to = true
					}
				}
				switch {
				case !to:
				case typ == "cdsc" && !hasExif:
					for _, id := range exifIDs {
						if id == from {
							exifID, hasExif = from, true
						}
					}
				case typ == "thmb" && jpegItems[from] && len(thumbIDs) < maxPreviews:
					thumbIDs = append(thumbIDs, from) // HEVC/AV1-coded thumbnails are not previews
				}
			}
		}
	}
	if !hasExif && len(exifIDs) > 0 {
		exifID, hasExif = exifIDs[0], true
	}

	// iloc: extents of the Exif item and the thumbnails.
	need := map[uint64]bool{}
	if hasExif {
		need[exifID] = true
	}
	for _, id := range thumbIDs {
		need[id] = true
	}
	locs := map[uint64]heifLoc{}
	if b, ok := kids["iloc"]; ok && len(need) > 0 {
		if p, ok := m.payload(b, 0, maxTableRead); ok {
			parseIloc(&cursor{b: p}, size, need, locs, warn)
		}
	}
	if hasExif {
		if l, ok := locs[exifID]; ok {
			info.exifOff, info.exifLen = l.off, l.n
		}
	}

	// iprp: ipma associations, then the ipco properties they name.
	primaryProps := []int{}
	thumbProps := map[uint64][]int{}
	if iprp, ok := kids["iprp"]; ok {
		var ipco, ipma box
		var haveIpco, haveIpma bool
		m.children(iprp.body, iprp.end, 3, maxBoxesPerLevel, func(b box) bool {
			switch {
			case b.typ == "ipco" && !haveIpco:
				ipco, haveIpco = b, true
			case b.typ == "ipma" && !haveIpma:
				ipma, haveIpma = b, true
			}
			return true
		})
		if haveIpma {
			if p, ok := m.payload(ipma, 0, maxTableRead); ok {
				c := &cursor{b: p}
				ver := c.u8()
				flags := c.uint(2)<<8 | c.u8()
				idSize := 2
				if ver >= 1 {
					idSize = 4
				}
				count := c.u32()
				for i := uint64(0); i < count && i < maxItems && !c.bad; i++ {
					id := c.uint(idSize)
					n := c.u8()
					for j := uint64(0); j < n && !c.bad; j++ {
						var idx int
						if flags&1 != 0 {
							idx = int(c.u16() & 0x7FFF)
						} else {
							idx = int(c.u8() & 0x7F)
						}
						switch {
						case c.bad || idx == 0:
						case hasPrimary && id == primary && len(primaryProps) < 4*maxTransforms:
							primaryProps = append(primaryProps, idx)
						case need[id] && len(thumbProps[id]) < maxTransforms:
							thumbProps[id] = append(thumbProps[id], idx)
						}
					}
				}
			}
		}
		wanted := map[int]bool{}
		for _, i := range primaryProps {
			wanted[i] = true
		}
		for _, l := range thumbProps {
			for _, i := range l {
				wanted[i] = true
			}
		}
		type prop struct {
			typ  string
			v    int
			w, h int
		}
		props := map[int]prop{}
		if haveIpco && len(wanted) > 0 {
			idx := 0
			m.children(ipco.body, ipco.end, 4, maxItems, func(b box) bool {
				idx++
				if !wanted[idx] {
					return true
				}
				switch b.typ {
				case "irot", "imir":
					if p, ok := m.payload(b, 0, 1); ok && len(p) == 1 {
						props[idx] = prop{typ: b.typ, v: int(p[0])}
					}
				case "ispe":
					if p, ok := m.payload(b, 0, 12); ok && len(p) == 12 {
						w, h := binary.BigEndian.Uint32(p[4:]), binary.BigEndian.Uint32(p[8:])
						if w > 0 && h > 0 && w <= 1<<16 && h <= 1<<16 {
							props[idx] = prop{typ: b.typ, w: int(w), h: int(h)}
						}
					}
				}
				return true
			})
		}
		var ops []xform
		for _, i := range primaryProps {
			if p, ok := props[i]; ok && p.typ != "ispe" && len(ops) < maxTransforms {
				ops = append(ops, xform{mirror: p.typ == "imir", v: p.v})
			}
		}
		info.orientation = heifOrientation(ops)
		for _, id := range thumbIDs {
			l, ok := locs[id]
			if !ok {
				continue
			}
			ref := PreviewRef{Offset: l.off, Length: l.n}
			for _, i := range thumbProps[id] {
				if p, ok := props[i]; ok && p.typ == "ispe" {
					ref.Width, ref.Height = p.w, p.h
				}
			}
			info.thumbs = append(info.thumbs, ref)
		}
	} else {
		for _, id := range thumbIDs {
			if l, ok := locs[id]; ok {
				info.thumbs = append(info.thumbs, PreviewRef{Offset: l.off, Length: l.n})
			}
		}
	}
	return info, true
}

// parseIloc reads an iloc payload (versions 0, 1, 2) and stores in locs the
// extent of each item in need that has construction_method 0, data
// reference 0 and exactly one extent lying inside the file.
func parseIloc(c *cursor, size int64, need map[uint64]bool, locs map[uint64]heifLoc, warn func(string, ...any)) {
	ver := c.u8()
	c.take(3)
	if ver > 2 {
		warn("HEIF: iloc version %d not supported", ver)
		return
	}
	b0, b1 := c.u8(), c.u8()
	offSize, lenSize, baseSize := int(b0>>4), int(b0&15), int(b1>>4)
	idxSize := 0
	if ver >= 1 {
		idxSize = int(b1 & 15)
	}
	for _, n := range []int{offSize, lenSize, baseSize, idxSize} {
		if n != 0 && n != 4 && n != 8 {
			warn("HEIF: iloc field size %d not supported", n)
			return
		}
	}
	var count uint64
	idSize := 2
	if ver < 2 {
		count = c.u16()
	} else {
		count, idSize = c.u32(), 4
	}
	if count > maxItems {
		warn("HEIF: iloc claims %d items, reading at most %d", count, maxItems)
	}
	extSize := idxSize + offSize + lenSize
	for i := uint64(0); i < count && i < maxItems && !c.bad; i++ {
		id := c.uint(idSize)
		method := uint64(0)
		if ver >= 1 {
			method = c.u16() & 15
		}
		dataRef := c.u16()
		base := c.uint(baseSize)
		ext := int(c.u16())
		if c.bad {
			break
		}
		if !need[id] || ext != 1 || method != 0 || dataRef != 0 {
			if need[id] {
				warn("HEIF: item %d: %d extents, construction method %d, data reference %d not supported", id, ext, method, dataRef)
			}
			c.take(ext * extSize)
			continue
		}
		c.take(idxSize)
		off, n := c.uint(offSize), c.uint(lenSize)
		if c.bad {
			break
		}
		start := base + off
		if start < base || start > uint64(size) {
			warn("HEIF: item %d at offset %d outside file", id, start)
			continue
		}
		if n == 0 { // the rest of the file
			n = uint64(size) - start
		}
		if n > uint64(size)-start {
			warn("HEIF: item %d at %d+%d outside file", id, start, n)
			continue
		}
		locs[id] = heifLoc{off: int64(start), n: int64(n)}
	}
	if c.bad {
		warn("HEIF: iloc truncated")
	}
}

// heif reads the Exif item of a HEIF/AVIF file. The orientation derived
// from the primary item's irot/imir properties wins over an EXIF
// Orientation tag: irot/imir are what HEIF/AVIF renderers (libheif,
// libavif-based viewers, browsers) apply, while the EXIF tag in a HEIF
// file is informative metadata that renderers do not use. When both are
// present and disagree, a warning records it. EXIF Orientation is used
// only when there is no irot/imir.
func (s *state) heif(r io.ReaderAt, size int64) error {
	info, ok := parseHEIF(r, size, s.warn)
	if !ok {
		return fmt.Errorf("%w: no HEIF meta box", ErrNoExif)
	}
	err := fmt.Errorf("%w: no Exif item", ErrNoExif)
	if info.exifLen > 0 {
		err = s.heifExif(r, size, info.exifOff, info.exifLen)
	}
	if info.orientation > 0 {
		if o := s.f.Orientation; o != 0 && o != info.orientation {
			s.warn("HEIF: EXIF Orientation %d disagrees with irot/imir (orientation %d); using irot/imir", o, info.orientation)
		}
		s.f.Orientation = info.orientation
	}
	if err != nil {
		if info.orientation == 0 {
			return err
		}
		s.warn("HEIF: %v", err)
	}
	return nil
}

// heifExif parses an Exif item: a 4-byte big-endian offset from the end of
// that field to the TIFF header, then the TIFF structure.
func (s *state) heifExif(r io.ReaderAt, size, off, n int64) error {
	if n < 4+8 {
		return fmt.Errorf("%w: Exif item of %d bytes too small", ErrNoExif, n)
	}
	h, err := readAt(r, size, off, 4)
	if err != nil {
		return fmt.Errorf("%w: Exif item: %v", ErrNoExif, err)
	}
	skip := int64(binary.BigEndian.Uint32(h))
	if skip > n-4-8 {
		return fmt.Errorf("%w: Exif item TIFF offset %d outside item", ErrNoExif, skip)
	}
	start := off + 4 + skip
	// Some writers give offset 0 but still include "Exif\0\0".
	if p, err := readAt(r, size, start, 6); err == nil && bytes.Equal(p, exifHeader) && off+n-start >= 6+8 {
		start += 6
	}
	end := off + n
	return s.tiff(io.NewSectionReader(r, start, end-start), end-start, tiffMain, false)
}

// maxTopBoxes caps the top-level boxes HasBox visits.
const maxTopBoxes = 64

// ErrBoxLimit is returned by HasBox when the file has more top-level boxes
// than it examines.
var ErrBoxLimit = errors.New("exif: too many top-level ISO BMFF boxes")

// HasBox reports whether the ISO BMFF file r of size bytes has a top-level
// box of type name. It reads only box headers, visits at most 64 boxes and
// validates every size against the file. A malformed box ends the walk
// with an error (boxes after it are unreachable for any parser that
// validates sizes); more than 64 boxes gives ErrBoxLimit.
func HasBox(r io.ReaderAt, size int64, name string) (bool, error) {
	for off, n := int64(0), 0; off < size; n++ {
		if n >= maxTopBoxes {
			return false, ErrBoxLimit
		}
		if size-off < 8 {
			return false, fmt.Errorf("exif: %d trailing bytes at %d are not a box", size-off, off)
		}
		h, err := readAt(r, size, off, 8)
		if err != nil {
			return false, fmt.Errorf("exif: box header at %d: %w", off, err)
		}
		typ := string(h[4:8])
		var end int64
		switch sz := int64(binary.BigEndian.Uint32(h[0:4])); {
		case sz == 0:
			end = size
		case sz == 1:
			lh, err := readAt(r, size, off+8, 8)
			if err != nil {
				return false, fmt.Errorf("exif: box %q at %d truncated", typ, off)
			}
			u := binary.BigEndian.Uint64(lh)
			if u < 16 || u > uint64(size-off) {
				return false, fmt.Errorf("exif: box %q at %d has invalid size %d", typ, off, u)
			}
			end = off + int64(u)
		case sz < 8 || sz > size-off:
			return false, fmt.Errorf("exif: box %q at %d has invalid size %d", typ, off, sz)
		default:
			end = off + sz
		}
		if typ == name {
			return true, nil
		}
		off = end
	}
	return false, nil
}
