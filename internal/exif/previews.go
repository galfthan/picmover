package exif

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// PreviewRef locates an embedded JPEG inside a RAW or HEIF container.
type PreviewRef struct {
	Offset, Length int64
	Width, Height  int // 0 when unknown
}

const (
	maxPreviews   = 8        // previews returned
	maxCandidates = 32       // previews collected before sorting
	maxPRVWScan   = 64 << 10 // bytes of the CR3 preview box scanned for the JPEG
)

var (
	soi              = []byte{0xFF, 0xD8, 0xFF}
	cr3PreviewUUID   = []byte{0xea, 0xf4, 0x2b, 0x5e, 0x1c, 0x98, 0x4b, 0x88, 0xb9, 0xfb, 0xb7, 0xdc, 0x40, 0x6e, 0x4d, 0x16}
	olympusHeader    = []byte("OLYMPUS\x00")
	olympusOldHeader = []byte("OLYMP\x00")
	omSystemHeader   = []byte("OM SYSTEM\x00\x00\x00")
)

// Previews returns embedded JPEG previews, largest first. It never reads
// the preview bytes themselves; it only validates that the range lies in
// the file and that it starts with FF D8 FF (a 3-byte read).
//
// Sources: TIFF-based raw files (JPEGInterchangeFormat/Length in any IFD of
// the IFD0 chain or SubIFDs; single-strip IFDs with Compression 6 or 7 that
// are not CFA/LinearRaw data or CR2 raw slices; Panasonic RW2 JpgFromRaw
// 0x002E; Olympus ORF PreviewImageStart/Length in the maker note's
// CameraSettings IFD), Canon CR3 (the full-size JPEG track and the PRVW
// box), Fujifilm RAF (header offsets 84/88) and HEIF/AVIF (thmb items of
// type jpeg). Other formats, including plain JPEG, have none.
//
// Ordering: by Width*Height when every preview's size is known, otherwise
// by Length; ties keep the larger Length first, then file order. At most 8
// are returned. The error is non-nil only when the file header cannot be
// read; a file without previews returns nil, nil.
func Previews(r io.ReaderAt, size int64) ([]PreviewRef, error) {
	if size < 8 {
		return nil, nil
	}
	head, err := readAt(r, size, 0, min(size, 16))
	if err != nil {
		return nil, fmt.Errorf("exif: reading header: %w", err)
	}
	s := &state{f: &Fields{}} // collects (and drops) warnings
	p := &previewSet{r: r, size: size}
	switch {
	case isTIFF(head):
		s.tiffPreviews(r, size, p)
	case string(head[4:8]) == "ftyp" && isHEIF(r, size):
		if info, ok := parseHEIF(r, size, s.warn); ok {
			for _, t := range info.thumbs {
				p.add(t.Offset, t.Length, t.Width, t.Height)
			}
		}
	case string(head[4:8]) == "ftyp":
		cr3Previews(&bmff{r: r, size: size, warn: s.warn}, p)
	case bytes.HasPrefix(head, rafMagic):
		if h, err := readAt(r, size, 0, 92); err == nil {
			p.add(int64(binary.BigEndian.Uint32(h[84:88])), int64(binary.BigEndian.Uint32(h[88:92])), 0, 0)
		}
	}
	return p.result(), nil
}

type previewSet struct {
	r    io.ReaderAt
	size int64
	list []PreviewRef
}

// add records a candidate after checking its range and its first 3 bytes.
// A candidate at an offset already recorded only fills in unknown sizes.
func (p *previewSet) add(off, n int64, w, h int) bool {
	if off < 0 || n < int64(len(soi)) || off > p.size || n > p.size-off {
		return false
	}
	if w <= 0 || h <= 0 || w > 1<<16 || h > 1<<16 {
		w, h = 0, 0
	}
	for i, q := range p.list {
		if q.Offset == off {
			if q.Width == 0 && w > 0 {
				p.list[i].Width, p.list[i].Height = w, h
			}
			return true
		}
	}
	if len(p.list) >= maxCandidates {
		return false
	}
	b, err := readAt(p.r, p.size, off, int64(len(soi)))
	if err != nil || !bytes.Equal(b, soi) {
		return false
	}
	p.list = append(p.list, PreviewRef{Offset: off, Length: n, Width: w, Height: h})
	return true
}

func (p *previewSet) result() []PreviewRef {
	if len(p.list) == 0 {
		return nil
	}
	byArea := true
	for _, q := range p.list {
		if q.Width == 0 {
			byArea = false
		}
	}
	sort.SliceStable(p.list, func(i, j int) bool {
		a, b := p.list[i], p.list[j]
		if byArea {
			if aa, ba := int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height); aa != ba {
				return aa > ba
			}
		}
		return a.Length > b.Length
	})
	if len(p.list) > maxPreviews {
		p.list = p.list[:maxPreviews]
	}
	return p.list
}

// ---- TIFF-based raw ----------------------------------------------------------

func (s *state) tiffPreviews(r io.ReaderAt, size int64, p *previewSet) {
	t, off, err := s.newTIFFReader(r, size, tiffMain)
	if err != nil {
		return
	}
	for i := 0; off != 0; i++ {
		entries, next, ok := t.readIFD(off, 0)
		if !ok {
			return
		}
		t.previewIFD(entries, 0, i == 0, p)
		off = next
	}
}

// previewIFD records the previews of one IFD and descends into its SubIFDs
// and, for IFD0, the Exif IFD's maker note. t.r is the whole file, so all
// offsets are file offsets.
func (t *tiffReader) previewIFD(entries []entry, depth int, isIFD0 bool, p *previewSet) {
	var comp, photo, w, h, stripOff, stripLen, jifOff, jifLen, exifOff int64
	var slices bool
	var subs []int64
	for _, e := range entries {
		switch e.tag {
		case 0x0100:
			w, _ = t.int1(e)
		case 0x0101:
			h, _ = t.int1(e)
		case 0x0103:
			comp, _ = t.int1(e)
		case 0x0106:
			photo, _ = t.int1(e)
		case 0x0111:
			if e.count == 1 {
				stripOff, _ = t.int1(e)
			}
		case 0x0117:
			if e.count == 1 {
				stripLen, _ = t.int1(e)
			}
		case 0x0201:
			jifOff, _ = t.int1(e)
		case 0x0202:
			jifLen, _ = t.int1(e)
		case 0x002E: // Panasonic JpgFromRaw: UNDEFINED data
			if isIFD0 && (e.typ == 7 || e.typ == 1) && e.count > 4 {
				p.add(int64(t.bo.Uint32(e.raw[:])), int64(e.count), 0, 0)
			}
		case 0x014A:
			subs = t.ints(e, maxIFDs)
		case 0x8769:
			if isIFD0 {
				exifOff, _ = t.int1(e)
			}
		case 0xC640: // CR2 raw slices: the strip is lossless JPEG raw data
			slices = true
		}
	}
	if jifOff > 0 && jifLen > 0 {
		p.add(jifOff, jifLen, 0, 0)
	}
	// 32803 = CFA, 34892 = LinearRaw: lossless-JPEG raw data, not a preview.
	if (comp == 6 || comp == 7) && stripOff > 0 && stripLen > 0 && photo != 32803 && photo != 34892 && !slices {
		p.add(stripOff, stripLen, int(min(max(w, 0), 1<<20)), int(min(max(h, 0), 1<<20)))
	}
	for _, sub := range subs {
		if sub <= 0 {
			continue
		}
		if es, _, ok := t.readIFD(sub, depth+1); ok {
			t.previewIFD(es, depth+1, false, p)
		}
	}
	if exifOff > 0 {
		t.olympusPreview(exifOff, depth+1, p)
	}
}

// olympusPreview finds PreviewImageStart/Length (0x0101/0x0102) in the
// CameraSettings sub-IFD (0x2020) of an Olympus maker note. New-style notes
// ("OLYMPUS\0II" or "OM SYSTEM\0\0\0II") use offsets relative to the maker
// note; old-style "OLYMP\0" notes use offsets relative to the TIFF header.
func (t *tiffReader) olympusPreview(exifOff int64, depth int, p *previewSet) {
	entries, _, ok := t.readIFD(exifOff, depth)
	if !ok {
		return
	}
	var mn, mnLen int64 = -1, 0
	for _, e := range entries {
		if e.tag == 0x927C && (e.typ == 7 || e.typ == 1) && e.count > 16 {
			mn, mnLen = int64(t.bo.Uint32(e.raw[:])), int64(e.count)
		}
	}
	if mn < 0 || mn > t.size || mnLen > t.size-mn {
		return
	}
	hdr, err := readAt(t.r, t.size, mn, 16)
	if err != nil {
		return
	}
	var base, ifd int64
	var bo binary.ByteOrder
	order := func(b []byte) binary.ByteOrder {
		switch string(b) {
		case "II":
			return binary.LittleEndian
		case "MM":
			return binary.BigEndian
		}
		return nil
	}
	switch {
	case bytes.HasPrefix(hdr, olympusHeader):
		base, ifd, bo = mn, 12, order(hdr[8:10])
	case bytes.HasPrefix(hdr, omSystemHeader):
		base, ifd, bo = mn, 16, order(hdr[12:14])
	case bytes.HasPrefix(hdr, olympusOldHeader):
		base, ifd, bo = 0, mn+8, t.bo
	}
	if bo == nil {
		return
	}
	mt := &tiffReader{s: t.s, r: io.NewSectionReader(t.r, base, t.size-base), size: t.size - base, bo: bo, visited: map[int64]bool{}}
	es, _, ok := mt.readIFD(ifd, depth+1)
	if !ok {
		return
	}
	for _, e := range es {
		if e.tag != 0x2020 {
			continue
		}
		// Type IFD/LONG holds the offset as its value; UNDEFINED (count > 4)
		// holds the offset of its data, which is the IFD.
		if !(e.count == 1 && (e.typ == 4 || e.typ == 13)) && !(e.typ == 7 && e.count > 4) {
			return
		}
		cs, _, ok := mt.readIFD(int64(bo.Uint32(e.raw[:])), depth+2)
		if !ok {
			return
		}
		var start, n int64
		for _, c := range cs {
			switch c.tag {
			case 0x0101:
				start, _ = mt.int1(c)
			case 0x0102:
				n, _ = mt.int1(c)
			}
		}
		if start > 0 && n > 0 {
			p.add(base+start, n, 0, 0)
		}
		return
	}
}

// ---- CR3 ---------------------------------------------------------------------

// cr3Previews records the full-size JPEG (the first moov/trak whose single
// sample starts with FF D8 FF) and the PRVW JPEG of the preview uuid box.
func cr3Previews(m *bmff, p *previewSet) {
	m.children(0, m.size, 1, maxBoxesPerLevel, func(b box) bool {
		switch b.typ {
		case "moov":
			cr3FullSize(m, b, p)
		case "uuid":
			if id, err := readAt(m.r, m.size, b.body, 16); err == nil && b.end-b.body >= 16 && bytes.Equal(id, cr3PreviewUUID) {
				cr3PRVW(m, b, p)
			}
		}
		return true
	})
}

// cr3PRVW scans at most 64 KiB of the preview uuid box for the JPEG start.
// The layout before the JPEG is not agreed on, so a PRVW box header is used
// only to narrow the length and, when the JPEG sits right after the 16-byte
// PRVW header (u32 0, u16 1, u16 width, u16 height, u16 1, u32 length), to
// read the size.
func cr3PRVW(m *bmff, b box, p *previewSet) {
	start := b.body + 16
	n := min(b.end-start, maxPRVWScan)
	if n < int64(len(soi)) {
		return
	}
	win, err := readAt(m.r, m.size, start, n)
	if err != nil {
		return
	}
	i := bytes.Index(win, soi)
	if i < 0 {
		return
	}
	jpg, end := start+int64(i), b.end
	var w, h int
	if j := bytes.Index(win[:i], []byte("PRVW")); j >= 4 {
		boxStart := start + int64(j) - 4
		if sz := int64(binary.BigEndian.Uint32(win[j-4:])); sz >= 8 && boxStart+sz > jpg && boxStart+sz <= end {
			end = boxStart + sz
		}
		if hdr := win[j+4:]; jpg == boxStart+8+16 && len(hdr) >= 16 {
			w, h = int(binary.BigEndian.Uint16(hdr[6:])), int(binary.BigEndian.Uint16(hdr[8:]))
			if l := int64(binary.BigEndian.Uint32(hdr[12:])); l > 0 && l < end-jpg {
				end = jpg + l
			}
		}
	}
	p.add(jpg, end-jpg, w, h)
}

func cr3FullSize(m *bmff, moov box, p *previewSet) {
	m.children(moov.body, moov.end, 2, maxBoxesPerLevel, func(trak box) bool {
		if trak.typ != "trak" {
			return true
		}
		mdia, ok := m.find(trak.body, trak.end, 3, "mdia")
		if !ok {
			return true
		}
		minf, ok := m.find(mdia.body, mdia.end, 4, "minf")
		if !ok {
			return true
		}
		stbl, ok := m.find(minf.body, minf.end, 5, "stbl")
		if !ok {
			return true
		}
		var off, n int64 = -1, -1
		var w, h int
		m.children(stbl.body, stbl.end, 6, maxBoxesPerLevel, func(b box) bool {
			switch b.typ {
			case "stsd": // version/flags, count, first entry: size, type, 24 bytes, width, height
				if d, ok := m.payload(b, 0, 44); ok && len(d) == 44 && string(d[12:16]) == "CRAW" {
					w, h = int(binary.BigEndian.Uint16(d[40:])), int(binary.BigEndian.Uint16(d[42:]))
				}
			case "stsz": // version/flags, sample_size, sample_count, first entry size
				if d, ok := m.payload(b, 0, 16); ok && len(d) >= 12 && binary.BigEndian.Uint32(d[8:]) > 0 {
					if ss := binary.BigEndian.Uint32(d[4:]); ss != 0 {
						n = int64(ss)
					} else if len(d) == 16 {
						n = int64(binary.BigEndian.Uint32(d[12:]))
					}
				}
			case "stco":
				if d, ok := m.payload(b, 0, 12); ok && len(d) == 12 && binary.BigEndian.Uint32(d[4:]) > 0 {
					off = int64(binary.BigEndian.Uint32(d[8:]))
				}
			case "co64":
				if d, ok := m.payload(b, 0, 16); ok && len(d) == 16 && binary.BigEndian.Uint32(d[4:]) > 0 {
					if u := binary.BigEndian.Uint64(d[8:]); u <= uint64(m.size) {
						off = int64(u)
					}
				}
			}
			return true
		})
		// The first JPEG track wins; keep looking otherwise.
		return !(off >= 0 && n > 0 && p.add(off, n, w, h))
	})
}
