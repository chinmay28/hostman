package main

import (
	"image"
	"image/color"

	"golang.org/x/image/draw"
)

// Matte paints the paper around the artwork in the given colour: every
// near-white pixel reachable from the image's edge through other near-white
// pixels, plus the one-pixel fringe where the artwork's outline was
// anti-aliased against the paper. White inside the artwork (a laptop screen,
// a highlight) is not connected to the edge and is left alone.
func Matte(src image.Image, paper color.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)

	w, h := b.Dx(), b.Dy()
	outside := make([]bool, w*h)
	at := func(x, y int) color.RGBA { return dst.RGBAAt(b.Min.X+x, b.Min.Y+y) }

	// Flood fill from the border over the near-white pixels.
	var stack []image.Point
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= w || y >= h || outside[y*w+x] || !isPaper(at(x, y)) {
			return
		}
		outside[y*w+x] = true
		stack = append(stack, image.Point{X: x, Y: y})
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		push(p.X-1, p.Y)
		push(p.X+1, p.Y)
		push(p.X, p.Y-1)
		push(p.X, p.Y+1)
	}

	// The fringe: light pixels touching the paper are the outline's
	// anti-aliasing against white, and would read as a pale halo.
	fringe := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if outside[y*w+x] || !isLight(at(x, y)) {
				continue
			}
			for dy := -1; dy <= 1 && !fringe[y*w+x]; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx >= 0 && ny >= 0 && nx < w && ny < h && outside[ny*w+nx] {
						fringe[y*w+x] = true
						break
					}
				}
			}
		}
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if outside[y*w+x] || fringe[y*w+x] {
				dst.SetRGBA(b.Min.X+x, b.Min.Y+y, paper)
			}
		}
	}
	return dst
}

// isPaper is a pixel of the white page: every channel nearly full.
func isPaper(c color.RGBA) bool { return c.R >= 235 && c.G >= 235 && c.B >= 235 }

// isLight is pale enough to be the page bleeding into an outline.
func isLight(c color.RGBA) bool { return int(c.R)+int(c.G)+int(c.B) >= 3*150 }

// CropSquare cuts the square of the given side centred on centre. Where the
// square runs past the image it is padded with pad.
func CropSquare(src image.Image, centre image.Point, side int, pad color.RGBA) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(pad), image.Point{}, draw.Src)
	r := image.Rect(centre.X-side/2, centre.Y-side/2, centre.X-side/2+side, centre.Y-side/2+side)
	visible := r.Intersect(src.Bounds())
	draw.Draw(dst, visible.Sub(r.Min), src, visible.Min, draw.Src)
	return dst
}

// Scale resamples a square image to size×size.
func Scale(src image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

// RoundCorners makes the image a rounded square in place: pixels outside a
// corner arc of radius×side go transparent, with partial coverage along the
// arc so the edge is smooth.
func RoundCorners(img *image.RGBA, radius float64) {
	const ss = 4 // sub-samples per axis along the edge
	b := img.Bounds()
	n := float64(b.Dx())
	r := radius * n
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			inside := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if inRoundedSquare(px, py, n, r) {
						inside++
					}
				}
			}
			if inside == ss*ss {
				continue
			}
			c := img.RGBAAt(b.Min.X+x, b.Min.Y+y)
			k := float64(inside) / (ss * ss)
			img.SetRGBA(b.Min.X+x, b.Min.Y+y, color.RGBA{
				R: uint8(float64(c.R)*k + 0.5),
				G: uint8(float64(c.G)*k + 0.5),
				B: uint8(float64(c.B)*k + 0.5),
				A: uint8(float64(c.A)*k + 0.5),
			})
		}
	}
}

// inRoundedSquare reports whether a point lies inside the n×n square with
// corners rounded to radius r.
func inRoundedSquare(x, y, n, r float64) bool {
	cx := min(max(x, r), n-r)
	cy := min(max(y, r), n-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}
