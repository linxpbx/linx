#!/usr/bin/env bash
# Renders design/app-icon.svg into the app icon Xcode compiles
# (Assets.xcassets/AppIcon.appiconset/AppIcon-1024.png), and writes it without
# an alpha channel, which is what the App Store insists on.
#
#   ios/tools/icon.sh
#
# Run it after changing the SVG, and commit the PNG with it. It needs the web
# client's Playwright (make setup-dev), which is the only browser this repo
# already has; nothing else in the iOS build depends on it.
set -euo pipefail

cd "$(dirname "$0")/.."
svg=../design/app-icon.svg
out=Linx/Resources/Assets.xcassets/AppIcon.appiconset/AppIcon-1024.png
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat > "$work/shoot.mjs" <<'JS'
import { chromium } from "playwright";
const [svg, png] = process.argv.slice(2);
const browser = await chromium.launch();
// The icon is opaque and exactly 1024 square; no device scale factor, so one
// CSS pixel is one image pixel.
const page = await browser.newPage({ viewport: { width: 1024, height: 1024 } });
await page.goto("file://" + svg);
await page.screenshot({ path: png, omitBackground: false });
await browser.close();
JS

cp "$work/shoot.mjs" ../web/shoot-icon.mjs
trap 'rm -rf "$work"; rm -f ../web/shoot-icon.mjs' EXIT
(cd ../web && node shoot-icon.mjs "$(cd ../design && pwd)/app-icon.svg" "$work/icon.png")

# Drop the alpha channel: Go's PNG encoder writes a truecolour (no alpha)
# image when every pixel is opaque, which this one is.
cat > "$work/flatten.go" <<'GO'
package main

import (
	"image"
	"image/draw"
	"image/png"
	"os"
)

func main() {
	in, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(in)
	if err != nil {
		panic(err)
	}
	in.Close()
	flat := image.NewRGBA(src.Bounds())
	draw.Draw(flat, flat.Bounds(), src, src.Bounds().Min, draw.Src)
	if !flat.Opaque() {
		panic("the app icon has see-through pixels; the App Store refuses those")
	}
	out, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	if err := png.Encode(out, flat); err != nil {
		panic(err)
	}
	if err := out.Close(); err != nil {
		panic(err)
	}
}
GO
(cd "$work" && go mod init flatten >/dev/null 2>&1 && go run flatten.go icon.png flat.png)
cp "$work/flat.png" "$out"
echo "icon: $out"
