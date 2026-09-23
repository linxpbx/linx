package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		args     []string
		wantCode int
		wantOut  string
	}{
		{nil, 2, ""},
		{[]string{"version"}, 0, "linx "},
		{[]string{"help"}, 0, "Commands:"},
		{[]string{"setup", "--bogus"}, 2, ""},
		{[]string{"doctor", "--bogus"}, 2, ""},
		{[]string{"bogus"}, 2, ""},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		if code := run(tt.args, &out, &errOut); code != tt.wantCode {
			t.Errorf("run(%v) = %d, want %d", tt.args, code, tt.wantCode)
		}
		if !strings.Contains(out.String(), tt.wantOut) {
			t.Errorf("run(%v) stdout = %q, want it to contain %q", tt.args, out.String(), tt.wantOut)
		}
	}
}
