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
}

export const CALL_LINK_PATH = "/api/v1/call-link";
export const CALL_LINK_PROTOCOL = "linx.call.v1";

type Signal = { description?: RTCSessionDescriptionInit; candidate?: RTCIceCandidateInit | null };

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
}

/** Most a picture may use, bits a second: ADR-081's 720p step. */
const MAX_BITRATE = 1_500_000;
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

  constructor(private readonly opts: PictureOptions) {
    this.wantCamera = opts.cameraFirst;
  }

  start() {
    const url = `wss://${window.location.host}${CALL_LINK_PATH}?call=${encodeURIComponent(this.opts.callId)}`;
    const ws = new WebSocket(url, CALL_LINK_PROTOCOL);
    this.ws = ws;
    ws.onmessage = (e) => void this.onMessage(String(e.data));
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      if (this.state.available === null) this.set({ available: false });
    };
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
        t.direction = "sendrecv";
      } else {
        this.pc.addTransceiver(track!, { direction: "sendrecv", streams: [media] });
      }
      this.capBitrate();
      this.set({ mine: true });
    } catch {
      this.set({ problem: "Linx couldn't start your camera. Check that this page may use it." });
    } finally {
      this.set({ starting: false });
    }
  }

  stop() {
    this.stopped = true;
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
      if (s.track?.kind !== "video") continue;
      const p = s.getParameters();
      if (!p.encodings?.length) continue;
      // Kept small when the other side's picture goes through the relay
      // over TCP or TLS (it says so): a lost packet holds up everything
      // behind it there, and the app keeps its own at 360p for the same
      // reason (DirectPicture.overTCPStep).
      p.encodings[0]!.maxBitrate = this.theirsOverTCP ? OVER_TCP_BITRATE : MAX_BITRATE;
      if (this.theirsOverTCP) p.encodings[0]!.scaleResolutionDownBy = 1.5;
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
    let msg: { type?: string; video?: boolean; polite?: boolean; session?: number; data?: Signal & { session?: number }; room?: string; token?: string; url?: string };
    try {
      msg = JSON.parse(raw);
    } catch {
      return;
    }
    switch (msg.type) {
      case "peer":
        if (!msg.video) {
          this.set({ available: false });
          return;
        }
        this.polite = !!msg.polite;
        this.session = msg.session;
        this.connect();
        this.set({ available: true });
        if (this.wantCamera) await this.setCamera(true);
        return;
      case "peer-gone":
        // The other side's link went (it reconnects, or the call is over);
        // a new "peer" starts afresh.
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
  }

  private async onSignal({ description, candidate, slowRoute }: Signal & { slowRoute?: boolean }) {
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
        await pc.setRemoteDescription(description);
        if (description.type === "offer") {
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
