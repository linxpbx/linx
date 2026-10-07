package asteriskconf

import (
	"strconv"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/numbering"
)

// Every busy tone is a country Linx can be set up in, and one Asterisk's
// detector can hear. Countries without one aren't listened for.
func TestBusyTonesAreSane(t *testing.T) {
	for c, tone := range BusyTones {
		if !numbering.Supported(c) {
			t.Errorf("busy tone for %s, a country Linx isn't set up in", c)
		}
		if tone.Hz < 300 || tone.Hz > 3400 || tone.OnMs < 100 || tone.OffMs < 100 {
			t.Errorf("%s: busy tone %+v", c, tone)
		}
	}
	// North America's busy signal is two tones at once: not listened for.
	if _, ok := BusyTones["US"]; ok {
		t.Error("US has a single-frequency busy tone")
	}
	if len(BusyTones) < 30 {
		t.Errorf("only %d busy tones", len(BusyTones))
	}
}

func TestBusyToneDialplan(t *testing.T) {
	ae := BusyTones["AE"]
	// The 4th burst is heard 300 ms into it: 3 whole on/off cycles and
	// those 300 ms are tone.
	if ae.MinMs() != 300 || ae.TrimMs() != 2550 {
		t.Errorf("AE: min %d, trim %d", ae.MinMs(), ae.TrimMs())
	}
	conf := extensionsFile()
	if !strings.Contains(conf, "\n[globals]\nBUSYTONE_AE=400/300/2550\n") {
		t.Errorf("no AE busy tone in the globals:\n%s", conf[:strings.Index(conf, "[linx-extensions]")])
	}
	// The dialplan's n() is BusyToneBursts, wherever it listens.
	n := strings.Count(conf, "rn("+strconv.Itoa(BusyToneBursts)+")eg(")
	if n != 2 || strings.Count(conf, "TONE_DETECT(${") != 2 {
		t.Errorf("%d detectors with n(%d), %d in all", n, BusyToneBursts, strings.Count(conf, "TONE_DETECT(${"))
	}
	// Incoming calls listen from the start, outgoing ones once answered,
	// and only the line's side (r) is listened to.
	for _, want := range []string{
		" same => n,Set(GROUP(linx-trunk)=${CHANNEL(endpoint)})\n same => n,Gosub(linx-busy-tone,s,1)\n",
		"U(linx-busy-tone^s^1))\n",
		"eg(linx-far-end-gone,s,1))=)",
		"eg(linx-voicemail,line-busy,1))=)",
		"|${VMSTART}|${FILTER(0-9,${VMTRIM})})\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("the dialplan has no %q", want)
		}
	}
}
