package trunk

import (
	"slices"
	"testing"
)

func TestCheckAbroadCountries(t *testing.T) {
	got, err := checkAbroadCountries([]string{"SA", "GB", "SA"})
	if err != nil || !slices.Equal(got, []string{"GB", "SA"}) {
		t.Errorf("= %v, %v; want [GB SA]", got, err)
	}
	if got, err := checkAbroadCountries(nil); err != nil || len(got) != 0 {
		t.Errorf("empty = %v, %v", got, err)
	}
	for _, bad := range []string{"XX", "sa", "SAU", ""} {
		if _, err := checkAbroadCountries([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Reaching a country calls abroad couldn't reach before needs the same
// confirmation as allowing calls abroad at all.
func TestWidensAbroad(t *testing.T) {
	for _, c := range []struct {
		before, after []string
		wider         bool
	}{
		{[]string{"SA"}, nil, true},                   // to everywhere
		{nil, []string{"SA"}, false},                  // from everywhere to one
		{[]string{"SA"}, []string{"SA", "GB"}, true},  // one more
		{[]string{"SA", "GB"}, []string{"SA"}, false}, // one fewer
		{[]string{"SA"}, []string{"SA"}, false},       // the same
		{nil, nil, false},
	} {
		if got := widensAbroad(c.before, c.after); got != c.wider {
			t.Errorf("widensAbroad(%v, %v) = %v", c.before, c.after, got)
		}
	}
}
