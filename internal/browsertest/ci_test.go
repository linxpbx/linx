package browsertest

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// CI runs the browser suite once per front door, side by side
// (.github/workflows/ci.yml, job "browser"). A door added to frontDoors but
// not to that list would silently go untested: this fails instead.
func TestCIRunsEveryFrontDoor(t *testing.T) {
	ci, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s+door: \[([^\]]*)\]`).FindSubmatch(ci)
	if m == nil {
		t.Fatal(`ci.yml has no "door: [...]" matrix for the browser suite`)
	}
	var doors []string
	for _, d := range strings.Split(string(m[1]), ",") {
		doors = append(doors, strings.TrimSpace(d))
	}
	want := slices.Clone(frontDoors)
	slices.Sort(doors)
	slices.Sort(want)
	if !slices.Equal(doors, want) {
		t.Errorf("ci.yml's browser suite doors = %v, want every front door %v", doors, want)
	}
}
