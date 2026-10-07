package asteriskconf

import (
	"fmt"
	"slices"
	"strings"
)

// BusyTone is a country's busy tone: one frequency, on and off (docs/PBX.md
// §4, "Listening for the busy tone"). An analog landline behind an office
// gateway never signals the far end hanging up; the exchange plays this
// instead, and the dialplan listens for it (Asterisk's TONE_DETECT).
type BusyTone struct {
	Hz, OnMs, OffMs int
}

// BusyTones are the busy tones Linx listens for, by country. Only a
// single-frequency tone can be listened for: Asterisk's detector wants the
// tone well above everything else in the sound, which a two-tone busy
// signal (North America's 480+620 Hz) never is. A country with no entry
// here simply isn't listened for: the dialplan skips the detector, and a
// gateway's line relies on the gateway signalling the hang-up itself
// (docs/PBX.md §4). A wrong entry is harmless the same way: the tone is
// never heard, so nothing is cut short.
//
// The UAE's was measured on the owner's landline (docs/DEMO_PHASE1F.md
// Part B step 9); the rest are the single-frequency busy tones in
// Asterisk 22.11's own configs/samples/indications.conf.sample (ITU-T
// E.180), with its "uk" as GB.
var BusyTones = map[string]BusyTone{
	"AE": {Hz: 400, OnMs: 375, OffMs: 375}, // United Arab Emirates (the UK's tone)
	"AT": {Hz: 420, OnMs: 400, OffMs: 400}, // Austria
	"AU": {Hz: 425, OnMs: 375, OffMs: 375}, // Australia
	"BE": {Hz: 425, OnMs: 500, OffMs: 500}, // Belgium
	"BG": {Hz: 425, OnMs: 500, OffMs: 500}, // Bulgaria
	"BR": {Hz: 425, OnMs: 250, OffMs: 250}, // Brazil
	"CH": {Hz: 425, OnMs: 500, OffMs: 500}, // Switzerland
	"CL": {Hz: 400, OnMs: 500, OffMs: 500}, // Chile
	"CN": {Hz: 450, OnMs: 350, OffMs: 350}, // China
	"CZ": {Hz: 425, OnMs: 330, OffMs: 330}, // Czech Republic
	"DE": {Hz: 425, OnMs: 480, OffMs: 480}, // Germany
	"DK": {Hz: 425, OnMs: 500, OffMs: 500}, // Denmark
	"EE": {Hz: 425, OnMs: 300, OffMs: 300}, // Estonia
	"ES": {Hz: 425, OnMs: 200, OffMs: 200}, // Spain
	"FI": {Hz: 425, OnMs: 300, OffMs: 300}, // Finland
	"FR": {Hz: 440, OnMs: 500, OffMs: 500}, // France
	"GB": {Hz: 400, OnMs: 375, OffMs: 375}, // United Kingdom
	"GR": {Hz: 425, OnMs: 300, OffMs: 300}, // Greece
	"HU": {Hz: 425, OnMs: 300, OffMs: 300}, // Hungary
	"ID": {Hz: 425, OnMs: 500, OffMs: 500}, // Indonesia
	"IL": {Hz: 414, OnMs: 500, OffMs: 500}, // Israel
	"IN": {Hz: 400, OnMs: 750, OffMs: 750}, // India
	"IT": {Hz: 425, OnMs: 500, OffMs: 500}, // Italy
	"JP": {Hz: 400, OnMs: 500, OffMs: 500}, // Japan
	"LT": {Hz: 425, OnMs: 350, OffMs: 350}, // Lithuania
	"MX": {Hz: 425, OnMs: 250, OffMs: 250}, // Mexico
	"MY": {Hz: 425, OnMs: 500, OffMs: 500}, // Malaysia
	"NL": {Hz: 425, OnMs: 500, OffMs: 500}, // Netherlands
	"NO": {Hz: 425, OnMs: 500, OffMs: 500}, // Norway
	"NZ": {Hz: 400, OnMs: 500, OffMs: 500}, // New Zealand
	"PL": {Hz: 425, OnMs: 500, OffMs: 500}, // Poland
	"PT": {Hz: 425, OnMs: 500, OffMs: 500}, // Portugal
	"RU": {Hz: 425, OnMs: 350, OffMs: 350}, // Russian Federation / ex Soviet Union
	"SE": {Hz: 425, OnMs: 250, OffMs: 250}, // Sweden
	"SG": {Hz: 425, OnMs: 750, OffMs: 750}, // Singapore
	"TH": {Hz: 400, OnMs: 500, OffMs: 500}, // Thailand
	"VE": {Hz: 425, OnMs: 500, OffMs: 500}, // Venezuela / South America
	"ZA": {Hz: 400, OnMs: 500, OffMs: 500}, // South Africa
}

// BusyToneBursts is how many bursts of the busy tone end a call: about 2.5
// seconds of it. One burst could be a sound in a conversation; four
// evenly spaced bursts of one pure frequency are the exchange.
const BusyToneBursts = 4

// MinMs is how long a burst must last to count: 80% of it, so a burst
// clipped a little by the line still counts while a short beep doesn't.
func (b BusyTone) MinMs() int { return b.OnMs * 4 / 5 }

// TrimMs is how much of the end of a recording is busy tone when the
// dialplan hears its last burst: every burst before it, and the part of
// the last one it waited for. Cutting that leaves the message as it was
// when the caller hung up (internal/voicemail).
func (b BusyTone) TrimMs() int { return (BusyToneBursts-1)*(b.OnMs+b.OffMs) + b.MinMs() }

// busyToneGlobals are the dialplan's globals for linx-busy-tone, one per
// country: BUSYTONE_<country>=<Hz>/<ms a burst must last>/<ms to trim>.
func busyToneGlobals() string {
	countries := make([]string, 0, len(BusyTones))
	for c := range BusyTones {
		countries = append(countries, c)
	}
	slices.Sort(countries)
	var b strings.Builder
	for _, c := range countries {
		t := BusyTones[c]
		fmt.Fprintf(&b, "BUSYTONE_%s=%d/%d/%d\n", c, t.Hz, t.MinMs(), t.TrimMs())
	}
	return b.String()
}

// extensionsFile is the dialplan as written: extensionsConf with the busy
// tones in its globals.
func extensionsFile() string {
	return strings.Replace(extensionsConf, "\n[globals]\n", "\n[globals]\n"+busyToneGlobals(), 1)
}
