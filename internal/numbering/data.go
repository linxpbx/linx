package numbering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nyaruka/phonenumbers"
)

// Data is the content of the numbering tables (migration 0016), built from
// libphonenumber's metadata. The control plane rewrites the tables at start
// whenever Version differs from what's stored, so a library update (monthly
// numbering data) reaches the database by itself.
type Data struct {
	Version string
	Regions []Region
	Descs   []Desc
	Short   []Short
	Always  []AlwaysRow
}

// Region is one row of numbering_region: one region's parsing rules. The
// same calling code can have several regions (+1: US, CA, ...; Position 0 is
// the main one). Non-geographic codes (+800, +882, ...) have region "001".
type Region struct {
	CallingCode              int
	Region                   string
	Position                 int
	InternationalPrefix      string // regular expression, PostgreSQL syntax
	NationalPrefix           string // digits
	NationalPrefixForParsing string // regular expression, PostgreSQL syntax
	TransformRule            string // "$1"-style replacement, as in the metadata
	LeadingDigits            string // regular expression, PostgreSQL syntax
	SameMobileAndFixed       bool
	GeneralPattern           string
	GeneralLengths           []int32
	GeneralLocalLengths      []int32
}

// Desc is one row of numbering_desc: the pattern for one type of number in
// a region, and the order libphonenumber checks it in.
type Desc struct {
	CallingCode int
	Region      string
	NumberType  string
	CheckOrder  int
	Pattern     string
	Lengths     []int32
}

// Short is one row of numbering_short: a region's short numbers.
type Short struct {
	Region           string
	GeneralPattern   string
	GeneralLengths   []int32
	ShortCodePattern string
	ShortCodeLengths []int32
	EmergencyPattern string
}

// AlwaysRow is one row of numbering_always.
type AlwaysRow struct {
	Region, Number, Label string
}

// Number types, named as the SQL functions return them.
const (
	TypeFixedLine         = "fixed_line"
	TypeMobile            = "mobile"
	TypeFixedLineOrMobile = "fixed_line_or_mobile"
	TypeTollFree          = "toll_free"
	TypePremiumRate       = "premium_rate"
	TypeSharedCost        = "shared_cost"
	TypeVoIP              = "voip"
	TypePersonalNumber    = "personal_number"
	TypePager             = "pager"
	TypeUAN               = "uan"
	TypeVoicemail         = "voicemail"
)

// Fixed line and mobile are checked last and specially (getNumberTypeHelper),
// so they sort after the others.
const (
	orderFixedLine = 100
	orderMobile    = 101
)

func typeName(t phonenumbers.PhoneNumberType) string {
	switch t {
	case phonenumbers.FIXED_LINE:
		return TypeFixedLine
	case phonenumbers.MOBILE:
		return TypeMobile
	case phonenumbers.FIXED_LINE_OR_MOBILE:
		return TypeFixedLineOrMobile
	case phonenumbers.TOLL_FREE:
		return TypeTollFree
	case phonenumbers.PREMIUM_RATE:
		return TypePremiumRate
	case phonenumbers.SHARED_COST:
		return TypeSharedCost
	case phonenumbers.VOIP:
		return TypeVoIP
	case phonenumbers.PERSONAL_NUMBER:
		return TypePersonalNumber
	case phonenumbers.PAGER:
		return TypePager
	case phonenumbers.UAN:
		return TypeUAN
	case phonenumbers.VOICEMAIL:
		return TypeVoicemail
	}
	return ""
}

// Build reads libphonenumber's metadata into Data.
func Build() (*Data, error) {
	coll, err := phonenumbers.MetadataCollection()
	if err != nil {
		return nil, fmt.Errorf("reading libphonenumber metadata: %w", err)
	}
	shortColl, err := phonenumbers.ShortNumberMetadataCollection()
	if err != nil {
		return nil, fmt.Errorf("reading libphonenumber short-number metadata: %w", err)
	}
	d := &Data{}
	// The library's own region order per calling code (main region first),
	// which getRegionCodeForNumber follows.
	position := map[string]int{}
	for cc, regions := range phonenumbers.BuildCountryCodeToRegionMap(coll) {
		for i, r := range regions {
			position[fmt.Sprintf("%d/%s", cc, r)] = i
		}
	}
	for _, m := range coll.GetMetadata() {
		cc := int(m.GetCountryCode())
		key := fmt.Sprintf("%d/%s", cc, m.GetId())
		pos, ok := position[key]
		if !ok {
			return nil, fmt.Errorf("libphonenumber metadata for %s isn't in the calling-code map", key)
		}
		g := m.GetGeneralDesc()
		d.Regions = append(d.Regions, Region{
			CallingCode:              cc,
			Region:                   m.GetId(),
			Position:                 pos,
			InternationalPrefix:      PGPattern(m.GetInternationalPrefix()),
			NationalPrefix:           m.GetNationalPrefix(),
			NationalPrefixForParsing: PGPattern(m.GetNationalPrefixForParsing()),
			TransformRule:            m.GetNationalPrefixTransformRule(),
			LeadingDigits:            PGPattern(m.GetLeadingDigits()),
			SameMobileAndFixed:       m.GetSameMobileAndFixedLinePattern(),
			GeneralPattern:           PGPattern(g.GetNationalNumberPattern()),
			GeneralLengths:           lengths(g.GetPossibleLength()),
			GeneralLocalLengths:      lengths(g.GetPossibleLengthLocalOnly()),
		})
		for _, t := range []struct {
			name  string
			order int
			desc  *phonenumbers.PhoneNumberDesc
		}{
			{TypePremiumRate, 1, m.GetPremiumRate()},
			{TypeTollFree, 2, m.GetTollFree()},
			{TypeSharedCost, 3, m.GetSharedCost()},
			{TypeVoIP, 4, m.GetVoip()},
			{TypePersonalNumber, 5, m.GetPersonalNumber()},
			{TypePager, 6, m.GetPager()},
			{TypeUAN, 7, m.GetUan()},
			{TypeVoicemail, 8, m.GetVoicemail()},
			{TypeFixedLine, orderFixedLine, m.GetFixedLine()},
			{TypeMobile, orderMobile, m.GetMobile()},
		} {
			// A missing description, one with no pattern or "NA" (no numbers
			// of this kind) matches nothing (isNumberMatchingDesc), so it
			// needs no row.
			if p := t.desc.GetNationalNumberPattern(); p == "" || p == "NA" {
				continue
			}
			d.Descs = append(d.Descs, Desc{
				CallingCode: cc, Region: m.GetId(), NumberType: t.name, CheckOrder: t.order,
				Pattern: PGPattern(t.desc.GetNationalNumberPattern()), Lengths: lengths(t.desc.GetPossibleLength()),
			})
		}
	}
	for _, m := range shortColl.GetMetadata() {
		d.Short = append(d.Short, Short{
			Region:           m.GetId(),
			GeneralPattern:   PGPattern(m.GetGeneralDesc().GetNationalNumberPattern()),
			GeneralLengths:   lengths(m.GetGeneralDesc().GetPossibleLength()),
			ShortCodePattern: PGPattern(m.GetShortCode().GetNationalNumberPattern()),
			ShortCodeLengths: lengths(m.GetShortCode().GetPossibleLength()),
			EmergencyPattern: PGPattern(m.GetEmergency().GetNationalNumberPattern()),
		})
	}
	for _, c := range Countries {
		for _, a := range c.Always {
			d.Always = append(d.Always, AlwaysRow{Region: c.Region, Number: a.Number, Label: a.Label})
		}
	}
	sort.Slice(d.Regions, func(i, j int) bool {
		a, b := d.Regions[i], d.Regions[j]
		return a.CallingCode < b.CallingCode || a.CallingCode == b.CallingCode && a.Position < b.Position
	})
	sort.Slice(d.Descs, func(i, j int) bool {
		a, b := d.Descs[i], d.Descs[j]
		if a.CallingCode != b.CallingCode {
			return a.CallingCode < b.CallingCode
		}
		if a.Region != b.Region {
			return a.Region < b.Region
		}
		return a.CheckOrder < b.CheckOrder
	})
	sort.Slice(d.Short, func(i, j int) bool { return d.Short[i].Region < d.Short[j].Region })
	sort.Slice(d.Always, func(i, j int) bool {
		a, b := d.Always[i], d.Always[j]
		return a.Region < b.Region || a.Region == b.Region && a.Number < b.Number
	})
	sum, err := json.Marshal(struct {
		Regions []Region
		Descs   []Desc
		Short   []Short
		Always  []AlwaysRow
	}{d.Regions, d.Descs, d.Short, d.Always})
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(sum)
	d.Version = hex.EncodeToString(h[:])
	return d, nil
}

func lengths(l []int32) []int32 {
	if l == nil {
		return []int32{}
	}
	return l
}

// PGPattern rewrites one of libphonenumber's regular expressions for
// PostgreSQL. The metadata uses only "(?:", "\d", character classes,
// alternation and counted repeats, all of which PostgreSQL's advanced
// regular expressions share with Java's and Go's; "\d" is spelled out as
// 0-9 so it can never match another script's digits. Data.Build fails the
// tests (not the server) if a new construct appears: see data_test.go.
func PGPattern(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p) && p[i+1] == 'd':
			if inClass {
				b.WriteString("0-9")
			} else {
				b.WriteString("[0-9]")
			}
			i++
		case c == '\\' && i+1 < len(p):
			b.WriteByte(c)
			b.WriteByte(p[i+1])
			i++
		case c == '[':
			inClass = true
			b.WriteByte(c)
		case c == ']':
			inClass = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
