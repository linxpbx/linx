package main

import "testing"

func TestAllowed(t *testing.T) {
	tests := []struct {
		expr string
		dev  bool
		want bool
	}{
		{"MIT", false, true},
		{"Apache-2.0", false, true},
		{"GPL-3.0-only", false, false},
		{"GPL-3.0-only", true, false},
		{"AGPL-3.0-or-later", true, false},
		{"MPL-2.0", false, false},
		{"MPL-2.0", true, true},
		{"(MIT OR GPL-3.0-only)", false, true},
		{"(MIT AND GPL-3.0-only)", false, false},
		{"", true, false},
	}
	for _, tt := range tests {
		if got := allowed(tt.expr, tt.dev); got != tt.want {
			t.Errorf("allowed(%q, dev=%v) = %v, want %v", tt.expr, tt.dev, got, tt.want)
		}
	}
}
