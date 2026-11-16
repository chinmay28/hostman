// Command icongen cuts the app icons out of art/hostman-logo.png.
//
// The logo is the one drawing of HostMan: the mascot in front of his racks,
// with the wordmark underneath. Icons are a square crop of the head and
// crossed arms, taken from the same source so the two always match — the
// PWA's, and the Android app's launcher icons and in-app mark. Run it after
// the logo changes:
//
//	make icons
//
// and `make icons-check` (also `go test` here) fails if a committed copy has
// drifted from what the logo makes.
//
// It lives in its own module so the server does not carry an image-scaling
// dependency it never uses.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
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

// The sides of the squares cut from the logo, in its pixels: the icon's own
// crop, and a wider one that keeps the face inside a launcher's circular mask.
const (
	iconBox     = 730
	maskableBox = 900
)

// webTargets are the PWA's files. Each shape suits a different consumer:
// browsers take the rounded tile as-is, iOS masks a full square itself, and
// Android's maskable icons are cropped wider so a circular launcher mask
// still keeps the face inside its safe zone.
var webTargets = []struct {
	name    string
	size    int
	box     int
	rounded bool
}{
	{"icon-192.png", 192, iconBox, true},
	{"icon-512.png", 512, iconBox, true},
	{"apple-touch-icon.png", 180, iconBox, false},
	{"icon-512-maskable.png", 512, maskableBox, false},
}

// densities are Android's screen buckets and their pixels per dp.
var densities = []struct {
	dir   string
	scale float64
}{
	{"mdpi", 1}, {"hdpi", 1.5}, {"xhdpi", 2}, {"xxhdpi", 3}, {"xxxhdpi", 4},
}

// appLogoSize is the in-app mark's side (drawable-nodpi/hostman_logo.png):
// shown at 56–72dp on the setup and status screens, so 256 px is sharp on
// every density.
const appLogoSize = 256

func main() {
	in := flag.String("in", "hostman-logo.png", "the logo to cut icons from")
	out := flag.String("out", ".", "directory to write the PWA's PNGs into")
	android := flag.String("android", "", "the Android app's res/ directory; empty skips the Android icons")
	check := flag.Bool("check", false, "write nothing; fail if a committed file differs from what the logo makes")
	flag.Parse()

	files, err := render(*in, *out, *android)
	if err == nil {
		if *check {
			err = checkFiles(files)
		} else {
			err = writeFiles(files)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "icongen:", err)
		os.Exit(1)
	}
}

// render makes every file from the logo at in, keyed by the path it belongs
// at. Nothing is written.
func render(in, webDir, androidRes string) (map[string][]byte, error) {
	f, err := os.Open(in)
	if err != nil {
		return nil, err
	}
	logo, err := png.Decode(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", in, err)
	}

	files := map[string][]byte{}
	put := func(path string, img image.Image) error {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		files[path] = buf.Bytes()
		return nil
	}

	if err := put(filepath.Join(webDir, "logo.png"), Scale(Matte(logo, color.RGBA{}), logoSize)); err != nil {
		return nil, err
	}
	matted := Matte(logo, badge)
	for _, t := range webTargets {
		icon := Scale(CropSquare(matted, focus, t.box, badge), t.size)
		if t.rounded {
			RoundCorners(icon, tileRadius)
		}
		if err := put(filepath.Join(webDir, t.name), icon); err != nil {
			return nil, err
		}
	}

	if androidRes == "" {
		return files, nil
	}
	for _, d := range densities {
		dir := filepath.Join(androidRes, "mipmap-"+d.dir)
		// The legacy icon: the rounded tile, 48dp.
		legacy := Scale(CropSquare(matted, focus, iconBox, badge), dp(48, d.scale))
		RoundCorners(legacy, tileRadius)
		if err := put(filepath.Join(dir, "ic_launcher.png"), legacy); err != nil {
			return nil, err
		}
		// The adaptive icon's foreground: 108dp, of which a launcher shows
		// the middle 72dp through its own mask. The maskable crop fills that
		// 72dp; the margin is transparent over the badge-coloured background.
		if err := put(filepath.Join(dir, "ic_launcher_foreground.png"), AdaptiveForeground(matted, d.scale)); err != nil {
			return nil, err
		}
	}
	mark := Scale(CropSquare(matted, focus, iconBox, badge), appLogoSize)
	RoundCorners(mark, tileRadius)
	if err := put(filepath.Join(androidRes, "drawable-nodpi", "hostman_logo.png"), mark); err != nil {
		return nil, err
	}
	files[filepath.Join(androidRes, "values", "ic_launcher_background.xml")] = []byte(fmt.Sprintf(
		"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n"+
			"<!-- The logo's badge colour. Generated from art/hostman-logo.png by `make icons`. -->\n"+
			"<resources>\n    <color name=\"ic_launcher_background\">#%02X%02X%02X</color>\n</resources>\n",
		badge.R, badge.G, badge.B))
	// -v26 although the app's minSdk is 29 (lint says so): without the
	// qualifier, lint takes the legacy PNGs beside it for a mistake. The
	// monochrome layer, for Android 13's themed icons, is the hand-drawn
	// silhouette (see README).
	files[filepath.Join(androidRes, "mipmap-anydpi-v26", "ic_launcher.xml")] = []byte(
		"<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" +
			"<!-- Generated from art/hostman-logo.png by `make icons`; edit that, not this. -->\n" +
			"<adaptive-icon xmlns:android=\"http://schemas.android.com/apk/res/android\">\n" +
			"    <background android:drawable=\"@color/ic_launcher_background\" />\n" +
			"    <foreground android:drawable=\"@mipmap/ic_launcher_foreground\" />\n" +
			"    <monochrome android:drawable=\"@drawable/ic_launcher_monochrome\" />\n" +
			"</adaptive-icon>\n")
	return files, nil
}

// AdaptiveForeground is the adaptive icon's foreground layer at one density:
// a 108dp transparent square with the maskable crop in its middle 72dp.
func AdaptiveForeground(matted image.Image, scale float64) *image.RGBA {
	side, inner := dp(108, scale), dp(72, scale)
	scene := Scale(CropSquare(matted, focus, maskableBox, badge), inner)
	fg := image.NewRGBA(image.Rect(0, 0, side, side))
	off := (side - inner) / 2
	for y := 0; y < inner; y++ {
		for x := 0; x < inner; x++ {
			fg.SetRGBA(off+x, off+y, scene.RGBAAt(x, y))
		}
	}
	return fg
}

func dp(n int, scale float64) int { return int(float64(n)*scale + 0.5) }

func writeFiles(files map[string][]byte) error {
	for _, path := range sorted(files) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[path], 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}

// checkFiles fails, naming every one, if a committed file is missing or is
// not what the logo makes now.
func checkFiles(files map[string][]byte) error {
	var stale []string
	for _, path := range sorted(files) {
		have, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(have, files[path]) {
			stale = append(stale, path)
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("out of date with the logo (run `make icons`): %v", stale)
	}
	return nil
}

func sorted(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
