package numbering

import (
	"slices"
	"testing"
)

// Linx can be set up in any country libphonenumber knows (ADR-085), each
// with the facts the setup wizard shows.
func TestCountries(t *testing.T) {
	if len(Countries) < 200 {
		t.Fatalf("only %d countries", len(Countries))
	}
	list := CountryList()
	if !slices.IsSortedFunc(list, func(a, b Country) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	}) {
		t.Error("CountryList isn't sorted by name")
	}
	for _, c := range []struct {
		region, name, prefix string
		emergency            []string
	}{
		{"AE", "United Arab Emirates", "0", []string{"999", "998", "997", "112", "901"}},
		{"US", "United States", "1", []string{"911"}},
		{"GB", "United Kingdom", "0", []string{"999", "112"}},
		{"AU", "Australia", "0", []string{"000"}},
		{"RU", "Russia", "8", []string{"112"}},
	} {
		info, ok := CountryInfo(c.region)
		if !ok || info.Name != c.name || info.NationalPrefix != c.prefix {
			t.Errorf("%s: %+v, %v", c.region, info, ok)
			continue
		}
		var got []string
		for _, e := range info.Emergency {
			got = append(got, e.Number)
		}
		for _, n := range c.emergency {
			if !slices.Contains(got, n) {
				t.Errorf("%s: emergency numbers %v miss %s", c.region, got, n)
			}
		}
	}
	if _, ok := CountryInfo("XX"); ok || Supported("XX") || Supported("001") {
		t.Error("XX or 001 is offered")
	}
}
