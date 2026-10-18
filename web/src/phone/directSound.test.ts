import { DirectSound, TO_RETURN, soundPackets } from "./directSound";

function feed(s: DirectSound, from: number, readings: number, step = 25) {
  return Array.from({ length: readings }, (_, i) => s.reading(from + (i + 1) * step));
}

test("stays on the phone system until the other side says its microphone is on the direct link", () => {
  const s = new DirectSound();
  expect(feed(s, 0, 6).every((r) => r === null)).toBe(true);
  s.heard("sending");
  expect(s.reading(175)).toBeNull();
  expect(s.reading(200)).toBe("direct");
});

test("an older side that says nothing is never played from the direct link", () => {
  const s = new DirectSound();
  s.heard("can");
  expect(feed(s, 0, 20).every((r) => r === null)).toBe(true);
  expect(s.playing).toBe("linx");
});

test("falls back after a second and a half with nothing, not after a quiet moment", () => {
  const s = new DirectSound();
  s.heard("sending");
  feed(s, 0, 3);
  expect(s.playing).toBe("direct");
  expect(s.reading(75)).toBeNull();
  expect(s.reading(75)).toBeNull();
  expect(s.reading(76)).toBeNull();
  expect(s.reading(76)).toBeNull();
  expect(s.reading(76)).toBeNull();
  expect(s.reading(76)).toBe("linx");
  const back = feed(s, 76, TO_RETURN);
  expect(back.slice(0, -1).every((r) => r === null)).toBe(true);
  expect(back.at(-1)).toBe("direct");
});

test("a link that goes puts both routes back on the phone system", () => {
  const s = new DirectSound();
  expect(s.linkGone()).toBe(false);
  s.heard("sending");
  s.heard("direct");
  expect(s.theyPlay).toBe("direct");
  expect(s.linkGone()).toBe(true);
  expect(s.playing).toBe("linx");
  expect(s.theyPlay).toBe("linx");
  expect(s.theySend).toBe(false);
});

test("counts only the sound that arrives", () => {
  const report = new Map<string, object>([
    ["a", { type: "inbound-rtp", kind: "audio", packetsReceived: 40 }],
    ["v", { type: "inbound-rtp", kind: "video", packetsReceived: 900 }],
    ["o", { type: "outbound-rtp", kind: "audio", packetsSent: 50 }],
  ]) as unknown as RTCStatsReport;
  expect(soundPackets(report)).toBe(40);
});
