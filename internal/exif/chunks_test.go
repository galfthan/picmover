package exif_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"testing"

	"picmover/internal/exif"
	"picmover/internal/testimg"
)

func chunkTIFF() []byte {
	return testimg.TIFF(nil,
		testimg.IFD{Entries: []testimg.Entry{
			testimg.ASCII(0x010F, "Google"), testimg.Short(0x0112, 6), testimg.Ptr(0x8769, 2),
		}},
		testimg.IFD{Entries: []testimg.Entry{testimg.ASCII(0x9003, "2022:03:04 05:06:07")}},
	)
}

func TestWebPAndPNGExif(t *testing.T) {
	cases := map[string][]byte{
		"webp prefixed": testimg.WebPWithExif(40, 30, 1, chunkTIFF(), true),
		"webp raw tiff": testimg.WebPWithExif(40, 30, 1, chunkTIFF(), false),
		"png":           testimg.PNGWithExif(40, 30, 1, chunkTIFF()),
	}
	for name, data := range cases {
		f, err := exif.Read(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.Make != "Google" || f.Orientation != 6 || f.DateTimeOriginal != "2022:03:04 05:06:07" || len(f.Warnings) != 0 {
			t.Errorf("%s: %+v", name, f)
		}
	}
	for name, data := range map[string][]byte{"webp": testimg.WebP(20, 10, 1), "png": testimg.PNG(20, 10, 1)} {
		if _, err := exif.Read(bytes.NewReader(data), int64(len(data))); !errors.Is(err, exif.ErrNoExif) {
			t.Errorf("%s without EXIF: err %v", name, err)
		}
	}
}

func TestWebPAndPNGHostile(t *testing.T) {
	riff := func(chunks ...[]byte) []byte {
		b := []byte("RIFF\x00\x00\x00\x00WEBP")
		for _, c := range chunks {
			b = append(b, c...)
		}
		binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
		return b
	}
	png := func(chunks ...[]byte) []byte {
		b := []byte("\x89PNG\r\n\x1a\n")
		for _, c := range chunks {
			b = append(b, c...)
		}
		return b
	}
	many := func(mk func(string, []byte) []byte, typ string) [][]byte {
		var cs [][]byte
		for range 10000 {
			cs = append(cs, mk(typ, nil))
		}
		return cs
	}
	// A chunk header claiming 4 GiB.
	hugeRIFF := append([]byte("JUNK"), 0xFF, 0xFF, 0xFF, 0xFF)
	hugePNG := append([]byte{0xFF, 0xFF, 0xFF, 0xF0}, "tEXt"...)
	exifHuge := append([]byte("EXIF"), 0xFF, 0xFF, 0xFF, 0x7F)
	cases := map[string][]byte{
		"webp chunk past EOF":       riff(hugeRIFF),
		"webp EXIF past EOF":        riff(append(exifHuge, chunkTIFF()...)),
		"webp 10k chunks":           riff(append(many(testimg.RIFFChunk, "JUNK"), testimg.RIFFChunk("EXIF", chunkTIFF()))...),
		"webp RIFF size lies":       append(riff(testimg.RIFFChunk("JUNK", make([]byte, 7))), 0xFF),
		"png chunk past EOF":        png(hugePNG),
		"png eXIf past EOF":         png(append([]byte{0xFF, 0xFF, 0xFF, 0xF0}, append([]byte("eXIf"), chunkTIFF()...)...)),
		"png 10k chunks":            png(append(many(testimg.PNGChunk, "tEXt"), testimg.PNGChunk("eXIf", chunkTIFF()))...),
		"png eXIf after IDAT":       png(testimg.PNGChunk("IDAT", []byte{1}), testimg.PNGChunk("eXIf", chunkTIFF())),
		"png eXIf garbage":          png(testimg.PNGChunk("eXIf", []byte("not a tiff at all"))),
		"webp EXIF only the prefix": riff(testimg.RIFFChunk("EXIF", []byte("Exif\x00\x00"))),
	}
	for name, data := range cases {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f, err := exif.Read(bytes.NewReader(data), int64(len(data)))
		runtime.ReadMemStats(&after)
		if a := after.TotalAlloc - before.TotalAlloc; a > 1<<20 {
			t.Errorf("%s: allocated %d bytes", name, a)
		}
		switch name {
		case "webp EXIF past EOF", "png eXIf past EOF":
			// Truncated chunk: parsed as far as the file goes.
			if err != nil || f.Make != "Google" {
				t.Errorf("%s: %v %+v", name, err, f)
			}
		case "webp 10k chunks", "png 10k chunks":
			// The EXIF chunk is beyond the chunk cap.
			if err == nil && f.Make != "" {
				t.Errorf("%s: read past the chunk cap: %+v", name, f)
			}
		default:
			if err == nil && f.Make != "" {
				t.Errorf("%s: unexpected fields %+v", name, f)
			}
		}
	}
}

func TestHasBox(t *testing.T) {
	ftyp := testimg.Box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))
	meta := testimg.Box("meta", make([]byte, 20))
	moov := testimg.Box("moov", testimg.Box("trak"))
	large := append(binary.BigEndian.AppendUint32(nil, 1), "mdat"...)
	large = binary.BigEndian.AppendUint64(large, 24)
	large = append(large, make([]byte, 8)...)
	var many []byte
	for range 100 {
		many = append(many, testimg.Box("free")...)
	}
	cases := []struct {
		name  string
		data  []byte
		found bool
		err   bool
	}{
		{"still", cat(ftyp, meta), false, false},
		{"sequence", cat(ftyp, meta, moov), true, false},
		{"after 64-bit box", cat(ftyp, large, moov), true, false},
		{"size 0 last", cat(ftyp, []byte{0, 0, 0, 0}, []byte("moov"), make([]byte, 4)), true, false},
		{"past EOF", cat(ftyp, []byte{0x7F, 0, 0, 0}, []byte("mdat")), false, true},
		{"smaller than header", cat(ftyp, []byte{0, 0, 0, 4}, []byte("mdat")), false, true},
		{"trailing bytes", cat(ftyp, []byte{1, 2, 3}), false, true},
		{"too many", cat(ftyp, many, moov), false, true},
	}
	for _, c := range cases {
		found, err := exif.HasBox(bytes.NewReader(c.data), int64(len(c.data)), "moov")
		if found != c.found || (err != nil) != c.err {
			t.Errorf("%s: found %v err %v", c.name, found, err)
		}
	}
	if _, err := exif.HasBox(bytes.NewReader(cat(ftyp, many)), int64(len(ftyp)+len(many)), "moov"); !errors.Is(err, exif.ErrBoxLimit) {
		t.Errorf("box limit: %v", err)
	}
}

func cat(bs ...[]byte) []byte {
	var out []byte
	for _, b := range bs {
		out = append(out, b...)
	}
	return out
}
