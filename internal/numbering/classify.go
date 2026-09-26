package numbering

import (
	"slices"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// Result is what a dialled number is. The SQL function numbering_classify
// returns the same fields.
type Result struct {
	Category   Category
	NumberType string // one of the Type constants; "" for emergency, service and invalid
	Region     string // the number's country ("001" for non-geographic numbers); "" if unknown
	E164       string // "+971501234567"; "" for emergency, service and invalid
	Dial       string // what goes to the line: E164, or the short number as dialled
	Label      string // for emergency numbers: "police", "ambulance", or "emergency"
}

// Classify decides what dialled is for a caller in home (a region code),
// using libphonenumber directly. It is the reference the SQL function is
// tested against; outgoing calls use the SQL function.
func Classify(home, dialled string) Result {
	s, ok := Clean(dialled)
	if !ok {
		return Result{Category: Invalid}
	}
	if !strings.HasPrefix(s, "+") {
		if label, ok := emergency(home, s); ok {
			return Result{Category: Emergency, Region: home, Dial: s, Label: label}
		}
		if isService(home, s) {
			return Result{Category: Service, Region: home, Dial: s}
		}
	}
	num, err := phonenumbers.Parse(s, home)
	if err != nil || !phonenumbers.IsValidNumber(num) {
		return Result{Category: Invalid}
	}
	t := typeName(phonenumbers.GetNumberType(num))
	e164 := phonenumbers.Format(num, phonenumbers.E164)
	r := Result{
		Category: categoryFor(t), NumberType: t, Region: phonenumbers.GetRegionCodeForNumber(num),
		E164: e164, Dial: e164,
	}
	if int(num.GetCountryCode()) != phonenumbers.GetCountryCodeForRegion(home) && r.Category != Premium {
		r.Category = International
	}
	return r
}

func categoryFor(t string) Category {
	switch t {
	case TypeFixedLine, TypeFixedLineOrMobile:
		return Landline
	case TypeMobile:
		return Mobile
	case TypeTollFree:
		return TollFree
	case TypePremiumRate:
		return Premium
	case TypeSharedCost:
		return SharedCost
	}
	return National
}

func emergency(home, s string) (string, bool) {
	if c, ok := Countries[home]; ok {
		if i := slices.IndexFunc(c.Always, func(a Always) bool { return a.Number == s }); i >= 0 {
			return c.Always[i].Label, true
		}
	}
	if phonenumbers.IsEmergencyNumber(s, home) {
		return "emergency", true
	}
	return "", false
}

// isService is libphonenumber's isValidShortNumberForRegion for a number
// dialled in home, without parsing it first: it matches the region's short
// numbers and its short codes.
func isService(home, s string) bool {
	coll, err := phonenumbers.ShortNumberMetadataCollection()
	if err != nil {
		return false
	}
	for _, m := range coll.GetMetadata() {
		if m.GetId() == home {
			return matchesDesc(s, m.GetGeneralDesc()) && matchesDesc(s, m.GetShortCode())
		}
	}
	return false
}

func matchesDesc(s string, d *phonenumbers.PhoneNumberDesc) bool {
	if d == nil || d.GetNationalNumberPattern() == "" {
		return false
	}
	if l := d.GetPossibleLength(); len(l) > 0 && !slices.Contains(l, int32(len(s))) {
		return false
	}
	return phonenumbers.MatchNationalNumber(s, d, false)
}
