package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// Predefined icon colours. Chosen for high contrast against both light
// and dark menu bars.
var iconColors = map[string]color.RGBA{
	"green":  {R: 34, G: 197, B: 94, A: 255},
	"yellow": {R: 234, G: 179, B: 8, A: 255},
	"red":    {R: 239, G: 68, B: 68, A: 255},
	"gray":   {R: 156, G: 163, B: 175, A: 255},
}

const iconSize = 64

// renderIconPNG draws the RavenLink status icon as a 64x64 PNG:
// a filled colored circle with a thick white "R" in the center.
// The R is drawn geometrically from filled rectangles so it renders
// crisply even when scaled down to 16x16.
func renderIconPNG(name string) []byte {
	fill, ok := iconColors[name]
	if !ok {
		fill = iconColors["gray"]
	}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))

	// 1. Background circle (status color).
	drawFilledCircle(img, iconSize/2, iconSize/2, (iconSize-4)/2, fill)

	// 2. White "R" centered in the circle.
	//    The R occupies a ~32x36 rectangle centered at (32, 32).
	//    Layout (with origin at top-left of the R):
	//
	//      ┌──────────┐
	//      │███████   │  top bar
	//      │██     ██ │
	//      │██     ██ │
	//      │███████   │  middle bar (closes the bowl)
	//      │██  ██    │
	//      │██    ██  │  diagonal leg
	//      │██      ██│
	//      └──────────┘
	//
	rLeft := 16
	rTop := 14
	rW := 32
	rH := 36
	stroke := 7

	// Left vertical stem
	fillRect(img, rLeft, rTop, stroke, rH, white)
	// Top horizontal bar
	fillRect(img, rLeft, rTop, rW-stroke, stroke, white)
	// Top-right inner vertical (closes the top of the bowl)
	fillRect(img, rLeft+rW-stroke-2, rTop, stroke, rH/2-stroke/2+2, white)
	// Middle horizontal bar (closes the bowl)
	fillRect(img, rLeft, rTop+rH/2-stroke/2, rW-stroke, stroke, white)
	// Diagonal leg from middle-junction to bottom-right
	drawThickLine(img,
		rLeft+rW/2-stroke/2, rTop+rH/2+stroke/2,
		rLeft+rW-stroke, rTop+rH,
		stroke, white)

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
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

// fillRect fills an axis-aligned rectangle.
func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	rect := image.Rect(x, y, x+w, y+h)
	draw.Draw(img, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// drawThickLine rasterizes a line from (x0,y0) to (x1,y1) with the
// given thickness using Bresenham. A tiny convenience for the R's leg.
func drawThickLine(img *image.RGBA, x0, y0, x1, y1, thickness int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := abs(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx - dy
	half := thickness / 2

	for {
		// Stamp a filled square at the current point for thickness.
		for yy := y0 - half; yy <= y0+half; yy++ {
			for xx := x0 - half; xx <= x0+half; xx++ {
				if xx >= 0 && xx < iconSize && yy >= 0 && yy < iconSize {
					img.SetRGBA(xx, yy, c)
				}
			}
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
