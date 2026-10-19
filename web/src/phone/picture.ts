// A call's picture (ADR-087, docs/VIDEO_CALLS.md §3): between two Linx
// devices it travels on a direct link between them, never through the
// phone system, which carries the sound only. The two find each other on
// the call link (GET /api/v1/call-link), a websocket the server answers
// only for this browser's own call and only with the device it is talking
// to; everything they say to each other passes through it unread.
//
// The link is WebRTC's "perfect negotiation": either side may turn its
// camera on or off at any time, and when both change something at once the
// polite side (the server says which) gives way. Turning the camera on is
// adding a track to a connection already there, so it takes a moment and
// nothing else in the call notices.

/** What the call screen shows. */
export interface PictureState {
  /** null until the server says; false: the other side can't do a picture. */
  available: boolean | null;
  /** This browser's camera is on. */
  mine: boolean;
  /** Their picture is arriving. */
  theirs: boolean;
  /** The camera is starting (the button ignores another press). */
  starting: boolean;
  /** Why the camera couldn't start, in plain words. */
  problem?: string;
  /** **HD**: this browser's picture at 720p, where the link has room. */
  hd?: boolean;
}

import { DirectSound, SOUND_EVERY_MS, SOUND_WORDS, soundPackets, type SoundRoute, type SoundWord } from "./directSound";
import { DIRECT_OPUS_MAX_BITRATE, preferOpusFecDtx } from "./sdp";

export const CALL_LINK_PATH = "/api/v1/call-link";
export const CALL_LINK_PROTOCOL = "linx.call.v1";

type Signal = { description?: RTCSessionDescriptionInit; candidate?: RTCIceCandidateInit | null; sound?: SoundWord };

export interface PictureOptions {
  /** This browser's SIP Call-ID for the call. */
  callId: string;
  iceServers: RTCIceServer[];
  /** The camera to use ("" for the default). */
  cameraId?: string;
  /** Turn the camera on as soon as the other side is there (a video call). */
  cameraFirst: boolean;
  onChange: () => void;
  /** The call became a group call (ADR-088): here is the way in. */
  onMove?: (join: { room: string; token: string; url: string }) => void;
  /** Linx's moment of the answer (ms since the epoch, by this browser's
   * clock): the call's timer counts from it on every device alike. */
  onAnswered?: (at: number) => void;
  /** The call's own microphone track (ADR-092): copied for the direct link,
   * so each route can be quieted apart. Without it there is no direct sound. */
  microphone?: () => MediaStreamTrack | null;
  /** The speaker the direct sound plays on ("" for the default). */
  speakerId?: string;
  /** This browser now plays the other side's sound from this route: the
   * phone system's copy is quieted while it is "direct". */
  onHearing?: (route: SoundRoute) => void;
  /** The other side now plays this browser's sound from this route: what
   * goes through the phone system can be quiet while it is "direct". */
  onTheyHear?: (route: SoundRoute) => void;
}

/** Most a picture may use with **HD** on, bits a second: ADR-081's 720p step. */
const MAX_BITRATE = 1_500_000;
/** Most it may use without HD: the 540p step, which is where the app's own
 * picture sits until its person presses HD (owner, 2026-10-17: the browser
 * had no HD button, and always sent all it could). */
const STANDARD_BITRATE = 900_000;
/** The camera opens at 720p; this brings it down to 540p without HD. */
const STANDARD_SCALE = 720 / 540;
/** Most it may use when the other side's picture goes over TCP: ADR-081's 360p step. */
const OVER_TCP_BITRATE = 350_000;

export class DirectPicture {
  state: PictureState = { available: null, mine: false, theirs: false, starting: false };
  readonly theirStream = new MediaStream();
  myStream: MediaStream | null = null;

  private ws: WebSocket | null = null;
  private pc: RTCPeerConnection | null = null;
  private polite = false;
  /** The other side's picture goes through the relay over TCP or TLS. */
  private theirsOverTCP = false;
  /** This pairing of the two ends, from the server's "peer". */
  private session?: number;
  private makingOffer = false;
  private ignoreOffer = false;
  private stopped = false;
  private camera: MediaStreamTrack | null = null;
  private wantCamera: boolean;
  /** What this browser sends on the link, sound and picture in one stream,
   * so the other side keeps them in step. */
  private readonly sending = new MediaStream();
  /** This browser's microphone on the link: silent until the other side
   * says it can take it. */
  private mic: MediaStreamTrack | null = null;
  private muted = false;
  private theyCanTakeSound = false;
  /** The other side said it can take sound on the link. Remembered,
   * because the answer to it needs this call's microphone and that can
   * arrive later: the word is said once and never repeated. */
  private theySaidCan = false;
  private readonly sound = new DirectSound();
  private soundTimer?: number;
  private inbox: Promise<void> = Promise.resolve();
  private theirSound: MediaStreamTrack | null = null;
  private player: HTMLAudioElement | null = null;

  constructor(private readonly opts: PictureOptions) {
    this.wantCamera = opts.cameraFirst;
  }

  start() {
    const url = `wss://${window.location.host}${CALL_LINK_PATH}?call=${encodeURIComponent(this.opts.callId)}`;
    const ws = new WebSocket(url, CALL_LINK_PROTOCOL);
    this.ws = ws;
    // One message at a time, in the order they came: handled side by side,
    // an offer could arrive while the answer before it was still being
    // applied, be taken for both sides offering at once and dropped, and
    // the two would wait for each other for ever.
    ws.onmessage = (e) => {
      this.inbox = this.inbox.then(() => this.onMessage(String(e.data))).catch(() => undefined);
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      if (this.state.available === null) this.set({ available: false });
    };
  }

  /** The **HD** button: this browser's picture at 720p, or back to 540p. */
  setHD(on: boolean) {
    if (on === !!this.state.hd) return;
    this.set({ hd: on });
    this.capBitrate();
  }

  /** Turns this browser's camera on or off. */
  async setCamera(on: boolean) {
    this.wantCamera = on;
    if (!this.pc || this.state.starting || on === this.state.mine) return;
    if (!on) {
      this.stopCamera();
      return;
    }
    this.set({ starting: true, problem: undefined });
    try {
      const id = this.opts.cameraId;
      const media = await navigator.mediaDevices.getUserMedia({
        audio: false,
        video: { ...(id ? { deviceId: id } : {}), width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30 } },
      });
      const [track] = media.getVideoTracks();
      if (this.stopped || !this.pc || !this.wantCamera) {
        track?.stop();
        return;
      }
      this.camera = track ?? null;
      this.myStream = media;
      const t = this.pc.getTransceivers().find((x) => x.receiver.track.kind === "video");
      if (t) {
        await t.sender.replaceTrack(track ?? null);
        t.sender.setStreams?.(this.sending);
        t.direction = "sendrecv";
      } else {
        if (!this.polite) this.addMySound();
        this.pc.addTransceiver(track!, { direction: "sendrecv", streams: [this.sending] });
      }
      this.capBitrate();
      this.set({ mine: true });
    } catch {
      this.set({ problem: "Linx couldn't start your camera. Check that this page may use it." });
    } finally {
      this.set({ starting: false });
    }
  }

  /** Sound received on the direct link so far, in bytes. */
  async soundBytesIn(): Promise<number> {
    const pc = this.pc;
    if (!pc) return 0;
    let n = 0;
    (await pc.getStats()).forEach((r: { type?: string; kind?: string; bytesReceived?: number }) => {
      if (r.type === "inbound-rtp" && r.kind === "audio") n += r.bytesReceived ?? 0;
    });
    return n;
  }

  /** Mutes this browser's microphone on the direct link too. */
  setMuted(on: boolean) {
    this.muted = on;
    if (this.mic) this.mic.enabled = this.theyCanTakeSound && !on;
  }

  stop() {
    this.stopped = true;
    this.soundGone();
    this.mic?.stop();
    this.mic = null;
    if (this.player) this.player.srcObject = null;
    this.camera?.stop();
    this.camera = null;
    this.myStream = null;
    this.pc?.close();
    this.pc = null;
    const ws = this.ws;
    this.ws = null;
    ws?.close();
  }

  private stopCamera() {
    const t = this.pc?.getTransceivers().find((x) => x.sender.track === this.camera && this.camera);
    if (t) {
      void t.sender.replaceTrack(null);
      t.direction = "recvonly";
    }
    this.camera?.stop();
    this.camera = null;
    this.myStream = null;
    this.set({ mine: false });
  }

  private capBitrate() {
    for (const s of this.pc?.getSenders() ?? []) {
      if (s.track?.kind === "audio") {
        // The voice before the picture when the link is short of room, and
        // never above the call's Opus ceiling.
        const p = s.getParameters();
        for (const e of p.encodings ?? []) {
          e.maxBitrate = DIRECT_OPUS_MAX_BITRATE;
          (e as RTCRtpEncodingParameters & { networkPriority?: string; priority?: string }).networkPriority = "high";
          (e as RTCRtpEncodingParameters & { priority?: string }).priority = "high";
        }
        void s.setParameters(p).catch(() => undefined);
        continue;
      }
      if (s.track?.kind !== "video") continue;
      const p = s.getParameters();
      if (!p.encodings?.length) continue;
      // Kept small when the other side's picture goes through the relay
      // over TCP or TLS (it says so): a lost packet holds up everything
      // behind it there, and the app keeps its own small for the same
      // reason (DirectPicture.overTCPStep) — unless this person asked for
      // HD, which the app also lets grow to 720p over TCP where there's room.
      const e = p.encodings[0]!;
      if (this.state.hd) {
        e.maxBitrate = MAX_BITRATE;
        e.scaleResolutionDownBy = 1;
      } else if (this.theirsOverTCP) {
        e.maxBitrate = OVER_TCP_BITRATE;
        e.scaleResolutionDownBy = 1.5;
      } else {
        e.maxBitrate = STANDARD_BITRATE;
        e.scaleResolutionDownBy = STANDARD_SCALE;
      }
      void s.setParameters(p).catch(() => undefined);
    }
  }

  private set(patch: Partial<PictureState>) {
    this.state = { ...this.state, ...patch };
    this.opts.onChange();
  }

  private send(data: Signal) {
    // Stamped with this pairing (the server's "peer" session), so the other
    // end drops it if it has been paired again since.
    this.ws?.send(JSON.stringify({ type: "signal", data: this.session ? { ...data, session: this.session } : data }));
  }

  private async onMessage(raw: string) {
    let msg: { type?: string; video?: boolean; answered_ms_ago?: number; polite?: boolean; session?: number; data?: Signal & { session?: number }; room?: string; token?: string; url?: string };
    try {
      msg = JSON.parse(raw);
    } catch {
      return;
    }
    switch (msg.type) {
      case "peer":
        if (typeof msg.answered_ms_ago === "number" && msg.answered_ms_ago >= 0) {
          this.opts.onAnswered?.(Date.now() - msg.answered_ms_ago);
        }
        if (!msg.video) {
          this.set({ available: false });
          return;
        }
        this.polite = !!msg.polite;
        this.session = msg.session;
        this.connect();
        this.set({ available: true });
        if (this.opts.microphone) {
          this.send({ sound: "can" });
          // No offer yet on a voice call: the side that leads makes one,
          // with its microphone in it; the other's goes in its slot. On a
          // video call the microphone goes in the camera's offer instead:
          // offered on its own first, the two rounds crossed the other
          // side's camera offer and a picture never came.
          if (!this.polite && !this.wantCamera) this.addMySound();
        }
        // Beside the link's messages, not in their line: a camera takes a
        // moment to start, and the other side's offer mustn't wait.
        if (this.wantCamera) void this.setCamera(true);
        return;
      case "peer-gone":
        // The other side's link went (it reconnects, or the call is over);
        // a new "peer" starts afresh.
        this.soundGone();
        this.pc?.close();
        this.pc = null;
        this.camera?.stop();
        this.camera = null;
        this.myStream = null;
        for (const t of this.theirStream.getTracks()) this.theirStream.removeTrack(t);
        this.set({ mine: false, theirs: false });
        return;
      case "signal":
        // From an earlier pairing: its connection is gone.
        if (msg.data?.session !== undefined && this.session !== undefined && msg.data.session !== this.session) return;
        if (msg.data) await this.onSignal(msg.data);
        return;
      case "move":
        if (msg.room && msg.token && msg.url) this.opts.onMove?.({ room: msg.room, token: msg.token, url: msg.url });
    }
  }

  private connect() {
    this.soundGone();
    this.pc?.close();
    this.theirsOverTCP = false;
    const pc = new RTCPeerConnection({ iceServers: this.opts.iceServers, bundlePolicy: "max-bundle", rtcpMuxPolicy: "require" });
    this.pc = pc;
    pc.onnegotiationneeded = async () => {
      try {
        this.makingOffer = true;
        await pc.setLocalDescription();
        if (pc.localDescription) this.send({ description: pc.localDescription.toJSON() });
      } catch {
        // A newer change replaced this one; it negotiates itself.
      } finally {
        this.makingOffer = false;
      }
    };
    pc.onicecandidate = ({ candidate }) => this.send({ candidate: candidate ? candidate.toJSON() : null });
    pc.oniceconnectionstatechange = () => {
      if (pc.iceConnectionState === "failed") pc.restartIce();
    };
    pc.ontrack = ({ track }) => {
      if (track.kind === "audio") {
        this.playTheirSound(track);
        return;
      }
      if (track.kind !== "video") return;
      for (const t of this.theirStream.getVideoTracks()) this.theirStream.removeTrack(t);
      this.theirStream.addTrack(track);
      // A track is "muted" while nothing arrives on it: the other side's
      // camera is off, or hasn't started.
      const update = () => this.set({ theirs: !track.muted && track.readyState === "live" });
      track.onmute = update;
      track.onunmute = update;
      track.onended = update;
      update();
    };
    if (this.opts.microphone) {
      this.soundTimer = window.setInterval(() => {
        if (this.pc !== pc) return;
        // A microphone that wasn't there when the other side spoke may be
        // now; nothing else says so, because "can" is said only once.
        this.takeTheirSound();
        void pc.getStats().then((r) => {
          if (this.pc === pc) this.heard(soundPackets(r));
        }, () => undefined);
      }, SOUND_EVERY_MS);
    }
  }

  // The sound (ADR-092, docs/DIRECT_SOUND.md).

  private microphone(): MediaStreamTrack | null {
    if (this.mic) return this.mic;
    const source = this.opts.microphone?.();
    if (!source) return null;
    const mic = source.clone();
    mic.enabled = this.theyCanTakeSound && !this.muted;
    this.mic = mic;
    return mic;
  }

  /** This browser's microphone goes in the offer it makes. */
  private addMySound() {
    const pc = this.pc;
    if (!pc || pc.getTransceivers().some((t) => t.receiver.track.kind === "audio")) return;
    const mic = this.microphone();
    if (!mic) return;
    pc.addTransceiver(mic, { direction: "sendrecv", streams: [this.sending] });
  }

  /** Their offer has a slot for the sound: this browser's microphone goes in it. */
  private async carryMySound(pc: RTCPeerConnection) {
    // A slot of this browser's own, made for an offer it then took back,
    // would be offered again later as a second sound: it is closed.
    for (const orphan of pc.getTransceivers()) {
      if (orphan.receiver.track.kind === "audio" && orphan.mid === null && orphan.sender.track) orphan.stop();
    }
    const t = pc.getTransceivers().find((x) => x.receiver.track.kind === "audio" && x.mid !== null && !x.sender.track);
    const mic = t && this.microphone();
    if (!t || !mic) return;
    await t.sender.replaceTrack(mic);
    t.sender.setStreams?.(this.sending);
    t.direction = "sendrecv";
  }

  /** Their sound on the link, kept off until it is the route played. */
  private playTheirSound(track: MediaStreamTrack) {
    this.theirSound = track;
    track.enabled = this.sound.playing === "direct";
    if (!this.player) this.player = new Audio();
    const player = this.player;
    player.autoplay = true;
    player.srcObject = new MediaStream([track]);
    const id = this.opts.speakerId;
    const sink = player as HTMLAudioElement & { setSinkId?: (id: string) => Promise<void> };
    if (id && sink.setSinkId) void sink.setSinkId(id).catch(() => undefined);
    void player.play().catch(() => undefined);
  }

  private soundSaid(word: SoundWord) {
    const before = this.sound.theyPlay;
    this.sound.heard(word);
    if (word === "can") {
      this.theySaidCan = true;
      this.takeTheirSound();
    } else if ((word === "direct" || word === "linx") && this.sound.theyPlay !== before) {
      this.opts.onTheyHear?.(this.sound.theyPlay);
    }
  }

  /** Answers the other side's "can" with "sending", once this call's
   * microphone is there to send. Before the retry below, a "can" that
   * arrived while the microphone was still missing was dropped and never
   * answered, so that side's sound stayed on the phone system for the
   * whole call (the nginx browser suite, intermittently, 2026-10-19). */
  private takeTheirSound() {
    if (!this.theySaidCan || this.theyCanTakeSound) return;
    const mic = this.microphone();
    if (!mic) return;
    this.theyCanTakeSound = true;
    mic.enabled = !this.muted;
    this.send({ sound: "sending" });
  }

  private heard(packets: number) {
    const route = this.sound.reading(packets);
    if (!route) return;
    if (this.theirSound) this.theirSound.enabled = route === "direct";
    this.opts.onHearing?.(route);
    this.send({ sound: route });
  }

  /** The link is gone or made again: both routes are the phone system's at once. */
  private soundGone() {
    window.clearInterval(this.soundTimer);
    this.soundTimer = undefined;
    this.theyCanTakeSound = false;
    this.theySaidCan = false;
    if (this.mic) this.mic.enabled = false;
    if (this.theirSound) this.theirSound.enabled = false;
    this.theirSound = null;
    if (!this.sound.linkGone()) return;
    this.opts.onHearing?.("linx");
    this.opts.onTheyHear?.("linx");
  }

  private async onSignal({ description, candidate, slowRoute, sound }: Signal & { slowRoute?: boolean }) {
    // A word this version doesn't know (a later one's) is left out.
    if (sound && SOUND_WORDS.includes(sound)) this.soundSaid(sound);
    const pc = this.pc;
    if (!pc) return;
    if (slowRoute && !this.theirsOverTCP) {
      this.theirsOverTCP = true;
      this.capBitrate();
    }
    try {
      if (description) {
        const collision = description.type === "offer" && (this.makingOffer || pc.signalingState !== "stable");
        this.ignoreOffer = !this.polite && collision;
        if (this.ignoreOffer) return;
        // Opus at the direct link's quality (40 kbit/s, full band, FEC,
        // DTX): this browser's encoder takes them from their description.
        await pc.setRemoteDescription(
          description.sdp ? { type: description.type, sdp: preferOpusFecDtx(description.sdp, DIRECT_OPUS_MAX_BITRATE) } : description,
        );
        if (description.type === "offer") {
          await this.carryMySound(pc);
          await pc.setLocalDescription();
          if (pc.localDescription) this.send({ description: pc.localDescription.toJSON() });
        }
        this.capBitrate();
      } else if (candidate !== undefined) {
        try {
          await pc.addIceCandidate(candidate ?? undefined);
        } catch (e) {
          if (!this.ignoreOffer) throw e;
        }
      }
    } catch {
      // Out of step: start the link again rather than leave it half-made.
      pc.restartIce();
    }
  }
}
