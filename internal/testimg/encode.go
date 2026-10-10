package testimg

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// The encoders below build test fixtures in memory. The gen2brain packages
// must be compiled with -tags nodynamic (WebAssembly backend only).

func rgba(w, h int, seed byte) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, seed)
	return img
}

// WebP returns a lossy WebP file of the given size, encoded with
// github.com/gen2brain/webp.
func WebP(w, h int, seed byte) []byte {
	var buf bytes.Buffer
	if err := webp.Encode(&buf, rgba(w, h, seed), webp.Options{Quality: 90, Method: 0}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// AVIF returns an AVIF file of the given size, encoded with
// github.com/gen2brain/avif at its fastest speed.
func AVIF(w, h int, seed byte) []byte {
	var buf bytes.Buffer
	if err := avif.Encode(&buf, rgba(w, h, seed), avif.Options{Quality: 80, Speed: 10}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// GIF returns a single-frame GIF file of the given size (Plan 9 palette).
func GIF(w, h int, seed byte) []byte {
	var buf bytes.Buffer
	if err := gif.Encode(&buf, rgba(w, h, seed), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// BMP returns a 24-bit BMP file of the given size.
func BMP(w, h int, seed byte) []byte {
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, rgba(w, h, seed)); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// TIFFImage returns an uncompressed RGBA TIFF image file of the given size.
// (The name TIFF is taken by the TIFF structure builder in testimg.go.)
func TIFFImage(w, h int, seed byte) []byte {
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, rgba(w, h, seed), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// BigJPEG returns a w x h baseline JPEG (4:2:0, quality 85) with smooth
// gradient content plus pseudo-random sensor-like noise, so it compresses to
// a size (and decode cost) similar to a camera photo.
// Intended for benchmarks; keep it in memory, never write it to disk.
func BigJPEG(w, h int) []byte {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	rnd := uint32(2463534242)
	noise := func() int {
		rnd ^= rnd << 13
		rnd ^= rnd >> 17
		rnd ^= rnd << 5
		return int(rnd&15) - 8
	}
	for y := 0; y < h; y++ {
		row := img.Y[y*img.YStride : y*img.YStride+w]
		for x := range row {
			row[x] = byte(16 + (x*180)/w + (y*40)/h + noise())
		}
	}
	cw, ch := (w+1)/2, (h+1)/2
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			img.Cb[y*img.CStride+x] = byte(64 + (x*128)/cw)
			img.Cr[y*img.CStride+x] = byte(64 + (y*128)/ch)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// WebPWithExif returns WebP(w, h, seed) with an EXIF chunk holding tiff
// (prefixed with "Exif\0\0" when prefix is set) appended to the RIFF
// container. The VP8X header's EXIF flag is set, inserting a VP8X chunk
// when the encoder wrote the simple format; the RIFF size is updated.
func WebPWithExif(w, h int, seed byte, tiff []byte, prefix bool) []byte {
	src := WebP(w, h, seed)
	body := src[12:]
	var out []byte
	out = append(out, "RIFF\x00\x00\x00\x00WEBP"...)
	if string(body[0:4]) == "VP8X" {
		vp8x := append([]byte(nil), body[:18]...)
		vp8x[8] |= 0x08
		out = append(out, vp8x...)
		body = body[18:]
	} else {
		out = append(out, "VP8X"...)
		out = binary.LittleEndian.AppendUint32(out, 10)
		out = append(out, 0x08, 0, 0, 0)
		out = append(out, le24(uint32(w-1))...)
		out = append(out, le24(uint32(h-1))...)
	}
	out = append(out, body...)
	payload := tiff
	if prefix {
		payload = append([]byte("Exif\x00\x00"), tiff...)
	}
	out = append(out, RIFFChunk("EXIF", payload)...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

// RIFFChunk returns a RIFF chunk (little-endian size, padded to even).
func RIFFChunk(id string, payload []byte) []byte {
	b := append([]byte(id), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...)
	b = append(b, payload...)
	if len(payload)&1 == 1 {
		b = append(b, 0)
	}
	return b
}

func le24(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16)} }

// PNGWithExif returns PNG(w, h, seed) with an eXIf chunk holding tiff
// inserted directly after IHDR.
func PNGWithExif(w, h int, seed byte, tiff []byte) []byte {
	src := PNG(w, h, seed)
	const afterIHDR = 8 + 4 + 4 + 13 + 4
	out := append([]byte(nil), src[:afterIHDR]...)
	out = append(out, PNGChunk("eXIf", tiff)...)
	return append(out, src[afterIHDR:]...)
}

// PNGChunk returns a PNG chunk with a valid CRC.
func PNGChunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}
