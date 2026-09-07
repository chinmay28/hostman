// Command icongen cuts the app icons out of art/hostman-logo.png.
//
// The logo is the one drawing of HostMan: the mascot in front of his racks,
// with the wordmark underneath. Icons are a square crop of the head and
// crossed arms, taken from the same source so the two always match. Run it
// after the logo changes:
//
//	make icons
//
// It lives in its own module so the server does not carry an image-scaling
// dependency it never uses.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// badge is the colour inside the logo's badge. The white page around the
// badge is painted over with it before cropping, so the icons' corners
// belong to the badge rather than to the paper it was drawn on.
var badge = color.RGBA{R: 0x01, G: 0x12, B: 0x25, A: 0xff}

// focus is where the icons look: the middle of the mascot's chest, in the
// logo's own pixels. Each icon is a square around it.
var focus = image.Point{X: 630, Y: 365}

// The corner radius of an iOS-style tile, as a fraction of its side.
const tileRadius = 0.2237

// logoSize is the side of logo.png, the whole drawing on a transparent
// background: the app throws it over a blurred screen when the header's mark
// is double-tapped, and nothing larger than this is ever shown.
const logoSize = 800

// targets are the files written. Each shape suits a different consumer:
// browsers take the rounded tile as-is, iOS masks a full square itself, and
// Android's maskable icons are cropped wider so a circular launcher mask
// still keeps the face inside its safe zone.
var targets = []struct {
	name    string
	size    int
	box     int // side of the square cut from the logo, in its pixels
	rounded bool
}{
	{"icon-192.png", 192, 730, true},
	{"icon-512.png", 512, 730, true},
	{"apple-touch-icon.png", 180, 730, false},
	{"icon-512-maskable.png", 512, 900, false},
}

func main() {
	in := flag.String("in", "hostman-logo.png", "the logo to cut icons from")
	out := flag.String("out", ".", "directory to write the PNGs into")
	flag.Parse()

	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "icongen:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	logo, err := png.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}

	if err := write(filepath.Join(out, "logo.png"), Scale(Matte(logo, color.RGBA{}), logoSize)); err != nil {
		return err
	}
	fmt.Println("wrote", filepath.Join(out, "logo.png"))

	matted := Matte(logo, badge)
	for _, t := range targets {
		icon := Scale(CropSquare(matted, focus, t.box, badge), t.size)
		if t.rounded {
			RoundCorners(icon, tileRadius)
		}
		path := filepath.Join(out, t.name)
		if err := write(path, icon); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}

func write(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
