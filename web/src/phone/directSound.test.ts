import { DirectSound, GONE_FOR, TO_RETURN, soundPackets } from "./directSound";

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

test("a quiet talker is not a broken route: DTX silence keeps the direct link", () => {
  const s = new DirectSound();
  s.heard("sending");
  feed(s, 0, 3);
  expect(s.playing).toBe("direct");
  // Opus sends about one packet every 400 ms in silence, and a browser
  // pauses the microphone for a moment when its audio device changes.
  for (let i = 0; i < GONE_FOR - 1; i += 1) expect(s.reading(75)).toBeNull();
  expect(s.playing).toBe("direct");
  expect(s.reading(76)).toBeNull();
  for (let i = 0; i < GONE_FOR - 1; i += 1) expect(s.reading(76)).toBeNull();
  expect(s.playing).toBe("direct");
});

test("sound that really stops for four seconds goes back, and comes back after a second", () => {
  const s = new DirectSound();
  s.heard("sending");
  feed(s, 0, 3);
  let last: string | null = null;
  for (let i = 0; i < GONE_FOR; i += 1) last = s.reading(75);
  expect(last).toBe("linx");
  const back = feed(s, 75, TO_RETURN);
  expect(back.slice(0, -1).every((r) => r === null)).toBe(true);
  expect(back.at(-1)).toBe("direct");
});

test("a direct route the link itself says is in trouble is left at once", () => {
  const s = new DirectSound();
  s.heard("sending");
  feed(s, 0, 3);
  expect(s.reading(200, false)).toBe("linx");
});

test("the sound stays on the direct link when the phone system's route isn't carrying either", () => {
  const s = new DirectSound();
  s.heard("sending");
  feed(s, 0, 3);
  for (let i = 0; i <= GONE_FOR; i += 1) expect(s.reading(75, false, false)).toBeNull();
  expect(s.playing).toBe("direct");
  expect(s.reading(75, false, true)).toBe("linx");
});

test("the direct link is not played while its own route isn't carrying", () => {
  const s = new DirectSound();
  s.heard("sending");
  expect(Array.from({ length: 6 }, (_, i) => s.reading((i + 1) * 25, false)).every((r) => r === null)).toBe(true);
  expect(s.playing).toBe("linx");
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
