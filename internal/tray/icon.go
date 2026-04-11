package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/assets"
)

// Predefined status ring colours that encircle the logo.
var iconColors = map[string]color.RGBA{
	"green":  {R: 34, G: 197, B: 94, A: 255},
	"yellow": {R: 234, G: 179, B: 8, A: 255},
	"red":    {R: 239, G: 68, B: 68, A: 255},
	"gray":   {R: 156, G: 163, B: 175, A: 255},
}

const iconSize = 64

// Decoded logo, lazily cached on first use.
var (
	decodedLogo     image.Image
	decodedLogoOnce sync.Once
)

func getLogo() image.Image {
	decodedLogoOnce.Do(func() {
		img, err := png.Decode(bytes.NewReader(assets.Team1310Logo))
		if err != nil {
			decodedLogo = nil
			return
		}
		decodedLogo = img
	})
	return decodedLogo
}

// renderIconPNG produces a 64x64 PNG that will render cleanly at
// menu-bar sizes (16–22pt). The design:
//
//   - A filled circle in the status color (green/yellow/red/gray)
//   - The team 1310 logo composited on top, scaled and centered
//
// For the macOS menu bar this still looks like a status indicator;
// at Dock/Activity Monitor size the full logo is used via the .icns
// generated separately.
func renderIconPNG(name string) []byte {
	fill, ok := iconColors[name]
	if !ok {
		fill = iconColors["gray"]
	}

	canvas := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	drawFilledCircle(canvas, iconSize/2, iconSize/2, (iconSize-4)/2, fill)

	logo := getLogo()
	if logo != nil {
		compositeLogo(canvas, logo, iconSize)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, canvas)
	return buf.Bytes()
}

// compositeLogo scales the team logo to fit ~70% of the canvas and
// draws it centered. Alpha compositing over the status circle.
func compositeLogo(dst *image.RGBA, src image.Image, size int) {
	sb := src.Bounds()
	srcW := sb.Dx()
	srcH := sb.Dy()
	target := int(float64(size) * 0.75)
	scale := math.Min(float64(target)/float64(srcW), float64(target)/float64(srcH))
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	dstX := (size - dstW) / 2
	dstY := (size - dstH) / 2

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx := sb.Min.X + int(float64(x)/scale)
			sy := sb.Min.Y + int(float64(y)/scale)
			if sx >= sb.Max.X {
				sx = sb.Max.X - 1
			}
			if sy >= sb.Max.Y {
				sy = sb.Max.Y - 1
			}
			c := src.At(sx, sy)
			draw.Draw(dst,
				image.Rect(dstX+x, dstY+y, dstX+x+1, dstY+y+1),
				&image.Uniform{C: c},
				image.Point{},
				draw.Over)
		}
	}
}

// drawFilledCircle draws a filled circle using a simple distance test.
func drawFilledCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	r2 := r * r
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= r2 {
				img.SetRGBA(x, y, c)
			}
		}
	}
}
