package imaging

import (
	"bytes"
	"context"
	"image"
	"sync"
	"testing"

	"picmover/internal/testimg"
)

// The 24 MP fixture is generated in memory once per test binary; it is
// never written to disk.
var big24 = sync.OnceValue(func() []byte { return testimg.BigJPEG(6000, 4000) })

func benchJPEG(b *testing.B, tw, th int) {
	data := big24()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		var (
			img image.Image
			err error
		)
		if tw == 0 {
			img, _, err = Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{})
		} else {
			img, err = jpegliScaled(bytes.NewReader(data), tw, th)
		}
		if err != nil {
			b.Fatal(err)
		}
		_ = img
	}
}

func BenchmarkDecodeStd24MP(b *testing.B)            { benchJPEG(b, 0, 0) }
func BenchmarkDecodeJpegli24MP_Quarter(b *testing.B) { benchJPEG(b, 1500, 1000) }
func BenchmarkDecodeJpegli24MP_Eighth(b *testing.B)  { benchJPEG(b, 750, 500) }

func BenchmarkFit24MPTo1600(b *testing.B) {
	data := big24()
	src, _, err := Decode(context.Background(), bytes.NewReader(data), int64(len(data)), Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		Fit(src, 1600)
	}
}

func BenchmarkPHash(b *testing.B) {
	src := scene(1600, 1067, 0)
	b.ResetTimer()
	for b.Loop() {
		PHash(src)
	}
}
