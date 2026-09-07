package main

import (
	"image"
	"image/color"
	"testing"
)

var (
	white = color.RGBA{255, 255, 255, 255}
	navy  = color.RGBA{1, 18, 37, 255}
	ink   = color.RGBA{20, 20, 20, 255}
	grey  = color.RGBA{200, 200, 200, 255}
)

// artwork is a 10×10 page: white, with a dark 6×6 block in the middle that
// has a white 2×2 hole and one grey anti-aliased pixel on its top edge.
func artwork() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			img.SetRGBA(x, y, white)
		}
	}
	for y := 2; y < 8; y++ {
		for x := 2; x < 8; x++ {
			img.SetRGBA(x, y, ink)
		}
	}
	img.SetRGBA(4, 4, white)
	img.SetRGBA(5, 4, white)
	img.SetRGBA(4, 5, white)
	img.SetRGBA(5, 5, white)
	img.SetRGBA(5, 2, grey)
	return img
}

func TestMattePaintsThePageButNotTheArtwork(t *testing.T) {
	out := Matte(artwork(), navy)
	cases := []struct {
		x, y int
		want color.RGBA
		why  string
	}{
		{0, 0, navy, "page corner"},
		{9, 5, navy, "page edge"},
		{1, 5, navy, "page right up against the block"},
		{3, 3, ink, "the artwork"},
		{4, 4, white, "white inside the artwork, not reachable from the edge"},
		{5, 2, navy, "grey fringe on the outline is painted over"},
	}
	for _, c := range cases {
		if got := out.RGBAAt(c.x, c.y); got != c.want {
			t.Errorf("(%d,%d) %s: got %v, want %v", c.x, c.y, c.why, got, c.want)
		}
	}
}

func TestMatteCanCutTheArtworkOut(t *testing.T) {
	out := Matte(artwork(), color.RGBA{})
	if got := out.RGBAAt(0, 0); got.A != 0 {
		t.Errorf("page corner is %v, want transparent", got)
	}
	if got := out.RGBAAt(3, 3); got != ink {
		t.Errorf("artwork is %v, want ink", got)
	}
}

func TestMatteLeavesTheSourceAlone(t *testing.T) {
	src := artwork()
	Matte(src, navy)
	if got := src.RGBAAt(0, 0); got != white {
		t.Errorf("source was modified: corner is %v", got)
	}
}

func TestCropSquarePadsPastTheEdge(t *testing.T) {
	out := CropSquare(artwork(), image.Point{X: 1, Y: 1}, 6, navy)
	if b := out.Bounds(); b.Dx() != 6 || b.Dy() != 6 {
		t.Fatalf("cropped %v, want 6×6", b)
	}
	// The square runs from (-2,-2) to (4,4): its first two rows and columns
	// are padding, the rest is the page and then the block's corner.
	if got := out.RGBAAt(0, 0); got != navy {
		t.Errorf("padding is %v, want %v", got, navy)
	}
	if got := out.RGBAAt(2, 2); got != white {
		t.Errorf("page corner is %v, want white", got)
	}
	if got := out.RGBAAt(4, 4); got != ink {
		t.Errorf("block corner is %v, want ink", got)
	}
}

func TestScaleGivesTheRequestedSize(t *testing.T) {
	out := Scale(artwork(), 25)
	if b := out.Bounds(); b.Dx() != 25 || b.Dy() != 25 {
		t.Fatalf("scaled to %v, want 25×25", b)
	}
	if got := out.RGBAAt(12, 12); got != white {
		t.Errorf("centre of the hole is %v, want white", got)
	}
}

func TestRoundCornersClearsTheCornersOnly(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.SetRGBA(x, y, ink)
		}
	}
	RoundCorners(img, 0.2237)
	if got := img.RGBAAt(0, 0).A; got != 0 {
		t.Errorf("corner alpha %d, want 0", got)
	}
	if got := img.RGBAAt(99, 99).A; got != 0 {
		t.Errorf("far corner alpha %d, want 0", got)
	}
	for _, p := range []image.Point{{50, 50}, {0, 50}, {50, 0}, {99, 50}, {30, 0}} {
		if got := img.RGBAAt(p.X, p.Y); got != ink {
			t.Errorf("%v should be untouched, got %v", p, got)
		}
	}
	// Along the arc the edge is partially covered, not a hard step.
	partial := false
	for x := 0; x < 25; x++ {
		if a := img.RGBAAt(x, 0).A; a > 0 && a < 255 {
			partial = true
		}
	}
	if !partial {
		t.Error("no anti-aliased pixels along the corner arc")
	}
}
