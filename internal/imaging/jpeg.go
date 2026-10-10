package imaging

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/gen2brain/jpegli"
)

// DecodeFit decodes an image that the caller will reduce to at most fitMax
// pixels on its longer side. For a JPEG whose longer side is more than 2x
// fitMax it decodes at a reduced DCT scale with jpegli, which is much
// faster and smaller than a full decode; every other case goes through the
// same path as Decode, as does a jpegli failure other than ErrTooLarge and
// a CMYK result. All of Decode's checks (pixel budget before and after,
// plausibility, memory budget, HEIF slots, HEIF sequences) apply. The
// result may still be larger than fitMax (it is never smaller on its
// longer side unless the source itself is); callers finish with Fit.
func DecodeFit(ctx context.Context, r io.ReaderAt, size int64, fitMax int, opts Options) (image.Image, Format, error) {
	return decode(ctx, r, size, fitMax, opts)
}

// decodeJPEGScaled decodes a w x h JPEG with jpegli at a reduced DCT scale
// for a target of fitMax on the longer side. It returns (nil, nil) when
// the caller should fall back to the standard decoder, and an error only
// when the image must be refused (jpegli's ErrTooLarge, which the standard
// decoder must not then be given) or ctx ended.
//
// CMYK (and YCCK) JPEGs are not taken from jpegli: it returns the raw
// *image.CMYK samples without the inversion that Adobe APP14 files
// (Photoshop) need, while image/jpeg applies it. Such a result is dropped
// and the standard decoder used.
//
// Observed behaviour of jpegli v0.4.2 DecodingOptions.ScaleTarget (see
// lib/jpegli.c and TestJpegliScaleTarget): it does not resize to the exact
// target. It picks the smallest libjpeg DCT scale N/8 (N = 1..8) for which
// ceil(N*W/8) >= target width and ceil(N*H/8) >= target height, and decodes
// at that scale; when no N < 8 qualifies it decodes at full size. So the
// output is always at least the target in both dimensions, and e.g. a
// 6000x4000 source with a 1500x1000 target yields exactly 1500x1000 (1/4),
// with 750x500 yields 750x500 (1/8), and with 1600x1067 yields 2250x1500
// (3/8). Scaled output is an *image.RGBA.
//
// jpegli writes libjpeg's warnings and errors for damaged files to the
// process's stderr; there is no option to silence them.
func decodeJPEGScaled(ctx context.Context, r io.Reader, w, h, fitMax int) (image.Image, error) {
	tw, th := fitSize(w, h, fitMax)
	img, err := jpegliScaled(r, tw, th)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch {
	case errors.Is(err, jpegli.ErrTooLarge):
		return nil, fmt.Errorf("%w: jpegli: %v", ErrTooLarge, err)
	case err != nil:
		return nil, nil
	}
	if _, cmyk := img.(*image.CMYK); cmyk {
		return nil, nil
	}
	if b := img.Bounds(); b.Empty() || b.Dx() < min(tw, w) || b.Dy() < min(th, h) {
		return nil, nil // unexpectedly small result
	}
	return img, nil
}

// jpegliScaled runs jpegli with ScaleTarget tw x th (see decodeJPEGScaled).
func jpegliScaled(r io.Reader, tw, th int) (image.Image, error) {
	img, err := guard(func() (image.Image, error) {
		return jpegli.DecodeWithOptions(r, &jpegli.DecodingOptions{
			ScaleTarget: image.Rect(0, 0, tw, th),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("imaging: jpegli decode: %w", err)
	}
	if img == nil || img.Bounds().Empty() {
		return nil, fmt.Errorf("imaging: jpegli decode: %w", ErrBadSize)
	}
	return img, nil
}
