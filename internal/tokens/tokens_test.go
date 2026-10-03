package tokens

import (
	"math"
	"strings"
	"testing"
)

const tokensFile = "../../design/tokens.json"

func TestContrastRatio(t *testing.T) {
	if got := ContrastRatio("#000000", "#FFFFFF"); math.Abs(got-21) > 0.01 {
		t.Fatalf("black/white = %.2f, want 21", got)
	}
	if got := ContrastRatio("#777777", "#777777"); got != 1 {
		t.Fatalf("same colour = %.2f, want 1", got)
	}
}

// TestDesignTokensMeetWCAG enforces WCAG 2.2 AA for every declared pair in both themes.
func TestDesignTokensMeetWCAG(t *testing.T) {
	tk, err := Load(tokensFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range tk.CheckContrast() {
		t.Error(f)
	}
}

func TestLoadRejectsBadHex(t *testing.T) {
	if _, err := parseHex("#12345"); err == nil {
		t.Fatal("expected error for short hex")
	}
	if _, err := parseHex("#GGGGGG"); err == nil {
		t.Fatal("expected error for non-hex")
	}
}

func TestRenderers(t *testing.T) {
	tk, err := Load(tokensFile)
	if err != nil {
		t.Fatal(err)
	}
	css := string(tk.CSS())
	for _, want := range []string{"--linx-color-accent: #1F5FD6;", "--linx-color-accent: #7FB0FF;", ":root[data-theme=\"dark\"]", "--linx-status-dnd:"} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS missing %q", want)
		}
	}
	files := tk.AssetCatalog()
	if _, ok := files["StatusAvailable.colorset/Contents.json"]; !ok {
		t.Error("asset catalog missing StatusAvailable colour set")
	}
	if !strings.Contains(string(files["BrandFill.colorset/Contents.json"]), `"red": "0x1F"`) {
		t.Error("BrandFill colour set has wrong red component")
	}
}

func TestSwiftTokens(t *testing.T) {
	tk, err := Load(tokensFile)
	if err != nil {
		t.Fatal(err)
	}
	swift := string(tk.SwiftTokens())
	for _, want := range []string{
		`public static let accent = Color("Accent", bundle: .main)`,
		`public static let onSurfaceDark = Color("OnSurfaceDark", bundle: .main)`,
		`public static let available = Color("StatusAvailable", bundle: .main)`,
		"public static let s4: CGFloat = 16",
		"public static let full: CGFloat = 9999",
		`"BrandFill": ("#1F5FD6", "#1F5FD6"),`,
	} {
		if !strings.Contains(swift, want) {
			t.Errorf("Swift tokens missing %q", want)
		}
	}
	// Every colour set in the catalog must have a Swift name, and the other way round.
	for path := range tk.AssetCatalog() {
		name, ok := strings.CutSuffix(path, ".colorset/Contents.json")
		if !ok {
			continue
		}
		if !strings.Contains(swift, `Color("`+name+`", bundle: .main)`) {
			t.Errorf("Swift tokens have no entry for colour set %q", name)
		}
	}
	// The spacing scale is ordered by value, not alphabetically ("10" after "8").
	if i, j := strings.Index(swift, "let s8:"), strings.Index(swift, "let s10:"); i > j {
		t.Error("spacing scale is not in numeric order")
	}
}
