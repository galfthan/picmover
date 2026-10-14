package share

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// qrQuiet is the quiet zone of the SVG in modules (the spec asks for 4).
const qrQuiet = 4

// QRSVG renders text as a QR code (level M) in SVG: one path of unit
// squares in a viewBox of modules, black on white, which the page scales.
func QRSVG(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	n := c.Size + 2*qrQuiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+qrQuiet, y+qrQuiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

// termQuiet is the quiet zone of the terminal rendering in modules.
const termQuiet = 2

// QRTerminal renders text as a QR code with Unicode half blocks, two
// module rows per line. Light modules are drawn as blocks, so the code
// reads dark-on-light on a dark terminal.
func QRTerminal(text string) (string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	light := func(x, y int) bool { return !c.Black(x, y) } // outside the code is light too
	var b strings.Builder
	for y := -termQuiet; y < c.Size+termQuiet; y += 2 {
		for x := -termQuiet; x < c.Size+termQuiet; x++ {
			top, bot := light(x, y), light(x, y+1)
			if y+1 >= c.Size+termQuiet {
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
