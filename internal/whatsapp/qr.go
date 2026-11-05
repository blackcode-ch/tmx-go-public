package whatsapp

import (
	"encoding/base64"
	"strings"

	"rsc.io/qr"
)

// QR codes for linking. The daemon draws them, so neither the UI nor the CLI needs a QR
// package: the UI shows the PNG, `tmx wa link` prints the same code with half blocks.

// quiet is the light border around the code, in modules (scanners need one).
const quiet = 2

// QRPNG returns the code as a data:image/png;base64 URL (8 pixels per module).
func QRPNG(code string) (string, error) {
	c, err := qr.Encode(code, qr.L)
	if err != nil {
		return "", err
	}
	c.Scale = 8
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(c.PNG()), nil
}

// QRText draws the code for a terminal: each character is two modules, one above the other,
// with light modules drawn (terminals are usually dark, and a code must be dark on light).
func QRText(code string) (string, error) {
	c, err := qr.Encode(code, qr.L)
	if err != nil {
		return "", err
	}
	light := func(x, y int) bool { return !c.Black(x, y) } // outside the code is light too
	var b strings.Builder
	for y := -quiet; y < c.Size+quiet; y += 2 {
		for x := -quiet; x < c.Size+quiet; x++ {
			top, bottom := light(x, y), light(x, y+1)
			if y+1 >= c.Size+quiet {
				bottom = false // below the last row: the terminal's own background
			}
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
