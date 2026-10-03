package tokens

import (
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
)

// The app's icon (design/app-icon.svg, rendered by ios/tools/icon.sh). It is
// checked here rather than looked at, because an icon that is a little off
// centre, a little transparent or a little the wrong blue looks fine in a
// review and wrong on a home screen — which is exactly what happened on
// 2026-10-03, when the mark shipped 110 px to the right of centre.
const appIconFile = "../../ios/Linx/Resources/Assets.xcassets/AppIcon.appiconset/AppIcon-1024.png"

func TestAppIcon(t *testing.T) {
	f, err := os.Open(appIconFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("the app icon isn't a PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 1024 || b.Dy() != 1024 {
		t.Fatalf("the app icon is %dx%d, want 1024x1024", b.Dx(), b.Dy())
	}
	// The App Store refuses an icon with see-through pixels.
	if o, ok := img.(interface{ Opaque() bool }); !ok || !o.Opaque() {
		t.Error("the app icon has an alpha channel; ios/tools/icon.sh takes it out")
	}

	tk, err := Load(tokensFile)
	if err != nil {
		t.Fatal(err)
	}
	background, mark := strings.ToUpper(tk.Color["brand-fill"].Light), strings.ToUpper(tk.Color["on-brand"].Light)
	if background == "" || mark == "" {
		t.Fatal("design/tokens.json has no brand-fill or on-brand")
	}
	if got := hexAt(img, b.Min.X+8, b.Min.Y+8); got != background {
		t.Errorf("the icon's background is %s, want brand-fill %s", got, background)
	}

	// Where the mark is: the box around every pixel of the mark's colour.
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if hexAt(img, x, y) != mark {
				continue
			}
			minX, minY = min(minX, x), min(minY, y)
			maxX, maxY = max(maxX, x), max(maxY, y)
		}
	}
	if minX > maxX {
		t.Fatalf("no %s pixel in the app icon: the mark isn't there", mark)
	}
	// Centred, give or take a pixel of rounding, and with a margin around it
	// (a mark that filled the square would be cropped by the system's mask).
	left, right := minX-b.Min.X, b.Max.X-1-maxX
	top, bottom := minY-b.Min.Y, b.Max.Y-1-maxY
	if abs(left-right) > 2 {
		t.Errorf("the mark is off centre across: %d px left, %d px right", left, right)
	}
	if abs(top-bottom) > 2 {
		t.Errorf("the mark is off centre down: %d px top, %d px bottom", top, bottom)
	}
	if left < 80 || top < 80 {
		t.Errorf("the mark has no room around it: %d px across, %d px down", left, top)
	}
}

// hexAt is one pixel as "#RRGGBB", as the tokens are written.
func hexAt(img image.Image, x, y int) string {
	r, g, b, _ := img.At(x, y).RGBA()
	const hex = "0123456789ABCDEF"
	out := []byte("#......")
	for i, v := range []uint32{r >> 8, g >> 8, b >> 8} {
		out[1+i*2], out[2+i*2] = hex[v>>4&0xF], hex[v&0xF]
	}
	return string(out)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
