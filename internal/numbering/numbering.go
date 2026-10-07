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

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nyaruka/phonenumbers"
)

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

// Country is a country Linx can be set up in: any region libphonenumber
// has numbering data for (ADR-085). Each one is proved before it is
// offered: the Docker test compares the database's decision with the
// library's on a corpus of numbers for every country (ADR-044).
type Country struct {
	Region string // ISO 3166 code, as libphonenumber names regions
	Name   string
	// Always are numbers that are always allowed and never limited, on top of
	// the emergency numbers in libphonenumber's data: the owner's list for
	// the UAE (docs/TRUNKS.md §11 item 8). Empty for most countries.
	Always []Always
}

// Always is a number that is always allowed.
type Always struct {
	Number string
	Label  string // plain words: "police", "ambulance"
}

// always are the extra always-allowed numbers, by region.
var always = map[string][]Always{
	"AE": {
		{"999", "police"},
		{"998", "ambulance"},
		{"997", "fire"},
		{"112", "emergency"},
		{"901", "police (non-emergency)"},
	},
}

// Countries are the countries on offer, by region code: every region
// libphonenumber has data for.
var Countries = func() map[string]Country {
	out := map[string]Country{}
	for region := range phonenumbers.GetSupportedRegions() {
		c := Country{Region: region, Always: always[region]}
		c.Name = CountryName(region, c)
		out[region] = c
	}
	return out
}()

// CountryList is every country on offer, sorted by name.
func CountryList() []Country {
	list := make([]Country, 0, len(Countries))
	for _, c := range Countries {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// DefaultCountry is the country until the admin chooses another
// (docs/TRUNKS.md §11 item 3).
const DefaultCountry = "AE"

// Supported reports whether region is a country Linx can be set up in.
func Supported(region string) bool {
	_, ok := Countries[region]
	return ok
}

// Info is what the setup wizard and Outgoing calls show about a country.
type Info struct {
	Country
	// NationalPrefix is what people dial before a national number ("0" in
	// most countries, "1" in North America, "8" in Russia): extension
	// numbers can't start with it.
	NationalPrefix string
	// Emergency are the numbers that always work there, from libphonenumber's
	// data plus Always, shortest first.
	Emergency []Always
}

var infoCache sync.Map // region -> Info

// CountryInfo describes region; ok is false if it isn't on offer.
func CountryInfo(region string) (Info, bool) {
	c, ok := Countries[region]
	if !ok {
		return Info{}, false
	}
	if v, ok := infoCache.Load(region); ok {
		return v.(Info), true
	}
	info := Info{Country: c}
	if coll, err := phonenumbers.MetadataCollection(); err == nil {
		for _, m := range coll.GetMetadata() {
			if m.GetId() == region {
				info.NationalPrefix = m.GetNationalPrefix()
				break
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range c.Always {
		seen[a.Number] = true
		info.Emergency = append(info.Emergency, a)
	}
	// Emergency numbers are short: every 2- and 3-digit number is asked.
	for n := 10; n < 1000; n++ {
		s := strconv.Itoa(n)
		if !seen[s] && phonenumbers.IsEmergencyNumber(s, region) {
			seen[s] = true
			info.Emergency = append(info.Emergency, Always{Number: s, Label: "emergency"})
		}
	}
	for n := 0; n < 100; n++ {
		s := fmt.Sprintf("%03d", n) // "000" (Australia), "010"
		if !seen[s] && phonenumbers.IsEmergencyNumber(s, region) {
			seen[s] = true
			info.Emergency = append(info.Emergency, Always{Number: s, Label: "emergency"})
		}
	}
	sort.SliceStable(info.Emergency, func(i, j int) bool { return len(info.Emergency[i].Number) < len(info.Emergency[j].Number) })
	infoCache.Store(region, info)
	return info, true
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
