// SDP tweaks for the low-bandwidth rules (docs/WEB.md §6): Opus with
// in-band FEC (recovers lost packets) and DTX (sends almost nothing during
// silence).
export function preferOpusFecDtx(sdp: string): string {
  const pt = /^a=rtpmap:(\d+) opus\/48000\/2\r?$/im.exec(sdp)?.[1];
  if (!pt) return sdp;
  const eol = sdp.includes("\r\n") ? "\r\n" : "\n";
  const lines = sdp.split(eol);
  const fmtp = lines.findIndex((l) => l.startsWith(`a=fmtp:${pt} `));
  if (fmtp === -1) {
    const rtpmap = lines.findIndex((l) => l.startsWith(`a=rtpmap:${pt} `));
    lines.splice(rtpmap + 1, 0, `a=fmtp:${pt} useinbandfec=1;usedtx=1`);
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
  lines[fmtp] = `a=fmtp:${pt} ${params.join(";")}`;
  return lines.join(eol);
}
