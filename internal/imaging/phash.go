package imaging

import (
	"image"
	"math"
	"math/bits"
	"slices"

	"golang.org/x/image/draw"
)

const (
	phashSize = 32 // side of the grayscale image the DCT runs on
	phashLow  = 8  // side of the low-frequency block kept
)

// PHash returns the 64-bit DCT perceptual hash of img: img is reduced to
// 32x32 grayscale (aspect is not preserved), a 2D DCT-II is taken, and the
// top-left 8x8 low-frequency coefficients are compared with the median of
// the 63 AC coefficients. Bit u*8+v is set when coefficient (u,v) is above
// the median; the DC term (bit 0) is excluded and always 0.
func PHash(img image.Image) uint64 {
	b := img.Bounds()
	if b.Empty() {
		return 0
	}
	small := Fit(img, 4*phashSize)
	g := image.NewRGBA(image.Rect(0, 0, phashSize, phashSize))
	draw.BiLinear.Scale(g, g.Bounds(), small, small.Bounds(), draw.Src, nil)

	var px [phashSize][phashSize]float64
	for y := 0; y < phashSize; y++ {
		for x := 0; x < phashSize; x++ {
			o := y*g.Stride + 4*x
			px[y][x] = 0.299*float64(g.Pix[o]) + 0.587*float64(g.Pix[o+1]) + 0.114*float64(g.Pix[o+2])
		}
	}

	var cos [phashLow][phashSize]float64
	for u := 0; u < phashLow; u++ {
		for x := 0; x < phashSize; x++ {
			cos[u][x] = math.Cos(float64(2*x+1) * float64(u) * math.Pi / (2 * phashSize))
		}
	}
	// Rows first: tmp[y][v] = sum_x px[y][x] cos(v, x).
	var tmp [phashSize][phashLow]float64
	for y := 0; y < phashSize; y++ {
		for v := 0; v < phashLow; v++ {
			var s float64
			for x := 0; x < phashSize; x++ {
				s += px[y][x] * cos[v][x]
			}
			tmp[y][v] = s
		}
	}
	// Then columns: coef[u][v] = sum_y tmp[y][v] cos(u, y).
	var coef [phashLow * phashLow]float64
	for u := 0; u < phashLow; u++ {
		for v := 0; v < phashLow; v++ {
			var s float64
			for y := 0; y < phashSize; y++ {
				s += tmp[y][v] * cos[u][y]
			}
			coef[u*phashLow+v] = s
		}
	}
	ac := slices.Clone(coef[1:])
	slices.Sort(ac)
	median := ac[len(ac)/2]
	var h uint64
	for i := 1; i < len(coef); i++ {
		if coef[i] > median {
			h |= 1 << uint(i)
		}
	}
	return h
}

// Distance is the Hamming distance between two hashes.
func Distance(a, b uint64) int { return bits.OnesCount64(a ^ b) }
