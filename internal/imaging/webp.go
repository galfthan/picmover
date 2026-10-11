package imaging

import (
	"fmt"
	"image"
	"io"

	"github.com/gen2brain/webp"
	"golang.org/x/image/draw"
)

// WebPMethod is the libwebp speed/size trade-off (0 fastest .. 6 smallest)
// EncodeWebP uses. The encoder runs as WebAssembly translated to Go, so
// the method dominates the thumbnail stage's cost: on a 1600 px photo
// method 0 takes about half the time of method 2 and a fifth of the
// default 4, and its files are at most about a third larger (on noisy
// content often smaller). See BenchmarkEncodeWebP and
// docs/design/renditions.md.
const WebPMethod = 0

// EncodeWebP writes img as a lossy WebP at the given quality (clamped to
// 1..100) with github.com/gen2brain/webp (WebAssembly; build with -tags
// nodynamic). The pixels are passed as tightly packed RGBA; an image with
// transparency is encoded with its alpha. A panic in the encoder is
// returned as an error (as for the decoders, see guard).
func EncodeWebP(w io.Writer, img image.Image, quality int) error {
	quality = max(1, min(quality, 100))
	_, err := guard(func() (struct{}, error) {
		return struct{}{}, webpEncode(w, packedRGBA(img), webp.Options{Quality: quality, Method: WebPMethod})
	})
	if err != nil {
		return fmt.Errorf("imaging: encode webp: %w", err)
	}
	return nil
}

// webpEncode is the encoder EncodeWebP calls; tests replace it.
var webpEncode = webp.Encode

// packedRGBA returns img as an *image.RGBA whose bounds start at (0,0) and
// whose stride is exactly 4*width: the encoder reads Pix as one block of
// width*height pixels.
func packedRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	if m, ok := img.(*image.RGBA); ok && b.Min == (image.Point{}) && m.Stride == 4*b.Dx() {
		return m
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}
