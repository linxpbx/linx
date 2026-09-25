import { preferOpusFecDtx } from "./sdp";

test("adds FEC and DTX to Opus, keeping its other settings", () => {
  const sdp = [
    "v=0",
    "m=audio 9 UDP/TLS/RTP/SAVPF 111 9 0",
    "a=rtpmap:111 opus/48000/2",
    "a=fmtp:111 minptime=10;useinbandfec=0",
    "a=rtpmap:9 G722/8000",
    "",
  ].join("\r\n");
  const out = preferOpusFecDtx(sdp);
  expect(out).toContain("a=fmtp:111 minptime=10;useinbandfec=1;usedtx=1\r\n");
  expect(out).toContain("a=rtpmap:9 G722/8000");
});

test("adds an fmtp line when Opus has none", () => {
  const out = preferOpusFecDtx("m=audio 9 RTP/SAVPF 96\na=rtpmap:96 opus/48000/2\n");
  expect(out).toBe("m=audio 9 RTP/SAVPF 96\na=rtpmap:96 opus/48000/2\na=fmtp:96 useinbandfec=1;usedtx=1\n");
});

test("leaves an offer without Opus alone", () => {
  const sdp = "m=audio 9 RTP/SAVPF 0\r\na=rtpmap:0 PCMU/8000\r\n";
  expect(preferOpusFecDtx(sdp)).toBe(sdp);
});
