import { describe, expect, it } from "vitest";
import { encodeWav, GREETING_RATE } from "./wav";

describe("encodeWav", () => {
  it("writes the WAV Linx takes for a greeting: 16-bit, mono, 8 kHz", () => {
    const wav = encodeWav(new Float32Array([0, 1, -1, 2, 0.5]));
    const v = new DataView(wav.buffer);
    const ascii = (at: number, n: number) => String.fromCharCode(...wav.slice(at, at + n));
    expect(ascii(0, 4)).toBe("RIFF");
    expect(ascii(8, 8)).toBe("WAVEfmt ");
    expect(v.getUint16(20, true)).toBe(1);
    expect(v.getUint16(22, true)).toBe(1);
    expect(v.getUint32(24, true)).toBe(GREETING_RATE);
    expect(v.getUint16(34, true)).toBe(16);
    expect(ascii(36, 4)).toBe("data");
    expect(v.getUint32(40, true)).toBe(10);
    expect(wav.length).toBe(54);
    // Loud samples are clipped, not wrapped.
    expect([0, 1, 2, 3, 4].map((i) => v.getInt16(44 + 2 * i, true))).toEqual([0, 32767, -32768, 32767, 16383]);
  });
});
