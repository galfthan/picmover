package thumbs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"picmover/internal/imaging"
	"picmover/internal/library"
	"picmover/internal/metadata"
	"picmover/internal/testimg"
)

// The 12 MP camera-like fixture is generated in memory once per test
// binary and written to a temp file per benchmark.
var big12 = sync.OnceValue(func() []byte { return testimg.BigJPEG(4000, 3000) })

func benchGen(b *testing.B) (*Generator, string) {
	b.Helper()
	lib, err := library.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	s, err := OpenStore(lib)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	p := filepath.Join(b.TempDir(), "big.jpg")
	if err := os.WriteFile(p, big12(), 0o644); err != nil {
		b.Fatal(err)
	}
	return &Generator{Store: s}, p
}

// BenchmarkGenerate12MP is one full generation (decode, scale, encode all
// renditions, pHash, store) of a 12 MP JPEG on one goroutine.
func BenchmarkGenerate12MP(b *testing.B) {
	g, p := benchGen(b)
	ctx := context.Background()
	for b.Loop() {
		if res := g.Generate(ctx, Source{Path: p, Kind: metadata.KindImage, Hash: 1}, true); res.State != StateOK {
			b.Fatal(res)
		}
	}
}

// BenchmarkDeriveLarge measures filling one large thumbnail from a stored
// 1600 px preview (decode, scale to 640, WebP encode, write), for a WebP
// preview and for a JPEG one written by an older version.
func BenchmarkDeriveLarge(b *testing.B) {
	for _, format := range []string{"webp", "jpeg"} {
		b.Run(format, func(b *testing.B) {
			g, _ := benchGen(b)
			ctx := context.Background()
			img := photoScene(1600, 1067, 1)
			var pbuf bytes.Buffer
			var err error
			if format == "webp" {
				err = imaging.EncodeWebP(&pbuf, img, PreviewQuality)
			} else {
				err = imaging.EncodeJPEG(&pbuf, img, PreviewQuality)
			}
			if err != nil {
				b.Fatal(err)
			}
			if err := g.Store.Put(ctx, 1, pbuf.Bytes(), 1, 1, nil, pbuf.Bytes()); err != nil {
				b.Fatal(err)
			}
			for b.Loop() {
				os.Remove(g.Store.LargePath(1))
				if filled, err := g.FillLarge(ctx, 1); !filled || err != nil {
					b.Fatal(filled, err)
				}
			}
		})
	}
}

// BenchmarkDeriveLargeParallel is the large-fill pass over existing
// content: GOMAXPROCS derivations at once from JPEG previews (what a
// library generated before WebP has).
func BenchmarkDeriveLargeParallel(b *testing.B) {
	g, _ := benchGen(b)
	ctx := context.Background()
	var pbuf bytes.Buffer
	if err := imaging.EncodeJPEG(&pbuf, photoScene(1600, 1067, 1), PreviewQuality); err != nil {
		b.Fatal(err)
	}
	if err := g.Store.Put(ctx, 1, pbuf.Bytes(), 1, 1, nil, pbuf.Bytes()); err != nil {
		b.Fatal(err)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			data, err := EncodeLarge(ctx, g.Store, 1, g.opts(nil))
			if err != nil || len(data) == 0 {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkGenerate12MPParallel measures the thumbnail stage's throughput:
// GOMAXPROCS generations at once, each with its own hash.
func BenchmarkGenerate12MPParallel(b *testing.B) {
	g, p := benchGen(b)
	ctx := context.Background()
	var next atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h := next.Add(1)
			if res := g.Generate(ctx, Source{Path: p, Kind: metadata.KindImage, Hash: h}, true); res.State != StateOK {
				b.Error(res)
				return
			}
		}
	})
}
