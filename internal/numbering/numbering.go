// Package numbering tells what kind of number someone dialled (docs/TRUNKS.md
// §5, ADR-044): an emergency number, a mobile, a landline, international,
// premium and so on, for the country Linx is set up in.
//
// The decision Asterisk acts on is made in the database, by SQL functions
// (migration 0016) reading tables this package builds from
// github.com/nyaruka/phonenumbers (a Go port of Google's libphonenumber), so
// outgoing calls keep working while the control plane restarts. Classify is
// the same decision made with the library itself; a Docker test checks the
// two agree on thousands of numbers.
package numbering

import "strings"

// Category is what a dialled number is, in the terms call permissions use.
type Category string

const (
	Emergency     Category = "emergency"     // always allowed, never limited
	Service       Category = "service"       // other short numbers the country's data knows
	Landline      Category = "landline"      // fixed line (or "fixed line or mobile" where they can't be told apart)
	Mobile        Category = "mobile"        //
	National      Category = "national"      // other national numbers: one number for a company (UAN), VoIP, personal, pager, voicemail
	SharedCost    Category = "shared_cost"   //
	TollFree      Category = "toll_free"     //
	Premium       Category = "premium"       // at home or abroad
	International Category = "international" // any other number in another country
	Invalid       Category = "invalid"       // not a number that can be called
)

// Country is a country Linx can be set up in. Only countries with a test
// corpus in this package are offered (ADR-044).
type Country struct {
	Region string // ISO 3166 code, as libphonenumber names regions
	Name   string
	// Always are numbers that are always allowed and never limited, on top of
	// the emergency numbers in libphonenumber's data: the owner's list
	// (docs/TRUNKS.md §11 item 8).
	Always []Always
}

// Always is a number that is always allowed.
type Always struct {
	Number string
	Label  string // plain words: "police", "ambulance"
}

// Countries are the countries on offer, by region code.
var Countries = map[string]Country{
	"AE": {Region: "AE", Name: "United Arab Emirates", Always: []Always{
		{"999", "police"},
		{"998", "ambulance"},
		{"997", "fire"},
		{"112", "emergency"},
		{"901", "police (non-emergency)"},
	}},
}

// DefaultCountry is the country until the admin chooses another
// (docs/TRUNKS.md §11 item 3).
const DefaultCountry = "AE"

// Supported reports whether region is a country Linx can be set up in.
func Supported(region string) bool {
	_, ok := Countries[region]
	return ok
}

// Clean removes the spaces, dashes, dots and brackets people type in
// numbers. It reports false if anything else but digits and one leading
// "+" is left.
func Clean(dialled string) (string, bool) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '-', '.', '(', ')':
			return -1
		}
		return r
	}, dialled)
	digits := strings.TrimPrefix(s, "+")
	if digits == "" || len(s) > 250 {
		return s, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return s, false
		}
	}
	return s, true
}
