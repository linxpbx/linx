// Command tokengen renders design/tokens.json into web CSS and an iOS asset catalog.
// With -check it fails if the generated files are missing or out of date.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"linxpbx.com/linx/internal/tokens"
)

func main() {
	var (
		in    = flag.String("in", "design/tokens.json", "tokens source file")
		css   = flag.String("css", "web/src/styles/tokens.css", "CSS output file")
		xcas  = flag.String("xcassets", "ios/Linx/Resources/Colors.xcassets", "iOS asset catalog directory")
		check = flag.Bool("check", false, "verify outputs are up to date instead of writing")
	)
	flag.Parse()

	tk, err := tokens.Load(*in)
	if err != nil {
		fail(err)
	}
	if fails := tk.CheckContrast(); len(fails) > 0 {
		for _, f := range fails {
			fmt.Fprintln(os.Stderr, "contrast:", f)
		}
		fail(fmt.Errorf("%d contrast rule(s) fail WCAG 2.2 AA", len(fails)))
	}

	outputs := map[string][]byte{*css: tk.CSS()}
	for rel, data := range tk.AssetCatalog() {
		outputs[filepath.Join(*xcas, rel)] = data
	}

	stale := 0
	for path, want := range outputs {
		if *check {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
				fmt.Fprintln(os.Stderr, "out of date:", path)
				stale++
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			fail(err)
		}
	}
	if stale > 0 {
		fail(fmt.Errorf("%d generated file(s) out of date: run `make tokens`", stale))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tokengen:", err)
	os.Exit(1)
}
