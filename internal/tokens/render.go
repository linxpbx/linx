package tokens

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const header = "Generated from design/tokens.json by `make tokens`. Do not edit."

// CSS renders custom properties: light on :root, dark via prefers-color-scheme
// (unless data-theme="light") and via an explicit data-theme="dark".
func (t *Tokens) CSS() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "/* %s */\n\n:root {\n  color-scheme: light;\n", header)
	t.writeColorVars(&b, false)
	for _, k := range sortedKeys(t.Font) {
		fmt.Fprintf(&b, "  --linx-font-%s: %s;\n", k, t.Font[k])
	}
	for _, k := range sortedKeys(t.Space) {
		fmt.Fprintf(&b, "  --linx-space-%s: %dpx;\n", k, t.Space[k])
	}
	for _, k := range sortedKeys(t.Radius) {
		fmt.Fprintf(&b, "  --linx-radius-%s: %dpx;\n", k, t.Radius[k])
	}
	b.WriteString("}\n\n@media (prefers-color-scheme: dark) {\n  :root:not([data-theme=\"light\"]) {\n    color-scheme: dark;\n")
	var dark strings.Builder
	t.writeColorVars(&dark, true)
	b.WriteString(indent(dark.String(), "  "))
	b.WriteString("  }\n}\n\n:root[data-theme=\"dark\"] {\n  color-scheme: dark;\n")
	b.WriteString(dark.String())
	b.WriteString("}\n")
	return []byte(b.String())
}

func (t *Tokens) writeColorVars(b *strings.Builder, dark bool) {
	for _, g := range []struct {
		prefix string
		m      map[string]Color
	}{{"color", t.Color}, {"status", t.Status}} {
		for _, k := range sortedKeys(g.m) {
			v := g.m[k].Light
			if dark {
				v = g.m[k].Dark
			}
			fmt.Fprintf(b, "  --linx-%s-%s: %s;\n", g.prefix, k, v)
		}
	}
}

func indent(s, pad string) string {
	lines := strings.SplitAfter(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "")
}

// AssetCatalog returns Xcode asset-catalog files (relative path → contents),
// one colour set per token with light and dark appearances.
func (t *Tokens) AssetCatalog() map[string][]byte {
	info := map[string]any{"author": "linx-tokengen", "version": 1}
	files := map[string][]byte{
		"Contents.json": mustJSON(map[string]any{"info": info}),
	}
	for _, g := range []struct {
		prefix string
		m      map[string]Color
	}{{"", t.Color}, {"Status", t.Status}} {
		for _, k := range sortedKeys(g.m) {
			c := g.m[k]
			name := g.prefix + pascal(k)
			files[filepath.Join(name+".colorset", "Contents.json")] = mustJSON(map[string]any{
				"colors": []any{
					map[string]any{"idiom": "universal", "color": swiftColor(c.Light)},
					map[string]any{
						"idiom":       "universal",
						"appearances": []any{map[string]string{"appearance": "luminosity", "value": "dark"}},
						"color":       swiftColor(c.Dark),
					},
				},
				"info": info,
			})
		}
	}
	return files
}

func swiftColor(hex string) map[string]any {
	rgb, _ := parseHex(hex)
	comp := func(v uint8) string { return "0x" + strings.ToUpper(strconv.FormatUint(uint64(v)+0x100, 16)[1:]) }
	return map[string]any{
		"color-space": "srgb",
		"components": map[string]string{
			"red": comp(rgb[0]), "green": comp(rgb[1]), "blue": comp(rgb[2]), "alpha": "1.000",
		},
	}
}

func pascal(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// SwiftTokens renders the iOS side of the tokens: the colour-set names from the
// asset catalog, the spacing scale and the radii. Fonts are deliberately absent —
// iOS uses the system font (docs/ui/DESIGN_TOKENS.md).
func (t *Tokens) SwiftTokens() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "// %s\n\nimport SwiftUI\n\n", header)

	b.WriteString("/// Colours from the Colors asset catalog, which `make tokens` generates from the same file.\n")
	b.WriteString("public enum LinxColor {\n")
	for _, k := range sortedKeys(t.Color) {
		fmt.Fprintf(&b, "    /// %s\n", t.Color[k].Use)
		fmt.Fprintf(&b, "    public static let %s = Color(\"%s\", bundle: .main)\n", camel(k), pascal(k))
	}
	b.WriteString("\n    /// Presence colours. Never shown without a text label.\n")
	b.WriteString("    public enum Status {\n")
	for _, k := range sortedKeys(t.Status) {
		fmt.Fprintf(&b, "        public static let %s = Color(\"Status%s\", bundle: .main)\n", camel(k), pascal(k))
	}
	b.WriteString("    }\n}\n\n")

	b.WriteString("/// The spacing scale, in points.\npublic enum LinxSpace {\n")
	for _, k := range sortedNumericKeys(t.Space) {
		fmt.Fprintf(&b, "    public static let s%s: CGFloat = %d\n", k, t.Space[k])
	}
	b.WriteString("}\n\n/// Corner radii, in points.\npublic enum LinxRadius {\n")
	for _, k := range sortedKeys(t.Radius) {
		fmt.Fprintf(&b, "    public static let %s: CGFloat = %d\n", camel(k), t.Radius[k])
	}
	b.WriteString("}\n\n")

	// swift-format indents what is inside an #if, so the generated file does too.
	var dbg strings.Builder
	dbg.WriteString("/// The same colours as plain hex, so the tests can check the asset catalog against\n")
	dbg.WriteString("/// design/tokens.json. Debug builds only, so the shipped app carries none of it.\n")
	dbg.WriteString("public enum LinxTokenReference {\n    public static let colors: [String: (light: String, dark: String)] = [\n")
	for _, g := range []struct {
		prefix string
		m      map[string]Color
	}{{"", t.Color}, {"Status", t.Status}} {
		for _, k := range sortedKeys(g.m) {
			fmt.Fprintf(&dbg, "        %q: (%q, %q),\n", g.prefix+pascal(k), g.m[k].Light, g.m[k].Dark)
		}
	}
	dbg.WriteString("    ]\n}\n")
	b.WriteString("#if DEBUG\n")
	b.WriteString(indent(dbg.String(), "    "))
	b.WriteString("#endif\n")
	return []byte(b.String())
}

func camel(s string) string {
	p := pascal(s)
	return strings.ToLower(p[:1]) + p[1:]
}
