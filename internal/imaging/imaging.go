// Package imaging decodes still images in pure Go (WebAssembly ports via
// wazero for HEIF and AVIF), bounded by a pixel budget checked before any
// pixel memory is allocated, plausibility limits on the declared size, an
// optional shared memory budget (MemBudget) and an optional cap on
// concurrent HEIF decodes. It also applies EXIF orientation, scales
// images down, encodes JPEG and computes perceptual hashes.
//
// The gen2brain packages must be built with -tags nodynamic so that only
// their WebAssembly backends are compiled in.
package imaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/heic"
	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"

	"picmover/internal/exif"
)

// Format is the sniffed container format.
type Format string

// Known formats. FormatUnknown is the empty string.
const (
	FormatUnknown Format = ""
	FormatJPEG    Format = "jpeg"
	FormatPNG     Format = "png"
	FormatGIF     Format = "gif"
	FormatWebP    Format = "webp"
	FormatBMP     Format = "bmp"
	FormatTIFF    Format = "tiff"
	FormatHEIF    Format = "heif"
	FormatAVIF    Format = "avif"
)

// DefaultMaxPixels is the pixel budget used when a caller passes 0.
const DefaultMaxPixels = 100_000_000

// Errors returned by this package; test with errors.Is.
var (
	ErrUnsupported = errors.New("imaging: unsupported format")
	ErrTooLarge    = errors.New("imaging: image too large")
	ErrBadSize     = errors.New("imaging: invalid image dimensions")
)

const sniffLen = 64

// Sniff reads at most 64 bytes from r and reports the format.
func Sniff(r io.ReaderAt) Format {
	var buf [sniffLen]byte
	n, _ := r.ReadAt(buf[:], 0)
	return sniff(buf[:n])
}

func sniff(b []byte) Format {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return FormatJPEG
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return FormatPNG
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return FormatGIF
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return FormatWebP
	case len(b) >= 14 && b[0] == 'B' && b[1] == 'M':
		return FormatBMP
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		return FormatTIFF
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return sniffBMFF(b)
	}
	return FormatUnknown
}

// sniffBMFF classifies an ISO BMFF file by its ftyp brands. The major brand
// wins; otherwise the compatible brands present in the sniffed prefix decide.
func sniffBMFF(b []byte) Format {
	size := int(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
	if size < 16 || size > len(b) {
		size = len(b)
	}
	brands := []string{string(b[8:12])}
	for i := 16; i+4 <= size; i += 4 {
		brands = append(brands, string(b[i:i+4]))
	}
	if f := brandFormat(brands[0]); f != FormatUnknown {
		return f
	}
	var generic bool
	for _, br := range brands[1:] {
		switch f := brandFormat(br); f {
		case FormatAVIF, FormatHEIF:
			return f
		}
		if br == "mif1" || br == "msf1" {
			generic = true
		}
	}
	if generic && (brands[0] == "mif1" || brands[0] == "msf1") {
		return FormatHEIF
	}
	return FormatUnknown
}

func brandFormat(br string) Format {
	switch br {
	case "avif", "avis":
		return FormatAVIF
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs":
		return FormatHEIF
	}
	return FormatUnknown
}

// guard runs f and converts a panic into an error; the WebAssembly
// decoders in particular may panic on hostile input, and the WebP
// encoder is guarded the same way.
func guard[T any](f func() (T, error)) (v T, err error) {
	defer func() {
		if p := recover(); p != nil {
			var zero T
			v, err = zero, fmt.Errorf("imaging: codec panic: %v", p)
		}
	}()
	return f()
}

// heifSequence refuses HEIF image sequences before any gen2brain/heic call.
//
// gen2brain/heic v0.7.2 parses a top-level moov box itself (sequence.go)
// whenever one is present: parseStsz/parseStts allocate slices sized by
// 32-bit sample counts read from the file, so a few hundred bytes can ask
// for tens of GiB (a fatal, unrecoverable out-of-memory error, not a
// panic), and Decode then decodes every frame (DecodeAll) to return the
// first, with the size reported by DecodeConfig taken from the sample
// entry rather than from what is decoded. Still photos never carry a moov
// box, so any file with one is reported as unsupported. The walk is
// bounded (exif.HasBox: 64 top-level boxes, sizes validated). A malformed
// box ends the walk: the upstream walker stops at the same box, so it
// cannot reach a moov behind it. Too many boxes is refused, since the
// upstream walker has no such cap.
func heifSequence(r io.ReaderAt, size int64) error {
	found, err := exif.HasBox(r, size, "moov")
	switch {
	case found:
		return fmt.Errorf("%w: HEIF image sequence", ErrUnsupported)
	case errors.Is(err, exif.ErrBoxLimit):
		return fmt.Errorf("%w: HEIF with too many top-level boxes", ErrUnsupported)
	}
	return nil
}

// Config returns dimensions without decoding pixels, when the format allows.
// HEIF image sequences are refused with ErrUnsupported.
func Config(r io.ReaderAt, size int64) (w, h int, f Format, err error) {
	f = Sniff(r)
	if f == FormatUnknown {
		return 0, 0, f, ErrUnsupported
	}
	if f == FormatHEIF {
		if err := heifSequence(r, size); err != nil {
			return 0, 0, f, err
		}
	}
	sr := io.NewSectionReader(r, 0, size)
	cfg, err := guard(func() (image.Config, error) {
		switch f {
		case FormatJPEG:
			return jpeg.DecodeConfig(sr)
		case FormatPNG:
			return png.DecodeConfig(sr)
		case FormatGIF:
			return gif.DecodeConfig(sr)
		case FormatWebP:
			return webp.DecodeConfig(sr)
		case FormatBMP:
			return bmp.DecodeConfig(sr)
		case FormatTIFF:
			return tiff.DecodeConfig(sr)
		case FormatHEIF:
			return heic.DecodeConfig(sr)
		case FormatAVIF:
			return avif.DecodeConfig(sr)
		}
		return image.Config{}, ErrUnsupported
	})
	if err != nil {
		return 0, 0, f, fmt.Errorf("imaging: %s config: %w", f, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, f, fmt.Errorf("%w: %dx%d", ErrBadSize, cfg.Width, cfg.Height)
	}
	return cfg.Width, cfg.Height, f, nil
}

// Known is the result of an earlier Config call on the same file.
type Known struct {
	W, H   int
	Format Format
}

// Options control Decode and DecodeFit.
type Options struct {
	// MaxPixels is the pixel budget; 0 = DefaultMaxPixels. It is checked
	// against the declared size before decoding and against the decoded
	// image afterwards.
	MaxPixels int64
	// Budget, when non-nil, bounds the memory of concurrent decodes: each
	// decode holds declared width*height*4 bytes (x8 for 16-bit PNG; x2
	// for HEIF and AVIF, whose WebAssembly decoders hold a second copy)
	// while it runs.
	Budget *MemBudget
	// HEIFSlots, when non-nil, bounds concurrent HEIF decodes: each takes
	// one value-slot (a send) for its duration. Its capacity is the limit.
	HEIFSlots chan struct{}
	// Known, when non-nil with a format and positive size, is trusted
	// instead of running Config again (no re-sniff, no second header
	// read; for HEIF no second WebAssembly instantiation). The pixel
	// budget and every other check still apply.
	Known *Known
}

func (o Options) maxPixels() int64 {
	if o.MaxPixels <= 0 {
		return DefaultMaxPixels
	}
	return o.MaxPixels
}

// Plausibility limits. A JPEG cannot describe more than ~1024 pixels per
// byte of input (jpegli applies the same limit). For PNG the bound is on
// the raw (filtered, uncompressed) image data the IHDR declares, which
// deflate cannot produce at more than ~1032 bytes per compressed byte; a
// pixel bound would refuse legitimate low-bit-depth images (a 1-bit scan
// packs 8 pixels into a raw byte). Above these limits the file is a header
// that lies about its size, and the standard decoders would allocate the
// full image before failing.
const (
	jpegPixelsPerByte = 1024
	pngRawPerByte     = 1100
)

// pngIHDR is the part of a PNG's IHDR chunk that the plausibility check
// and the memory estimate need.
type pngIHDR struct {
	w, h      uint32
	depth     uint8
	colorType uint8
}

// readPNGIHDR reads the IHDR from the first 33 bytes of a PNG (signature,
// chunk length and type, 13 bytes of data, CRC). ok is false when the file
// is short, the first chunk is not a 13-byte IHDR, or the bit depth or
// colour type is invalid; the caller then leaves the file to the decoder,
// which reports its own error. Nothing proportional to the declared size
// is allocated.
func readPNGIHDR(r io.ReaderAt) (hdr pngIHDR, ok bool) {
	var b [33]byte
	if n, _ := r.ReadAt(b[:], 0); n < len(b) {
		return hdr, false
	}
	if string(b[:8]) != "\x89PNG\r\n\x1a\n" || binary.BigEndian.Uint32(b[8:]) != 13 || string(b[12:16]) != "IHDR" {
		return hdr, false
	}
	hdr = pngIHDR{
		w:         binary.BigEndian.Uint32(b[16:]),
		h:         binary.BigEndian.Uint32(b[20:]),
		depth:     b[24],
		colorType: b[25],
	}
	if pngChannels(hdr.colorType) == 0 {
		return hdr, false
	}
	switch hdr.depth {
	case 1, 2, 4, 8, 16:
	default:
		return hdr, false
	}
	return hdr, true
}

// pngChannels returns the samples per pixel of a PNG colour type, or 0 for
// an invalid one.
func pngChannels(colorType uint8) uint64 {
	switch colorType {
	case 0, 3: // gray, palette
		return 1
	case 2: // RGB
		return 3
	case 4: // gray + alpha
		return 2
	case 6: // RGBA
		return 4
	}
	return 0
}

// rawBytes is the size of the declared image's filtered scanlines: one
// filter byte plus ceil(width*channels*depth/8) bytes per row. Interlaced
// images are slightly larger (a filter byte per sub-image row); the same
// bound is applied. The row size fits in a uint64 (at most 2^32*64/8+1);
// the product with the height may not, so it is taken in float64.
func (p pngIHDR) rawBytes() float64 {
	row := (uint64(p.w)*pngChannels(p.colorType)*uint64(p.depth) + 7) / 8
	return float64(p.h) * float64(1+row)
}

// prepare returns the declared size and format of the image and the memory
// estimate for decoding it, and checks the pixel budget, the plausibility
// limits and (for HEIF) the sequence ban.
func prepare(r io.ReaderAt, size int64, opts Options) (w, h int, f Format, mem int64, err error) {
	if k := opts.Known; k != nil && k.Format != FormatUnknown && k.W > 0 && k.H > 0 {
		w, h, f = k.W, k.H, k.Format
		if f == FormatHEIF {
			if err := heifSequence(r, size); err != nil {
				return 0, 0, f, 0, err
			}
		}
	} else if w, h, f, err = Config(r, size); err != nil {
		return 0, 0, f, 0, err
	}
	px := int64(w) * int64(h)
	if limit := opts.maxPixels(); px > limit {
		return w, h, f, 0, fmt.Errorf("%w: %dx%d (%d pixels, limit %d)", ErrTooLarge, w, h, px, limit)
	}
	// The decoded Go image: 4 bytes per pixel (8 for 16-bit PNG), x2 for
	// HEIF and AVIF, whose WebAssembly decoders hold a second copy.
	mem = px * 4
	switch f {
	case FormatJPEG:
		if px > jpegPixelsPerByte*size {
			return w, h, f, 0, fmt.Errorf("%w: %dx%d (%d pixels) from a %d-byte %s file", ErrTooLarge, w, h, px, size, f)
		}
	case FormatPNG:
		if hdr, ok := readPNGIHDR(r); ok {
			if raw := hdr.rawBytes(); raw > pngRawPerByte*float64(size) {
				return w, h, f, 0, fmt.Errorf("%w: %dx%d (%.0f raw bytes) from a %d-byte %s file", ErrTooLarge, w, h, raw, size, f)
			}
			if hdr.depth == 16 {
				mem = px * 8
			}
		}
	case FormatHEIF, FormatAVIF:
		mem *= 2
	}
	return w, h, f, mem, nil
}

// acquire takes a HEIF slot (for HEIF) and mem bytes of the memory budget;
// release undoes both.
func acquire(ctx context.Context, opts Options, f Format, mem int64) (release func(), err error) {
	slot := false
	if f == FormatHEIF && opts.HEIFSlots != nil {
		select {
		case opts.HEIFSlots <- struct{}{}:
			slot = true
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := opts.Budget.Acquire(ctx, mem); err != nil {
		if slot {
			<-opts.HEIFSlots
		}
		return nil, err
	}
	return func() {
		opts.Budget.Release(mem)
		if slot {
			<-opts.HEIFSlots
		}
	}, nil
}

// Decode decodes the whole image. It refuses images larger than
// opts.MaxPixels (0 = DefaultMaxPixels, 100e6) before allocating anything
// (the dimensions come from Config, which reads headers only, or from
// opts.Known), refuses JPEG and PNG headers that are implausible for the
// file size, and drops a decoded image that turns out larger than the
// budget (a decoder may produce a size other than the declared one).
// HEIF image sequences are refused with ErrUnsupported.
func Decode(ctx context.Context, r io.ReaderAt, size int64, opts Options) (image.Image, Format, error) {
	return decode(ctx, r, size, 0, opts)
}

func decode(ctx context.Context, r io.ReaderAt, size int64, fitMax int, opts Options) (image.Image, Format, error) {
	if err := ctx.Err(); err != nil {
		return nil, FormatUnknown, err
	}
	w, h, f, mem, err := prepare(r, size, opts)
	if err != nil {
		return nil, f, err
	}
	release, err := acquire(ctx, opts, f, mem)
	if err != nil {
		return nil, f, err
	}
	defer release()
	var img image.Image
	if f == FormatJPEG && fitMax > 0 && max(w, h) > 2*fitMax {
		img, err = decodeJPEGScaled(ctx, io.NewSectionReader(r, 0, size), w, h, fitMax)
		if err != nil {
			return nil, f, err
		}
	}
	if img == nil {
		if err := ctx.Err(); err != nil {
			return nil, f, err
		}
		if img, err = decodeFull(f, io.NewSectionReader(r, 0, size)); err != nil {
			return nil, f, err
		}
	}
	b := img.Bounds()
	if px, limit := int64(b.Dx())*int64(b.Dy()), opts.maxPixels(); px > limit {
		return nil, f, fmt.Errorf("%w: decoded %dx%d (%d pixels, limit %d; declared %dx%d)", ErrTooLarge, b.Dx(), b.Dy(), px, limit, w, h)
	}
	if err := ctx.Err(); err != nil {
		return nil, f, err
	}
	return img, f, nil
}

func decodeFull(f Format, sr io.Reader) (image.Image, error) {
	img, err := guard(func() (image.Image, error) {
		switch f {
		case FormatJPEG:
			return jpeg.Decode(sr)
		case FormatPNG:
			return png.Decode(sr)
		case FormatGIF:
			return gif.Decode(sr)
		case FormatWebP:
			return webp.Decode(sr)
		case FormatBMP:
			return bmp.Decode(sr)
		case FormatTIFF:
			return tiff.Decode(sr)
		case FormatHEIF:
			return heic.Decode(sr)
		case FormatAVIF:
			return avif.Decode(sr)
		}
		return nil, ErrUnsupported
	})
	if err != nil {
		return nil, fmt.Errorf("imaging: decode %s: %w", f, err)
	}
	if img == nil || img.Bounds().Empty() {
		return nil, fmt.Errorf("imaging: decode %s: %w", f, ErrBadSize)
	}
	return img, nil
}

// EncodeJPEG writes img as a baseline JPEG at the given quality (clamped to
// 1..100).
func EncodeJPEG(w io.Writer, img image.Image, quality int) error {
	quality = max(1, min(quality, 100))
	if err := jpeg.Encode(w, img, &jpeg.Options{Quality: quality}); err != nil {
		return fmt.Errorf("imaging: encode jpeg: %w", err)
	}
	return nil
}

// toRGBA returns img as an *image.RGBA with bounds starting at (0,0),
// converting when necessary.
func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	if m, ok := img.(*image.RGBA); ok && b.Min == (image.Point{}) {
		return m
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// Orient applies an EXIF orientation (1..8) and returns the upright image.
// Orientation 1 and values outside 1..8 return img unchanged.
//
//	2 flip horizontal   3 rotate 180      4 flip vertical
//	5 transpose         6 rotate 90 CW    7 transverse     8 rotate 270 CW
func Orient(img image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return img
	}
	src := toRGBA(img)
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride : y*src.Stride+4*w]
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			o := dy*dst.Stride + 4*dx
			copy(dst.Pix[o:o+4], row[4*x:4*x+4])
		}
	}
	return dst
}

// fitSize returns the size of a w x h image scaled so that its longer side
// is limit, preserving aspect (each side at least 1).
func fitSize(w, h, limit int) (int, int) {
	if w >= h {
		return limit, max(1, int((int64(h)*int64(limit)+int64(w)/2)/int64(w)))
	}
	return max(1, int((int64(w)*int64(limit)+int64(h)/2)/int64(h))), limit
}

// Fit returns img scaled so that its longer side is at most limit pixels
// (never upscaled). When img already fits (or limit <= 0) img itself is
// returned. Reductions of 2x or more use an integer box downsample first
// (which does the anti-aliasing), then draw.BiLinear for the final step, a
// reduction of less than 2x where BiLinear is visually indistinguishable
// from CatmullRom and much cheaper. Reductions of less than 2x from the
// start (no box step) use draw.CatmullRom. Time stays roughly linear in the
// source pixel count.
func Fit(img image.Image, limit int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if limit <= 0 || (w <= limit && h <= limit) {
		return img
	}
	tw, th := fitSize(w, h, limit)
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	if k := min(w/tw, h/th); k >= 2 {
		src := boxDown(img, k)
		draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		return dst
	}
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
