package imaging

import (
	"image"
	"image/color"
)

// boxDown averages k x k blocks of img into an *image.RGBA of size
// ceil(w/k) x ceil(h/k). Edge blocks average only the pixels present.
func boxDown(img image.Image, k int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := (w+k-1)/k, (h+k-1)/k
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	switch m := img.(type) {
	case *image.YCbCr:
		boxYCbCr(dst, m, k)
	case *image.RGBA:
		boxPix(dst, m.Pix, m.Stride, w, h, k, false)
	case *image.NRGBA:
		boxPix(dst, m.Pix, m.Stride, w, h, k, true)
	case *image.Gray:
		boxGray(dst, m, k)
	default:
		boxGeneric(dst, img, k)
	}
	return dst
}

// chromaShift returns the horizontal and vertical chroma subsampling
// shifts of r.
func chromaShift(r image.YCbCrSubsampleRatio) (sx, sy int, ok bool) {
	switch r {
	case image.YCbCrSubsampleRatio444:
		return 0, 0, true
	case image.YCbCrSubsampleRatio422:
		return 1, 0, true
	case image.YCbCrSubsampleRatio420:
		return 1, 1, true
	case image.YCbCrSubsampleRatio440:
		return 0, 1, true
	case image.YCbCrSubsampleRatio411:
		return 2, 0, true
	case image.YCbCrSubsampleRatio410:
		return 2, 1, true
	}
	return 0, 0, false
}

func boxYCbCr(dst *image.RGBA, m *image.YCbCr, k int) {
	sx, sy, ok := chromaShift(m.SubsampleRatio)
	if !ok || m.Rect.Min.X < 0 || m.Rect.Min.Y < 0 {
		// Shift-based chroma indexing matches image.YCbCr.COffset only
		// for non-negative coordinates.
		boxGeneric(dst, m, k)
		return
	}
	b := m.Rect
	ow, oh := dst.Rect.Dx(), dst.Rect.Dy()
	sumY := make([]uint32, ow)
	sumCb := make([]uint32, ow)
	sumCr := make([]uint32, ow)
	cx0 := b.Min.X >> sx
	for oy := 0; oy < oh; oy++ {
		y0 := b.Min.Y + oy*k
		y1 := min(y0+k, b.Max.Y)
		clear(sumY)
		clear(sumCb)
		clear(sumCr)
		for y := y0; y < y1; y++ {
			yrow := m.Y[(y-b.Min.Y)*m.YStride:]
			cbase := ((y >> sy) - (b.Min.Y >> sy)) * m.CStride
			cbrow, crrow := m.Cb[cbase:], m.Cr[cbase:]
			for ox := 0; ox < ow; ox++ {
				x0 := b.Min.X + ox*k
				x1 := min(x0+k, b.Max.X)
				var ty, tb, tr uint32
				for x := x0; x < x1; x++ {
					ci := (x >> sx) - cx0
					ty += uint32(yrow[x-b.Min.X])
					tb += uint32(cbrow[ci])
					tr += uint32(crrow[ci])
				}
				sumY[ox] += ty
				sumCb[ox] += tb
				sumCr[ox] += tr
			}
		}
		rows := uint32(y1 - y0)
		drow := dst.Pix[oy*dst.Stride:]
		for ox := 0; ox < ow; ox++ {
			x0 := b.Min.X + ox*k
			n := rows * uint32(min(x0+k, b.Max.X)-x0)
			r, g, bb := color.YCbCrToRGB(uint8((sumY[ox]+n/2)/n), uint8((sumCb[ox]+n/2)/n), uint8((sumCr[ox]+n/2)/n))
			drow[4*ox], drow[4*ox+1], drow[4*ox+2], drow[4*ox+3] = r, g, bb, 0xFF
		}
	}
}

// boxPix handles 4-byte RGBA-ordered pixel buffers; straight (non
// premultiplied) alpha is premultiplied before averaging.
func boxPix(dst *image.RGBA, pix []byte, stride, w, h, k int, straight bool) {
	for oy := 0; oy < dst.Rect.Dy(); oy++ {
		y0 := oy * k
		y1 := min(y0+k, h)
		for ox := 0; ox < dst.Rect.Dx(); ox++ {
			x0 := ox * k
			x1 := min(x0+k, w)
			var sr, sg, sb, sa, n uint32
			for y := y0; y < y1; y++ {
				row := pix[y*stride+4*x0 : y*stride+4*x1]
				for i := 0; i+3 < len(row); i += 4 {
					r, g, b, a := uint32(row[i]), uint32(row[i+1]), uint32(row[i+2]), uint32(row[i+3])
					if straight && a != 0xFF {
						r, g, b = r*a/0xFF, g*a/0xFF, b*a/0xFF
					}
					sr += r
					sg += g
					sb += b
					sa += a
				}
				n += uint32(x1 - x0)
			}
			o := oy*dst.Stride + 4*ox
			dst.Pix[o] = uint8((sr + n/2) / n)
			dst.Pix[o+1] = uint8((sg + n/2) / n)
			dst.Pix[o+2] = uint8((sb + n/2) / n)
			dst.Pix[o+3] = uint8((sa + n/2) / n)
		}
	}
}

func boxGray(dst *image.RGBA, m *image.Gray, k int) {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	for oy := 0; oy < dst.Rect.Dy(); oy++ {
		y0 := oy * k
		y1 := min(y0+k, h)
		for ox := 0; ox < dst.Rect.Dx(); ox++ {
			x0 := ox * k
			x1 := min(x0+k, w)
			var s, n uint32
			for y := y0; y < y1; y++ {
				for _, v := range m.Pix[y*m.Stride+x0 : y*m.Stride+x1] {
					s += uint32(v)
				}
				n += uint32(x1 - x0)
			}
			v := uint8((s + n/2) / n)
			o := oy*dst.Stride + 4*ox
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = v, v, v, 0xFF
		}
	}
}

func boxGeneric(dst *image.RGBA, img image.Image, k int) {
	b := img.Bounds()
	for oy := 0; oy < dst.Rect.Dy(); oy++ {
		y0 := b.Min.Y + oy*k
		y1 := min(y0+k, b.Max.Y)
		for ox := 0; ox < dst.Rect.Dx(); ox++ {
			x0 := b.Min.X + ox*k
			x1 := min(x0+k, b.Max.X)
			var sr, sg, sb, sa, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, bb, a := img.At(x, y).RGBA()
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(bb)
					sa += uint64(a)
				}
				n += uint64(x1 - x0)
			}
			o := oy*dst.Stride + 4*ox
			dst.Pix[o] = uint8(((sr + n/2) / n) >> 8)
			dst.Pix[o+1] = uint8(((sg + n/2) / n) >> 8)
			dst.Pix[o+2] = uint8(((sb + n/2) / n) >> 8)
			dst.Pix[o+3] = uint8(((sa + n/2) / n) >> 8)
		}
	}
}
