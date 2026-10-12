// Command gen draws picmover's application icon (a rounded square with a
// sun over mountains) and writes the PNGs go-winres embeds, the tray
// icons, the 256 px source, the web favicon and manifest icons, and the
// 256 px PNG the Linux desktop entry installs:
//
//	go run ./winres/gen
//
// run from the repository root. Each size is rendered directly (4x4
// supersampled), not scaled down from the large one.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

var (
	bgTop    = color.NRGBA{0x1f, 0x6f, 0xb2, 0xff} // sky blue
	bgBottom = color.NRGBA{0x12, 0x4a, 0x7c, 0xff}
	sun      = color.NRGBA{0xf5, 0xb7, 0x2e, 0xff} // amber
	mountain = color.NRGBA{0xf4, 0xf1, 0xea, 0xff} // near white
	ridge    = color.NRGBA{0xc9, 0xd6, 0xe3, 0xff}
)

const supersample = 4

// shade returns the colour of point (x, y) in unit coordinates (0..1),
// or transparent outside the rounded square.
func shade(x, y float64) color.NRGBA {
	const r = 0.2 // corner radius
	// Rounded square: inside when the distance to the inner rectangle is <= r.
	dx := math.Max(math.Max(r-x, x-(1-r)), 0)
	dy := math.Max(math.Max(r-y, y-(1-r)), 0)
	if dx*dx+dy*dy > r*r {
		return color.NRGBA{}
	}
	// Front mountain (white), the back one behind it (lighter blue).
	if y > 0.95-1.1*math.Max(0, 0.42-math.Abs(x-0.36)) {
		return mountain
	}
	if y > 0.95-0.9*math.Max(0, 0.40-math.Abs(x-0.72)) {
		return ridge
	}
	// Sun.
	if sx, sy := x-0.68, y-0.33; sx*sx+sy*sy < 0.13*0.13 {
		return sun
	}
	t := y
	return color.NRGBA{
		R: uint8(float64(bgTop.R)*(1-t) + float64(bgBottom.R)*t),
		G: uint8(float64(bgTop.G)*(1-t) + float64(bgBottom.G)*t),
		B: uint8(float64(bgTop.B)*(1-t) + float64(bgBottom.B)*t),
		A: 0xff,
	}
}

// render draws the icon at size×size pixels.
func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	n := float64(size * supersample)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < supersample; sy++ {
				for sx := 0; sx < supersample; sx++ {
					x := (float64(px*supersample+sx) + 0.5) / n
					y := (float64(py*supersample+sy) + 0.5) / n
					c := shade(x, y)
					fa := float64(c.A) / 255
					r += float64(c.R) * fa
					g += float64(c.G) * fa
					b += float64(c.B) * fa
					a += fa
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(r / a), G: uint8(g / a), B: uint8(b / a),
				A: uint8(a / float64(supersample*supersample) * 255),
			})
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}

// ico packs PNG images into an ICO file (PNG-compressed entries, which
// Windows Vista and later read).
func ico(sizes []int, pngs [][]byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		dim := uint8(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		binary.Write(&buf, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(p)), uint32(offset)})
		offset += len(p)
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return buf.Bytes()
}

func write(p string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s (%d bytes)", p, len(data))
}

func main() {
	sizes := []int{16, 32, 48, 192, 256, 512}
	pngs := map[int][]byte{}
	for _, s := range sizes {
		pngs[s] = encode(render(s))
		if s <= 48 {
			write(filepath.Join("winres", "icon"+strconv.Itoa(s)+".png"), pngs[s])
		}
	}
	write(filepath.Join("winres", "icon.png"), pngs[256])
	write(filepath.Join("internal", "shell", "assets", "tray.png"), pngs[32])
	write(filepath.Join("internal", "shell", "assets", "tray.ico"), ico([]int{16, 32, 48}, [][]byte{pngs[16], pngs[32], pngs[48]}))
	write(filepath.Join("internal", "shell", "assets", "icon256.png"), pngs[256])
	write(filepath.Join("web", "public", "favicon.png"), pngs[32])
	write(filepath.Join("web", "public", "icons", "icon-192.png"), pngs[192])
	write(filepath.Join("web", "public", "icons", "icon-512.png"), pngs[512])
}
