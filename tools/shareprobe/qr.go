package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"

	"rsc.io/qr"
)

// quiet is the QR quiet zone in modules.
const quiet = 2

// qrTerminal renders text as a QR code with Unicode half blocks, two rows
// of modules per line, dark modules drawn as the terminal background. It
// prints dark-on-light by inverting: a "full block" is a light module.
func qrTerminal(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	light := func(x, y int) bool { return !c.Black(x, y) } // outside the code is light too
	var b strings.Builder
	for y := -quiet; y < c.Size+quiet; y += 2 {
		for x := -quiet; x < c.Size+quiet; x++ {
			top, bot := light(x, y), light(x, y+1)
			if y+1 >= c.Size+quiet {
				bot = false
			}
			switch {
			case top && bot:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bot:
				b.WriteRune('▄')
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// qrPNG renders text as a PNG with a 4-module quiet zone, for showing on a
// screen when the terminal rendering does not scan.
func qrPNG(text string, scale int) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := (c.Size + 8) * scale
	img := image.NewGray(image.Rect(0, 0, n, n))
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			v := uint8(255)
			if c.Black(px/scale-4, py/scale-4) {
				v = 0
			}
			img.SetGray(px, py, color.Gray{Y: v})
		}
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, img)
	return buf.Bytes(), err
}
