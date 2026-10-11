package imaging

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/gen2brain/webp"

	"picmover/internal/testimg"
)

func TestEncodeWebPRoundTrip(t *testing.T) {
	src := scene(320, 200, 0)
	// A sub-image with an offset and a wide stride must be packed first.
	big := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 200; y++ {
		for x := 0; x < 320; x++ {
			big.Set(x+10, y+20, src.At(x, y))
		}
	}
	transparent := image.NewNRGBA(image.Rect(0, 0, 320, 200))
	transparent.Set(5, 5, color.NRGBA{255, 0, 0, 128})
	cases := []struct {
		name  string
		img   image.Image
		phash bool
	}{
		{"rgba", src, true},
		{"subimage", big.SubImage(image.Rect(10, 20, 330, 220)), true},
		{"ycbcr", image.NewYCbCr(image.Rect(0, 0, 320, 200), image.YCbCrSubsampleRatio420), false},
		{"transparent", transparent, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := EncodeWebP(&buf, c.img, 85); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			if f := Sniff(bytes.NewReader(data)); f != FormatWebP {
				t.Fatalf("Sniff = %q", f)
			}
			// The decoder path (x/image/webp, chosen by sniffing) reads it.
			got, f, err := DecodeFit(context.Background(), bytes.NewReader(data), int64(len(data)), 256, Options{})
			if err != nil || f != FormatWebP {
				t.Fatalf("DecodeFit: %v %q", err, f)
			}
			if b := got.Bounds(); b.Dx() != 320 || b.Dy() != 200 {
				t.Fatalf("decoded %v", b)
			}
			if c.phash {
				if d := Distance(PHash(got), PHash(src)); d > 4 {
					t.Errorf("pHash moved %d bits", d)
				}
			}
		})
	}
}

// TestDecodeFitWebPPreview decodes a WebP written by another encoder (the
// test fixture uses a different method and quality) through the same path
// as a stored preview.
func TestDecodeFitWebPPreview(t *testing.T) {
	data := testimg.WebP(1600, 1000, 3)
	img, f, err := DecodeFit(context.Background(), bytes.NewReader(data), int64(len(data)), 640, Options{})
	if err != nil || f != FormatWebP {
		t.Fatalf("DecodeFit: %v %q", err, f)
	}
	if b := Fit(img, 640).Bounds(); b.Dx() != 640 || b.Dy() != 400 {
		t.Fatalf("fit %v", b)
	}
}

// BenchmarkEncodeWebP compares libwebp methods at the preview size against
// the standard JPEG encoder, on camera-like content (gradients plus noise).
func BenchmarkEncodeWebP(b *testing.B) {
	photo := testimg.BigJPEG(1600, 1067)
	src, _, err := Decode(context.Background(), bytes.NewReader(photo), int64(len(photo)), Options{})
	if err != nil {
		b.Fatal(err)
	}
	for _, m := range []int{0, 2, 4} {
		b.Run(fmt.Sprintf("method%d", m), func(b *testing.B) {
			var n int
			for b.Loop() {
				var buf bytes.Buffer
				if err := webp.Encode(&buf, src, webp.Options{Quality: 85, Method: m}); err != nil {
					b.Fatal(err)
				}
				n = buf.Len()
			}
			b.ReportMetric(float64(n), "bytes")
		})
	}
	b.Run("jpeg", func(b *testing.B) {
		var n int
		for b.Loop() {
			var buf bytes.Buffer
			if err := EncodeJPEG(&buf, src, 85); err != nil {
				b.Fatal(err)
			}
			n = buf.Len()
		}
		b.ReportMetric(float64(n), "bytes")
	})
}
