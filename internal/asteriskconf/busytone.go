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

// BusyTones are the busy tones of the countries Linx can be set up in
// (numbering.Countries: a test checks every one has its tone). Only a
// single-frequency tone can be listened for: Asterisk's detector wants
// the tone well above everything else in the sound, which a two-tone busy
// signal (North America's 480+620 Hz) never is.
var BusyTones = map[string]BusyTone{
	// The UK's tone, which the UAE uses (measured on the owner's landline,
	// docs/DEMO_PHASE1F.md Part B step 9): 400 Hz, 0.375 s on and off.
	"AE": {Hz: 400, OnMs: 375, OffMs: 375},
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
