// SDP tweaks for the low-bandwidth rules (docs/WEB.md §6, CLAUDE.md
// "Low-resource and low-bandwidth rule"): Opus with in-band FEC (recovers
// lost packets), DTX (sends almost nothing during silence), mono, and at
// most OPUS_MAX_BITRATE (full-quality voice; browsers otherwise send about
// 32–40 kbps). Browsers still go lower by themselves on a poor link.

/** Opus's most, in bits per second: full-quality mono voice. */
export const OPUS_MAX_BITRATE = 24000;
export function preferOpusFecDtx(sdp: string): string {
  const pt = /^a=rtpmap:(\d+) opus\/48000\/2\r?$/im.exec(sdp)?.[1];
  if (!pt) return sdp;
  const eol = sdp.includes("\r\n") ? "\r\n" : "\n";
  const lines = sdp.split(eol);
  const fmtp = lines.findIndex((l) => l.startsWith(`a=fmtp:${pt} `));
  if (fmtp === -1) {
    const rtpmap = lines.findIndex((l) => l.startsWith(`a=rtpmap:${pt} `));
    lines.splice(rtpmap + 1, 0, `a=fmtp:${pt} useinbandfec=1;usedtx=1;stereo=0;sprop-stereo=0;maxaveragebitrate=${OPUS_MAX_BITRATE}`);
    return lines.join(eol);
  }
  const line = lines[fmtp] ?? "";
  const params = line.slice(`a=fmtp:${pt} `.length).split(";").filter(Boolean);
  const set = (k: string, v: string) => {
    const i = params.findIndex((p) => p.trim().startsWith(k + "="));
    if (i === -1) params.push(`${k}=${v}`);
    else params[i] = `${k}=${v}`;
  };
  set("useinbandfec", "1");
  set("usedtx", "1");
  set("stereo", "0");
  set("sprop-stereo", "0");
  set("maxaveragebitrate", String(OPUS_MAX_BITRATE));
  lines[fmtp] = `a=fmtp:${pt} ${params.join(";")}`;
  return lines.join(eol);
}

/** Caps what this browser sends, whatever the other side's SDP allows. */
export async function capAudioSend(pc: RTCPeerConnection): Promise<void> {
  for (const sender of pc.getSenders()) {
    if (sender.track?.kind !== "audio") continue;
    const params = sender.getParameters();
    if (!params.encodings?.length) continue;
    params.encodings = params.encodings.map((e) => ({ ...e, maxBitrate: OPUS_MAX_BITRATE }));
    await sender.setParameters(params).catch(() => undefined);
  }
}
