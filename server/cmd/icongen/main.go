// Command icongen draws HostMan's app icons. iOS home-screen icons must be
// PNG, so they are generated rather than hand-drawn, and checked in.
//
//	go run ./cmd/icongen -out ../apps/web/public
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", ".", "directory to write icons into")
	flag.Parse()

	targets := map[string]int{
		"apple-touch-icon.png": 180,
		"icon-192.png":         192,
		"icon-512.png":         512,
	}
	for name, size := range targets {
		path := filepath.Join(*out, name)
		if err := write(path, size); err != nil {
			fmt.Fprintln(os.Stderr, "icongen:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}

func write(path string, size int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, draw(size))
}

// Supersampling factor: the shapes are pure math, so the cheapest way to get
// smooth edges is to sample each pixel several times.
const ss = 4

func draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	n := float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / n
					y := (float64(py) + (float64(sy)+0.5)/ss) / n
					sr, sg, sb, sa := sample(x, y)
					r, g, b, a = r+sr, g+sg, b+sb, a+sa
				}
			}
			count := float64(ss * ss)
			img.Set(px, py, color.RGBA{
				R: uint8(r/count + 0.5),
				G: uint8(g/count + 0.5),
				B: uint8(b/count + 0.5),
				A: uint8(a/count + 0.5),
			})
		}
	}
	return img
}

// sample returns the colour at a point in the unit square.
func sample(x, y float64) (r, g, b, a float64) {
	if !inRoundedSquare(x, y, 0.2237) {
		return 0, 0, 0, 0
	}
	// Indigo to violet, along the diagonal.
	t := (x + y) / 2
	bgR := lerp(0x63, 0x8b, t)
	bgG := lerp(0x66, 0x5c, t)
	bgB := lerp(0xf1, 0xf6, t)

	if inMark(x, y) {
		return 0xff, 0xff, 0xff, 0xff
	}
	return bgR, bgG, bgB, 0xff
}

// inMark is the glyph: an upward arrow over a baseline, for "ship it".
func inMark(x, y float64) bool {
	const cx = 0.5

	// Baseline bar.
	if y >= 0.70 && y <= 0.785 && math.Abs(x-cx) <= 0.23 {
		return true
	}
	// Shaft.
	if y >= 0.40 && y <= 0.635 && math.Abs(x-cx) <= 0.075 {
		return true
	}
	// Head: a triangle whose half-width grows as it descends.
	if y >= 0.215 && y <= 0.40 {
		halfWidth := (y - 0.215) / (0.40 - 0.215) * 0.23
		if math.Abs(x-cx) <= halfWidth {
			return true
		}
	}
	return false
}

// inRoundedSquare reports whether a point is inside the unit square with
// rounded corners of the given radius.
func inRoundedSquare(x, y, radius float64) bool {
	cx := math.Min(math.Max(x, radius), 1-radius)
	cy := math.Min(math.Max(y, radius), 1-radius)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func lerp(from, to float64, t float64) float64 { return from + (to-from)*t }
