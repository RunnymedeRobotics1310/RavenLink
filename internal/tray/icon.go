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
func renderIconPNG(_ string) []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))

	logo := getLogo()
	if logo != nil {
		compositeLogo(canvas, logo, iconSize)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, canvas)
	return buf.Bytes()
}

// renderTemplateIconPNG produces a 22x22 black-silhouette PNG suitable
// for SetTemplateIcon on macOS. Template icons are rendered by
// AppKit with automatic light/dark tinting, which is the only reliable
// way to get a visible menu bar icon across macOS versions.
//
// The result is a black alpha-mask of the team logo at 22x22 — the
// size macOS expects for menu bar extras.
func renderTemplateIconPNG() []byte {
	const size = 22
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))

	logo := getLogo()
	if logo == nil {
		// fallback: a filled black circle
		drawFilledCircle(canvas, size/2, size/2, (size-2)/2,
			color.RGBA{R: 0, G: 0, B: 0, A: 255})
		var buf bytes.Buffer
		_ = png.Encode(&buf, canvas)
		return buf.Bytes()
	}

	// Downscale the logo into a 22x22 canvas and convert every non-
	// transparent pixel to black (preserving alpha for smooth edges).
	sb := logo.Bounds()
	srcW := sb.Dx()
	srcH := sb.Dy()
	scale := math.Min(float64(size)/float64(srcW), float64(size)/float64(srcH))
	dstW := int(float64(srcW) * scale)
	dstH := int(float64(srcH) * scale)
	dstX := (size - dstW) / 2
	dstY := (size - dstH) / 2

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx := sb.Min.X + int(float64(x)/scale)
			sy := sb.Min.Y + int(float64(y)/scale)
			_, _, _, a := logo.At(sx, sy).RGBA()
			if a == 0 {
				continue
			}
			// Alpha in 0-65535; convert to 8-bit.
			alpha := uint8(a >> 8)
			canvas.SetRGBA(dstX+x, dstY+y,
				color.RGBA{R: 0, G: 0, B: 0, A: alpha})
		}
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
	target := int(float64(size) * 0.92)
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
