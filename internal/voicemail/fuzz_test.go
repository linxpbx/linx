package voicemail

import (
	"encoding/binary"
	"testing"
)

// Two parsers here read bytes Linx doesn't control: the note the dialplan
// writes beside a recording, whose caller number and name come from a
// trunk's caller ID (attacker-influenced), and the greeting WAV a browser
// uploads. Neither may panic or hang, whatever it is fed.
//
//	go test ./internal/voicemail/ -run x -fuzz FuzzGreetingFromWAV -fuzztime 1m

func FuzzParseNote(f *testing.F) {
	for _, s := range []string{
		"v1|01a10c69-f69a-7e7b-81b9-e518ac076716||0501234567|Sara Haddad|1791200000",
		"v1|01a10c69-f69a-7e7b-81b9-e518ac076716|101||+9715|1791200000|2550",
		"v1|notauuid||x|y|z",
		"v2|...", "", "v1", "v1|||||", "v1||||||||||",
		"v1|01a10c69-f69a-7e7b-81b9-e518ac076716||0|x|99999999999999999999|abc",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		n, err := parseNote(b) // must not panic
		if err != nil {
			return
		}
		// A note parseNote accepts has only the plain fields its regexes
		// allow: nothing in the number, name or extension that a later
		// screen or query would have to escape.
		if !numberPattern.MatchString(n.number) {
			t.Fatalf("accepted number %q", n.number)
		}
		if !namePattern.MatchString(n.name) {
			t.Fatalf("accepted name %q", n.name)
		}
		if !extPattern.MatchString(n.callerExtension) {
			t.Fatalf("accepted extension %q", n.callerExtension)
		}
		if n.busyTone < 0 || n.busyTone > maxBusyTone {
			t.Fatalf("accepted busy tone %v", n.busyTone)
		}
	})
}

func FuzzGreetingFromWAV(f *testing.F) {
	// A real 16 kHz mono 16-bit WAV of half a second, the smallest the
	// greeting rules accept, as the honest seed.
	f.Add(wav(GreetingRate/2, GreetingRate, 1, 16))
	// The shapes a broken or hostile upload takes.
	f.Add([]byte{})
	f.Add([]byte("RIFF"))
	f.Add([]byte("RIFF\xff\xff\xff\xffWAVE"))
	f.Add(wav(10, 8000, 2, 16))        // wrong rate, stereo
	f.Add(wav(10, GreetingRate, 1, 8)) // wrong depth
	f.Fuzz(func(t *testing.T, b []byte) {
		audio, err := GreetingFromWAV(b) // must not panic
		if err != nil {
			return
		}
		// Accepted audio is within the length bounds the dialplan and the
		// store both rely on (half a second to just over 30 s of slin16).
		if len(audio) < minGreetingBytes || len(audio) > maxGreetingBytes {
			t.Fatalf("accepted %d bytes of audio, outside [%d, %d]", len(audio), minGreetingBytes, maxGreetingBytes)
		}
		if len(audio)%2 != 0 {
			t.Fatalf("accepted an odd number of bytes (%d): not whole 16-bit samples", len(audio))
		}
	})
}

// wav builds a minimal PCM WAV for the seed corpus.
func wav(samples, rate, channels, bits int) []byte {
	data := make([]byte, samples*channels*bits/8)
	var b []byte
	b = append(b, "RIFF"...)
	b = le32(b, uint32(36+len(data)))
	b = append(b, "WAVE"...)
	b = append(b, "fmt "...)
	b = le32(b, 16)
	b = le16(b, 1) // PCM
	b = le16(b, uint16(channels))
	b = le32(b, uint32(rate))
	b = le32(b, uint32(rate*channels*bits/8))
	b = le16(b, uint16(channels*bits/8))
	b = le16(b, uint16(bits))
	b = append(b, "data"...)
	b = le32(b, uint32(len(data)))
	return append(b, data...)
}

func le16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
