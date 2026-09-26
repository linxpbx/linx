package numbering

import (
	"fmt"
	"strings"

	"github.com/nyaruka/phonenumbers"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// Route is the outgoing-call decision (numbering_route in the database).
type Route struct {
	Result
	Allowed bool
	Reason  string // one of the Reason constants
	// Lines are the trunks an allowed call goes out on, in the order
	// they're tried (numbering_lines).
	Lines []Line
}

// Line is one trunk a call can go out on, and what it's sent as there.
type Line struct {
	Trunk    string // the trunk's name
	Number   string // the number as that trunk is sent it
	CallerID string // what the called person sees; empty: the provider's default
}

// Why a call is or isn't allowed.
const (
	ReasonEmergency     = "emergency"      // always allowed
	ReasonInvalid       = "invalid"        // not a number
	ReasonUnknownCaller = "unknown_caller" // the caller's extension doesn't exist or is turned off
	// ReasonNotPermitted is refused because the caller's call permission
	// level (docs/TRUNKS.md §5) doesn't allow this category, or it has no
	// level assigned at all: fail closed, so a newly created extension
	// can't dial out (other than emergency numbers) until an admin
	// assigns it a level.
	ReasonNotPermitted = "not_permitted"
	ReasonNoLines      = "no_lines" // allowed, but no trunk is set up for outgoing calls
	ReasonAllowed      = "allowed"  // goes out on Lines
)

// Clash is an extension whose number can't be used in the country
// (numbering_extension_clash).
type Clash struct {
	Number string
	Reason string // one of the Clash constants
}

// Why an extension number is reserved.
const (
	ClashNationalPrefix      = "national_prefix"
	ClashInternationalPrefix = "international_prefix"
	ClashEmergency           = "emergency"
	ClashService             = "service"
)

// ClashText explains in plain words why number can't be an extension in
// country.
func ClashText(number, reason string, country string) string {
	c := Countries[country]
	switch reason {
	case ClashNationalPrefix, ClashInternationalPrefix:
		return fmt.Sprintf("Extension numbers can't start with %s: in %s that's how people start dialling outside numbers.",
			firstDigit(number), CountryName(country, c))
	case ClashEmergency:
		return fmt.Sprintf("%s is an emergency number in %s, so it can't be an extension.", number, CountryName(country, c))
	case ClashService:
		return fmt.Sprintf("%s is a short service number in %s, so it can't be an extension.", number, CountryName(country, c))
	}
	return fmt.Sprintf("%s can't be an extension number.", number)
}

func firstDigit(n string) string {
	if n == "" {
		return n
	}
	return n[:1]
}

// CountryName is a region's name in English ("United Kingdom").
func CountryName(region string, c Country) string {
	if c.Name != "" {
		return c.Name
	}
	if region == "001" {
		return "no single country (an international service)"
	}
	r, err := language.ParseRegion(region)
	if err != nil {
		return region
	}
	if name := display.English.Regions().Name(r); name != "" {
		return name
	}
	return region
}

// Kind names what the number is, in plain words.
func (r Result) Kind() string {
	switch r.Category {
	case Emergency:
		return "Emergency number (" + r.Label + ")"
	case Service:
		return "Short service number"
	case Invalid:
		return "Not a phone number"
	case International:
		return "International number in " + CountryName(r.Region, Countries[r.Region])
	case Premium:
		if r.Region != "" && !Supported(r.Region) {
			return "Premium-rate number (expensive to call) in " + CountryName(r.Region, Countries[r.Region])
		}
		return "Premium-rate number (expensive to call)"
	}
	switch r.NumberType {
	case TypeFixedLine:
		return "Landline"
	case TypeFixedLineOrMobile:
		return "Landline or mobile number"
	case TypeMobile:
		return "Mobile number"
	case TypeTollFree:
		return "Toll-free number"
	case TypeSharedCost:
		return "Shared-cost number (the caller pays part of the cost)"
	case TypeUAN:
		return "Company number (one number for a whole company)"
	case TypeVoIP:
		return "Internet phone number"
	case TypePersonalNumber:
		return "Personal number"
	case TypePager:
		return "Pager number"
	case TypeVoicemail:
		return "Voicemail access number"
	}
	return "Phone number"
}

// Pretty is the number written for people: "+971 50 123 4567".
func (r Result) Pretty() string {
	if r.E164 == "" {
		return r.Dial
	}
	n, err := phonenumbers.Parse(r.E164, "")
	if err != nil {
		return r.E164
	}
	return phonenumbers.Format(n, phonenumbers.INTERNATIONAL)
}

// Explain describes the decision for a call from extension from, in plain
// words, as `linx route test` prints it.
func (r Route) Explain(dialled, from, country string) string {
	var b strings.Builder
	switch {
	case r.Reason == ReasonUnknownCaller:
		fmt.Fprintf(&b, "Extension %s doesn't exist or is turned off, so it can't call out.\n", from)
		return b.String()
	case r.Category == Invalid:
		fmt.Fprintf(&b, "%q isn't a number that can be called from %s.\n", dialled, CountryName(country, Countries[country]))
		return b.String()
	}
	fmt.Fprintf(&b, "%s: %s.\n", r.Kind(), r.Pretty())
	switch r.Reason {
	case ReasonEmergency:
		b.WriteString("Always allowed, for everyone, and never limited.\n")
	case ReasonNotPermitted:
		fmt.Fprintf(&b, "Extension %s isn't allowed to call this kind of number (its call permission level doesn't include it, or it has none).\n", from)
	case ReasonNoLines:
		b.WriteString("Allowed, but no outside line is set up for outgoing calls, so the call can't go out until one is.\n")
		return b.String()
	default:
		fmt.Fprintf(&b, "Allowed for extension %s.\n", from)
	}
	if len(r.Lines) == 0 {
		b.WriteString("No outside line is set up for outgoing calls, so it can't go out until one is.\n")
	}
	for i, l := range r.Lines {
		if i == 0 {
			fmt.Fprintf(&b, "Goes out on %q as %s", l.Trunk, l.Number)
		} else {
			fmt.Fprintf(&b, "If that line is down or full: %q as %s", l.Trunk, l.Number)
		}
		if l.CallerID != "" {
			fmt.Fprintf(&b, ", showing %s", l.CallerID)
		}
		b.WriteString(".\n")
	}
	return b.String()
}
