// Command iconbuilder renders the team 1310 logo as a macOS .iconset
// (directory of PNGs at multiple sizes). Apple's iconutil then
// converts the directory into a .icns file.
//
// Usage (from the project root):
//
//	go run ./cmd/iconbuilder dist/RavenLink.iconset
//
// Then:
//
//	iconutil -c icns dist/RavenLink.iconset -o dist/RavenLink.icns
//
// The build-macos.sh script handles both steps.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/assets"
)

// macOS .iconset file layout. Each entry gets one PNG at the given
// pixel size.
var iconsetFiles = []struct {
	px   int
	name string
}{
	{16, "icon_16x16.png"},
	{32, "icon_16x16@2x.png"},
	{32, "icon_32x32.png"},
	{64, "icon_32x32@2x.png"},
	{128, "icon_128x128.png"},
	{256, "icon_128x128@2x.png"},
	{256, "icon_256x256.png"},
	{512, "icon_256x256@2x.png"},
	{512, "icon_512x512.png"},
	{1024, "icon_512x512@2x.png"},
}

// Background gradient for the rounded-square plate behind the logo.
// Runnymede team colors approximated from the website.
var (
	bgTop    = color.RGBA{R: 17, G: 24, B: 39, A: 255}  // near-black blue
	bgBottom = color.RGBA{R: 30, G: 41, B: 59, A: 255}  // slate
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: iconbuilder <output-iconset-dir>")
		os.Exit(1)
	}
	outDir := os.Args[1]
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}

	// Decode the team logo once.
	logo, err := png.Decode(bytes.NewReader(assets.Team1310Logo))
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode logo: %v\n", err)
		os.Exit(1)
	}

	for _, e := range iconsetFiles {
		data := renderIcon(logo, e.px)
		path := filepath.Join(outDir, e.name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%dpx, %d bytes)\n", path, e.px, len(data))
	}
}

// renderIcon composites the team logo onto a rounded-square background
// at the given size. Follows Apple's guidance for macOS icons: logo
// occupies ~80% of the canvas, centered, with a subtle backdrop.
func renderIcon(logo image.Image, size int) []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))

	// 1. Rounded-square background with vertical gradient.
	corner := float64(size) * 0.22
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if !insideRoundedRect(x, y, size, size, corner) {
				continue
			}
			t := float64(y) / float64(size)
			r := uint8(lerp(float64(bgTop.R), float64(bgBottom.R), t))
			g := uint8(lerp(float64(bgTop.G), float64(bgBottom.G), t))
			b := uint8(lerp(float64(bgTop.B), float64(bgBottom.B), t))
			canvas.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}

	// 2. Scale the logo to fit with ~10% padding and centre it.
	logoBounds := logo.Bounds()
	srcW := logoBounds.Dx()
	srcH := logoBounds.Dy()

	// Target box: 80% of canvas, maintain aspect ratio.
	target := int(float64(size) * 0.80)
	scale := math.Min(float64(target)/float64(srcW), float64(target)/float64(srcH))
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	dstX := (size - dstW) / 2
	dstY := (size - dstH) / 2

	// Nearest-neighbour downscale into the canvas. Fine for iconset
	// sizes from 16x16 up to 1024x1024; avoids pulling in x/image.
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx := logoBounds.Min.X + int(float64(x)/scale)
			sy := logoBounds.Min.Y + int(float64(y)/scale)
			if sx >= logoBounds.Max.X {
				sx = logoBounds.Max.X - 1
			}
			if sy >= logoBounds.Max.Y {
				sy = logoBounds.Max.Y - 1
			}
			src := logo.At(sx, sy)
			// Composite src over canvas at (dstX+x, dstY+y).
			draw.Draw(canvas,
				image.Rect(dstX+x, dstY+y, dstX+x+1, dstY+y+1),
				&image.Uniform{C: src},
				image.Point{},
				draw.Over)
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, canvas)
	return buf.Bytes()
}

// insideRoundedRect returns true if (x,y) is inside a rounded rectangle
// of the given dimensions and corner radius.
func insideRoundedRect(x, y, w, h int, r float64) bool {
	if float64(x) >= r && float64(x) <= float64(w)-r {
		return y >= 0 && y < h
	}
	if float64(y) >= r && float64(y) <= float64(h)-r {
		return x >= 0 && x < w
	}
	cx, cy := r, r
	if float64(x) > float64(w)-r {
		cx = float64(w) - r
	}
	if float64(y) > float64(h)-r {
		cy = float64(h) - r
	}
	dx := float64(x) - cx
	dy := float64(y) - cy
	return math.Sqrt(dx*dx+dy*dy) <= r
}

func lerp(a, b float64, t float64) float64 {
	return a + (b-a)*t
}
