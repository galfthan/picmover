package exif

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// maxChunks caps the RIFF (WebP) and PNG chunks visited per file.
const maxChunks = 64

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func isWebP(h []byte) bool {
	return len(h) >= 12 && string(h[0:4]) == "RIFF" && string(h[8:12]) == "WEBP"
}

// exifPayload parses an EXIF payload of n bytes at off: a TIFF structure,
// optionally preceded by "Exif\0\0".
func (s *state) exifPayload(r io.ReaderAt, size, off, n int64) error {
	if p, err := readAt(r, size, off, min(n, 6)); err == nil && bytes.Equal(p, exifHeader) {
		off, n = off+6, n-6
	}
	return s.tiff(io.NewSectionReader(r, off, n), n, tiffMain, false)
}

// webp walks the chunks of a RIFF/WEBP container and parses the EXIF chunk.
// Chunk sizes are little-endian; odd-sized chunks are padded to even.
func (s *state) webp(r io.ReaderAt, size int64) error {
	end := size
	if h, err := readAt(r, size, 4, 4); err == nil {
		if riff := int64(binary.LittleEndian.Uint32(h)) + 8; riff < end {
			end = riff
		}
	}
	pos := int64(12)
	for i := 0; pos+8 <= end; i++ {
		if i >= maxChunks {
			s.warn("WebP: more than %d chunks, rest ignored", maxChunks)
			break
		}
		h, err := readAt(r, size, pos, 8)
		if err != nil {
			break
		}
		id, n := string(h[0:4]), int64(binary.LittleEndian.Uint32(h[4:8]))
		body := pos + 8
		if n > end-body {
			if id != "EXIF" {
				s.warn("WebP: chunk %q at %d runs past the end of the file", id, pos)
				break
			}
			s.warn("WebP: EXIF chunk truncated")
			n = end - body
		}
		if id == "EXIF" {
			return s.exifPayload(r, size, body, n)
		}
		pos = body + n + n&1
	}
	return ErrNoExif
}

// png walks PNG chunks up to the first IDAT and parses an eXIf chunk (a
// raw TIFF structure). Chunk lengths are big-endian; the CRC is not
// checked.
func (s *state) png(r io.ReaderAt, size int64) error {
	pos := int64(len(pngMagic))
	for i := 0; pos+12 <= size; i++ {
		if i >= maxChunks {
			s.warn("PNG: more than %d chunks before IDAT, rest ignored", maxChunks)
			break
		}
		h, err := readAt(r, size, pos, 8)
		if err != nil {
			break
		}
		n, typ := int64(binary.BigEndian.Uint32(h[0:4])), string(h[4:8])
		body := pos + 8
		if typ == "IDAT" || typ == "IEND" {
			break
		}
		if n > size-body {
			if typ != "eXIf" {
				s.warn("PNG: chunk %q at %d runs past the end of the file", typ, pos)
				break
			}
			s.warn("PNG: eXIf chunk truncated")
			n = size - body
		}
		if typ == "eXIf" {
			return s.exifPayload(r, size, body, n)
		}
		pos = body + n + 4
	}
	return fmt.Errorf("%w: no eXIf chunk before image data", ErrNoExif)
}
