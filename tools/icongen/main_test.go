package main

import (
	"image/color"
	"testing"
)

// The icons are committed, and every one is made from the logo. This is what
// notices a hand edit, or a logo change whose icons were never regenerated.
func TestCommittedIconsMatchTheLogo(t *testing.T) {
	files, err := render("../../art/hostman-logo.png", "../../apps/web/public", "../../apps/android/app/src/main/res")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFiles(files); err != nil {
		t.Fatal(err)
	}
}

// A launcher shows the middle 72dp of the 108dp foreground through its mask,
// and may scale or move the layer: the scene fills that middle, and the
// margin around it is clear so the badge-coloured background shows there.
func TestAdaptiveForegroundFillsTheSafeZoneOnly(t *testing.T) {
	fg := AdaptiveForeground(artwork(), 2)
	if b := fg.Bounds(); b.Dx() != 216 || b.Dy() != 216 {
		t.Fatalf("foreground is %v, want 216×216 at xhdpi", b)
	}
	if c := fg.RGBAAt(5, 5); c != (color.RGBA{}) {
		t.Errorf("margin pixel = %v, want transparent", c)
	}
	if c := fg.RGBAAt(108, 108); c.A != 0xff {
		t.Errorf("centre pixel = %v, want the opaque scene", c)
	}
}
