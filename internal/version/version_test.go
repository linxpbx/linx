package version

import "testing"

func TestString(t *testing.T) {
	Version, Commit = "1.2.3", "abc123"
	if got, want := String("linx"), "linx 1.2.3 (abc123)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
