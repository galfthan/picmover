package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"rsc.io/qr"

	"picmover/internal/share"
)

// qrTerminal renders text as a QR code with Unicode half blocks (the
// product's renderer).
func qrTerminal(text string) (string, error) { return share.QRTerminal(text) }

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
