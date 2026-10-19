import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SOUND_EVERY_MS } from "./directSound";
import { DirectPicture } from "./picture";

/** The call link, as the picture uses it: what was sent, and a way to
 * deliver what Linx says. */
class FakeSocket {
  static last: FakeSocket | null = null;
  sent: string[] = [];
  onmessage?: (e: { data: string }) => void;
  onclose?: () => void;

  constructor() {
    FakeSocket.last = this;
  }
  send(text: string) {
    this.sent.push(text);
  }
  close() {}

  /** The words this browser said about the sound, in order. */
  words(): string[] {
    return this.sent.flatMap((s) => {
      const sound = (JSON.parse(s) as { data?: { sound?: string } }).data?.sound;
      return sound ? [sound] : [];
    });
  }
}

/** Only what DirectPicture.connect() touches. */
class FakePeerConnection {
  onnegotiationneeded?: () => void;
  onicecandidate?: (e: { candidate: RTCIceCandidate | null }) => void;
  oniceconnectionstatechange?: () => void;
  ontrack?: (e: { track: MediaStreamTrack }) => void;
  iceConnectionState = "new";

  getTransceivers() {
    return [];
  }
  addTransceiver() {
    return {};
  }
  async getStats() {
    return new Map();
  }
  close() {}
  restartIce() {}
}

/** Only what DirectPicture holds a stream for: the tracks in it. */
class FakeMediaStream {
  private tracks: MediaStreamTrack[] = [];
  getTracks() {
    return this.tracks;
  }
  getVideoTracks() {
    return this.tracks.filter((t) => t.kind === "video");
  }
  addTrack(t: MediaStreamTrack) {
    this.tracks.push(t);
  }
  removeTrack(t: MediaStreamTrack) {
    this.tracks = this.tracks.filter((x) => x !== t);
  }
}

/** A microphone track, as microphone() uses it: it clones the call's. */
function fakeMic(): MediaStreamTrack {
  const track = { kind: "audio", enabled: false, readyState: "live", stop() {}, clone: () => track };
  return track as unknown as MediaStreamTrack;
}

/** Lets the link's queue (a promise chain) drain. */
async function settle() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

describe("the sound on the direct link", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeSocket.last = null;
    vi.stubGlobal("WebSocket", FakeSocket);
    vi.stubGlobal("RTCPeerConnection", FakePeerConnection);
    vi.stubGlobal("MediaStream", FakeMediaStream);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  /** A picture whose call's microphone appears only when `mic()` says so. */
  function open(mic: () => MediaStreamTrack | null) {
    const picture = new DirectPicture({
      callId: "a-call",
      iceServers: [],
      cameraFirst: false,
      onChange: () => {},
      microphone: mic,
    });
    picture.start();
    const link = FakeSocket.last!;
    // Linx pairs the two ends: polite, so this side waits to be offered to.
    link.onmessage!({ data: JSON.stringify({ type: "peer", video: true, polite: true, session: 1 }) });
    return { picture, link };
  }

  it("answers the other side's \"can\" with \"sending\"", async () => {
    const { link } = open(() => fakeMic());
    await settle();
    link.onmessage!({ data: JSON.stringify({ type: "signal", data: { sound: "can" } }) });
    await settle();
    expect(link.words()).toContain("sending");
  });

  // The nginx browser suite failed here intermittently (2026-10-19): the
  // other side's "can" arrived before this call's microphone did, the answer
  // to it was dropped, and because "can" is said once and never repeated the
  // sound stayed on the phone system for the rest of the call.
  it("answers a \"can\" that came before the microphone, once it arrives", async () => {
    let mic: MediaStreamTrack | null = null;
    const { link } = open(() => mic);
    await settle();
    link.onmessage!({ data: JSON.stringify({ type: "signal", data: { sound: "can" } }) });
    await settle();
    expect(link.words()).not.toContain("sending");

    mic = fakeMic();
    await vi.advanceTimersByTimeAsync(SOUND_EVERY_MS);
    await settle();
    expect(link.words()).toContain("sending");
  });

  it("does not say \"sending\" twice", async () => {
    const { link } = open(() => fakeMic());
    await settle();
    link.onmessage!({ data: JSON.stringify({ type: "signal", data: { sound: "can" } }) });
    await settle();
    await vi.advanceTimersByTimeAsync(SOUND_EVERY_MS * 4);
    await settle();
    expect(link.words().filter((w) => w === "sending")).toHaveLength(1);
  });
});
