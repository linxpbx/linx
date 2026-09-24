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

func TestClassifyLicence(t *testing.T) {
	tests := []struct {
		text string
		ok   bool
	}{
		{"MIT License\nPermission is hereby granted, free of charge, to any person", true},
		{"Apache License\nVersion 2.0, January 2004", true},
		{"Redistribution and use in source and binary forms, with or without", true},
		{"Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee", true},
		{"Permission to use, copy, modify, and distribute this software for any\npurpose with or without fee is hereby granted, provided", true},
		{"Permission to use, copy, modify, and distribute this software", false},
		{"GNU GENERAL PUBLIC LICENSE Version 3", false},
		{"Permission is hereby granted, free of charge\n...\nGNU Lesser General Public License", false},
		{"Mozilla Public License Version 2.0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := classifyLicence(tt.text) != ""; got != tt.ok {
			t.Errorf("classifyLicence(%.40q) ok = %v, want %v", tt.text, got, tt.ok)
		}
	}
}
