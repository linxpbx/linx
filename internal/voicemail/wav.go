package voicemail

import "encoding/binary"

// SampleRate is the recordings' rate: 8 kHz G.711 mu-law, one byte a
// sample, as Asterisk records them (8 KB a second, about 0.5 MB a minute).
const SampleRate = 8000

// WAV turns a mu-law recording into a 16-bit PCM WAV file, which every
// browser, phone and mail program plays (mu-law WAV isn't played
// everywhere). Twice the size of the recording; made when asked, never
// stored.
func WAV(ulaw []byte) []byte {
	data := len(ulaw) * 2
	out := make([]byte, 44+data)
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+data))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)           // fmt chunk size
	binary.LittleEndian.PutUint16(out[20:], 1)            // PCM
	binary.LittleEndian.PutUint16(out[22:], 1)            // mono
	binary.LittleEndian.PutUint32(out[24:], SampleRate)   // samples a second
	binary.LittleEndian.PutUint32(out[28:], SampleRate*2) // bytes a second
	binary.LittleEndian.PutUint16(out[32:], 2)            // bytes a sample
	binary.LittleEndian.PutUint16(out[34:], 16)           // bits a sample
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(data))
	for i, b := range ulaw {
		binary.LittleEndian.PutUint16(out[44+2*i:], uint16(ulawToLinear[b]))
	}
	return out
}

// ulawToLinear is G.711's mu-law expansion.
var ulawToLinear = func() (t [256]int16) {
	for i := range t {
		u := ^byte(i)
		mag := (int(u&0x0f)<<3 + 0x84) << ((u & 0x70) >> 4)
		v := int16(mag - 0x84)
		if u&0x80 != 0 {
			v = -v
		}
		t[i] = v
	}
	return
}()
