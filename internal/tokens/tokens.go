// Package tokens loads design/tokens.json and renders it for web (CSS) and iOS (asset catalog).
package tokens

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Color is a themed colour value.
type Color struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
	Use   string `json:"use,omitempty"`
}

// ContrastRule requires fg on bg to reach Min in both light and dark themes.
type ContrastRule struct {
	FG  string  `json:"fg"`
	BG  string  `json:"bg"`
	Min float64 `json:"min"`
}

// Tokens mirrors design/tokens.json.
type Tokens struct {
	Color    map[string]Color  `json:"color"`
	Status   map[string]Color  `json:"status"`
	Font     map[string]string `json:"font"`
	Space    map[string]int    `json:"space"`
	Radius   map[string]int    `json:"radius"`
	Contrast []ContrastRule    `json:"contrast"`
}

// Load reads and validates a tokens file.
func Load(path string) (*Tokens, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for group, m := range map[string]map[string]Color{"color": t.Color, "status": t.Status} {
		for name, c := range m {
			for _, hex := range []string{c.Light, c.Dark} {
				if _, err := parseHex(hex); err != nil {
					return nil, fmt.Errorf("%s.%s: %w", group, name, err)
				}
			}
		}
	}
	return &t, nil
}

// Lookup resolves a reference like "color.text" or "status.away".
func (t *Tokens) Lookup(ref string) (Color, bool) {
	group, name, ok := strings.Cut(ref, ".")
	if !ok {
		return Color{}, false
	}
	var c Color
	switch group {
	case "color":
		c, ok = t.Color[name]
	case "status":
		c, ok = t.Status[name]
	default:
		ok = false
	}
	return c, ok
}

// CheckContrast returns one message per rule that fails in either theme.
func (t *Tokens) CheckContrast() []string {
	var fails []string
	for _, r := range t.Contrast {
		fg, ok1 := t.Lookup(r.FG)
		bg, ok2 := t.Lookup(r.BG)
		if !ok1 || !ok2 {
			fails = append(fails, fmt.Sprintf("unknown token in rule %s on %s", r.FG, r.BG))
			continue
		}
		for _, theme := range []struct{ name, fg, bg string }{
			{"light", fg.Light, bg.Light}, {"dark", fg.Dark, bg.Dark},
		} {
			ratio := ContrastRatio(theme.fg, theme.bg)
			if ratio+1e-9 < r.Min {
				fails = append(fails, fmt.Sprintf("%s: %s (%s) on %s (%s) = %.2f:1, need %.1f:1",
					theme.name, r.FG, theme.fg, r.BG, theme.bg, ratio, r.Min))
			}
		}
	}
	return fails
}

// ContrastRatio implements the WCAG 2.x contrast ratio for two #RRGGBB colours.
func ContrastRatio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	rgb, _ := parseHex(hex)
	lin := func(c uint8) float64 {
		s := float64(c) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(rgb[0]) + 0.7152*lin(rgb[1]) + 0.0722*lin(rgb[2])
}

func parseHex(hex string) ([3]uint8, error) {
	var rgb [3]uint8
	if len(hex) != 7 || hex[0] != '#' {
		return rgb, fmt.Errorf("colour %q must be #RRGGBB", hex)
	}
	for i := range 3 {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return rgb, fmt.Errorf("colour %q: %w", hex, err)
		}
		rgb[i] = uint8(v)
	}
	return rgb, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
