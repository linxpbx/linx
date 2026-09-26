package numbering

import (
	"regexp"
	"strings"
	"testing"
)

func TestClassifyUAE(t *testing.T) {
	for _, tc := range []struct {
		dialled  string
		category Category
		e164     string
		label    string
	}{
		{"050 123 4567", Mobile, "+971501234567", ""},
		{"0501234567", Mobile, "+971501234567", ""},
		{"+971 50 123 4567", Mobile, "+971501234567", ""},
		{"00971501234567", Mobile, "+971501234567", ""},
		{"971501234567", Mobile, "+971501234567", ""},
		{"04 234 5678", Landline, "+97142345678", ""},
		{"(04) 234-5678", Landline, "+97142345678", ""},
		{"800 123", TollFree, "+971800123", ""},
		{"600 522222", National, "+971600522222", ""},
		{"900 234567", Premium, "+971900234567", ""},
		{"+44 20 7946 0958", International, "+442079460958", ""},
		{"0044 20 7946 0958", International, "+442079460958", ""},
		{"+1 212 555 0100", International, "+12125550100", ""},
		{"+1 650 253 0000", International, "+16502530000", ""},
		{"999", Emergency, "", "police"},
		{"998", Emergency, "", "ambulance"},
		{"997", Emergency, "", "fire"},
		{"112", Emergency, "", "emergency"},
		{"901", Emergency, "", "police (non-emergency)"},
		{"4451", Service, "", ""},
		{"+999", Invalid, "", ""},
		{"12", Invalid, "", ""},
		{"050123456a", Invalid, "", ""},
		{"*43", Invalid, "", ""},
		{"", Invalid, "", ""},
		{"+", Invalid, "", ""},
	} {
		r := Classify("AE", tc.dialled)
		if r.Category != tc.category || r.E164 != tc.e164 || r.Label != tc.label {
			t.Errorf("Classify(AE, %q) = %+v, want %s %q %q", tc.dialled, r, tc.category, tc.e164, tc.label)
		}
	}
}

func TestPGPattern(t *testing.T) {
	for in, want := range map[string]string{
		`5[02-68]\d{7}`:      `5[02-68][0-9]{7}`,
		`[\d]{2}|(?:1\d)?`:   `[0-9]{2}|(?:1[0-9])?`,
		`00(?:[1-9]\d)`:      `00(?:[1-9][0-9])`,
		`[2-4679][2-8]\d{6}`: `[2-4679][2-8][0-9]{6}`,
	} {
		if got := PGPattern(in); got != want {
			t.Errorf("PGPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// The SQL functions assume libphonenumber's patterns use only constructs
// PostgreSQL reads the same way. A data update that brings anything else
// fails here, before it reaches a server.
func TestBuildPatternsArePortable(t *testing.T) {
	d, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Regions) < 200 || len(d.Descs) < 1000 || len(d.Short) < 200 || d.Version == "" {
		t.Fatalf("suspiciously little data: %d regions, %d descs, %d short, version %q",
			len(d.Regions), len(d.Descs), len(d.Short), d.Version)
	}
	portable := regexp.MustCompile(`^[0-9|?*+{},\-\[\]()$^]*$`)
	check := func(where, p string) {
		t.Helper()
		// "NA" is libphonenumber's "no numbers of this kind"; it matches no
		// digits in either engine.
		if p != "NA" && !portable.MatchString(strings.ReplaceAll(p, "(?:", "")) {
			t.Errorf("%s: pattern %q uses a construct the SQL functions weren't checked with", where, p)
		}
	}
	var ae *Region
	for i, r := range d.Regions {
		for _, p := range []string{r.InternationalPrefix, r.NationalPrefixForParsing, r.LeadingDigits, r.GeneralPattern} {
			check(r.Region, p)
		}
		if !regexp.MustCompile(`^(?:[0-9]*\$[12])*[0-9]*$`).MatchString(r.TransformRule) {
			t.Errorf("%s: transform rule %q", r.Region, r.TransformRule)
		}
		if r.Region == "AE" {
			ae = &d.Regions[i]
		}
	}
	for _, x := range d.Descs {
		check(x.Region+" "+x.NumberType, x.Pattern)
	}
	for _, s := range d.Short {
		for _, p := range []string{s.GeneralPattern, s.ShortCodePattern, s.EmergencyPattern} {
			check(s.Region+" short", p)
		}
	}
	if ae == nil || ae.CallingCode != 971 || ae.NationalPrefix != "0" || ae.InternationalPrefix != "00" {
		t.Fatalf("UAE rules: %+v", ae)
	}
	d2, err := Build()
	if err != nil || d2.Version != d.Version {
		t.Fatalf("Build isn't deterministic: %q vs %q (%v)", d.Version, d2.Version, err)
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{"+971 (50) 123-45.67": "+971501234567", "0501234567": "0501234567"} {
		if got, ok := Clean(in); !ok || got != want {
			t.Errorf("Clean(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "+", "++971", "050#", "٠٥٠١٢٣٤٥٦٧", "50 x 12"} {
		if _, ok := Clean(in); ok {
			t.Errorf("Clean(%q) accepted", in)
		}
	}
}

func TestExplain(t *testing.T) {
	r := Route{Result: Classify("AE", "0501234567"), Reason: ReasonNoLines}
	got := r.Explain("0501234567", "101", "AE")
	if !strings.Contains(got, "Mobile number: +971 50 123 4567.") || !strings.Contains(got, "no outside line is set up") {
		t.Errorf("Explain = %q", got)
	}
	r = Route{Result: Classify("AE", "+44 20 7946 0958"), Reason: ReasonNoLines}
	if got := r.Explain("", "101", "AE"); !strings.Contains(got, "International number in United Kingdom") {
		t.Errorf("Explain = %q", got)
	}
	r = Route{Result: Classify("AE", "999"), Allowed: true, Reason: ReasonEmergency}
	if got := r.Explain("999", "101", "AE"); !strings.Contains(got, "Emergency number (police): 999.") || !strings.Contains(got, "Always allowed") {
		t.Errorf("Explain = %q", got)
	}
	r = Route{Result: Classify("AE", "0501234567"), Allowed: true, Reason: ReasonAllowed, Lines: []Line{
		{Trunk: "UCM", Number: "0501234567", CallerID: "+97142000100"}, {Trunk: "Backup", Number: "+971501234567"}}}
	want := "Allowed for extension 101.\nGoes out on \"UCM\" as 0501234567, showing +97142000100.\n" +
		"If that line is down or full: \"Backup\" as +971501234567.\n"
	if got := r.Explain("0501234567", "101", "AE"); !strings.HasSuffix(got, want) {
		t.Errorf("Explain = %q, want it to end %q", got, want)
	}
	r = Route{Result: Classify("AE", "999"), Allowed: true, Reason: ReasonEmergency, Lines: []Line{{Trunk: "UCM", Number: "999"}}}
	if got := r.Explain("999", "101", "AE"); !strings.HasSuffix(got, "never limited.\nGoes out on \"UCM\" as 999.\n") {
		t.Errorf("Explain = %q", got)
	}
	r = Route{Result: Classify("AE", "0501234567"), Reason: ReasonNotPermitted}
	if got := r.Explain("0501234567", "101", "AE"); !strings.Contains(got, "isn't allowed to call this kind of number") || strings.Contains(got, "No outside line") {
		t.Errorf("Explain = %q, want the refusal alone", got)
	}
	if got := ClashText("0123", ClashNationalPrefix, "AE"); !strings.Contains(got, "can't start with 0") {
		t.Errorf("ClashText = %q", got)
	}
}
