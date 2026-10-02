// A recorded greeting as Linx takes it (docs/ui/SCREENS_PHASE1F.md §12.4):
// 16-bit PCM WAV, one channel, 8000 samples a second. The browser does
// the resampling (OfflineAudioContext), so the server never decodes a
// compressed format, and the upload is small (16 KB a second).

export const GREETING_RATE = 8000;
export const GREETING_MAX_SECONDS = 30;

/** samples (−1…1) as a 16-bit mono WAV at rate. */
export function encodeWav(samples: Float32Array, rate = GREETING_RATE): Uint8Array {
  const out = new Uint8Array(44 + samples.length * 2);
  const v = new DataView(out.buffer);
  const ascii = (at: number, s: string) => { for (let i = 0; i < s.length; i++) out[at + i] = s.charCodeAt(i); };
  ascii(0, "RIFF");
  v.setUint32(4, 36 + samples.length * 2, true);
  ascii(8, "WAVEfmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  ascii(36, "data");
  v.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i] ?? 0));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return out;
}

/** A recording (whatever the browser's MediaRecorder made) as Linx's greeting WAV, at most 30 seconds. */
export async function greetingWav(recording: Blob): Promise<{ wav: Uint8Array; seconds: number }> {
  const ctx = new AudioContext();
  try {
    const decoded = await ctx.decodeAudioData(await recording.arrayBuffer());
    const seconds = Math.min(decoded.duration, GREETING_MAX_SECONDS);
    const offline = new OfflineAudioContext(1, Math.max(1, Math.ceil(seconds * GREETING_RATE)), GREETING_RATE);
    const src = offline.createBufferSource();
    src.buffer = decoded;
    src.connect(offline.destination);
    src.start();
    const rendered = await offline.startRendering();
    return { wav: encodeWav(rendered.getChannelData(0)), seconds };
  } finally {
    void ctx.close();
  }
}
