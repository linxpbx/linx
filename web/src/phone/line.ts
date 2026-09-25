// The browser's phone line (docs/WEB.md §5): a SIP account over this page's
// own /sip websocket, which the control plane relays to Asterisk, with
// audio relayed through Linx's TURN server when there's no direct route.
// One call at a time in this slice (no hold or call waiting).
import * as JsSIP from "jssip";
import type { RTCSession } from "jssip/lib/RTCSession";
import type { RTCSessionEvent, UA } from "jssip/lib/UA";
import type { DTMF_TRANSPORT } from "jssip/lib/Constants";
import { api, type TurnCredentials, type WebPhone } from "@/api/client";
import { preferOpusFecDtx } from "./sdp";
import { Ringtone } from "./ringtone";
import { loadAudioSettings, type AudioSettings } from "./settings";

export type LineStatus = "starting" | "ready" | "reconnecting" | "unavailable";

export interface Peer {
  name: string;
  number: string;
}

export interface Connection {
  mode: "direct" | "relayed";
  rttMs?: number;
  /** For a relayed call: how this browser reaches the relay (udp, tcp, tls). */
  relayProtocol?: string;
  /** Audio received so far, in bytes (for "can I hear them?" checks). */
  audioBytesIn: number;
}

export interface Call {
  id: string;
  direction: "incoming" | "outgoing";
  peer: Peer;
  phase: "ringing" | "calling" | "active" | "reconnecting";
  answeredAt?: number;
  muted: boolean;
  connection?: Connection;
}

export interface RecentCall {
  id: string;
  peer: Peer;
  kind: "outgoing" | "incoming" | "missed";
  at: number;
}

export interface LineState {
  status: LineStatus;
  /** Why the line is unavailable, in plain words. */
  problem?: string;
  extension?: string;
  call: Call | null;
  recent: RecentCall[];
}

/** Calls to *43 hear themselves back (the echo test, docs/PBX.md). */
export const ECHO_TEST = "*43";

const ICE_GATHER_LIMIT_MS = 3000;
const STATS_EVERY_MS = 2000;
const TURN_REFRESH_BEFORE_MS = 5 * 60 * 1000;

export class PhoneLine {
  private state: LineState = { status: "starting", call: null, recent: [] };
  private listeners = new Set<() => void>();
  private ua: UA | null = null;
  private session: RTCSession | null = null;
  private domain = "";
  private turn: TurnCredentials | null = null;
  private turnTimer: number | undefined;
  private statsTimer: number | undefined;
  private reconnectTimer: number | undefined;
  private audio: HTMLAudioElement;
  private ringtone: Ringtone;
  private settings: AudioSettings = loadAudioSettings();
  private stopped = false;
  private directory: (number: string) => string | undefined = () => undefined;
  private retriedCredentials = false;

  constructor() {
    this.audio = document.createElement("audio");
    this.audio.autoplay = true;
    this.ringtone = new Ringtone(() => this.settings.ringVolume);
    this.onOnline = this.onOnline.bind(this);
    this.onUnload = this.onUnload.bind(this);
  }

  // --- React store interface (useSyncExternalStore) ---
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  getState = () => this.state;
  private set(patch: Partial<LineState>) {
    this.state = { ...this.state, ...patch };
    for (const fn of this.listeners) fn();
  }
  private setCall(patch: Partial<Call> | null) {
    if (patch === null || !this.state.call) {
      this.set({ call: patch === null ? null : (patch as Call) });
      return;
    }
    this.set({ call: { ...this.state.call, ...patch } });
  }

  /** Gets this session's line from the API and signs it in. */
  async start(): Promise<void> {
    this.stopped = false;
    window.addEventListener("online", this.onOnline);
    window.addEventListener("pagehide", this.onUnload);
    const { data, error, response } = await api.POST("/api/v1/me/web-phone");
    if (!data) {
      const code = error && "code" in error ? error.code : "";
      const problem =
        code === "no_extension"
          ? "Your account has no extension yet, so you can't make or take calls. Ask your admin to give you one."
          : code === "extension_unavailable"
            ? "Your extension is turned off. Ask your admin to turn it back on."
            : response.status === 503
              ? "Calls from the browser aren't set up on this server yet."
              : "Linx couldn't set up your phone line. Reload the page to try again.";
      this.set({ status: "unavailable", problem });
      return;
    }
    this.connect(data);
  }

  private connect(wp: WebPhone) {
    this.ua?.stop();
    this.domain = wp.sip_uri.split("@")[1] ?? "";
    this.useTurn(wp.turn);
    const socket = new JsSIP.WebSocketInterface(`wss://${window.location.host}${wp.websocket_path}`);
    const ua = new JsSIP.UA({
      sockets: [socket],
      uri: wp.sip_uri,
      // The line's own username, not JsSIP's random one: Asterisk dials
      // this contact, so requests this browser sends in calls it answered
      // are From it too, and the /sip relay accepts only this line's
      // username (docs/WEB.md §5).
      contact_uri: `sip:${wp.sip_username}@${randomHost()}.invalid;transport=ws`,
      password: wp.password,
      display_name: wp.display_name,
      register: true,
      register_expires: 300,
      session_timers: false,
      user_agent: "Linx Web",
      connection_recovery_min_interval: 2,
      connection_recovery_max_interval: 30,
    });
    this.ua = ua;
    this.set({ extension: wp.extension });
    ua.on("registered", () => {
      this.retriedCredentials = false;
      this.set({ status: "ready", problem: undefined });
    });
    ua.on("disconnected", () => {
      if (!this.stopped) this.set({ status: "reconnecting" });
    });
    ua.on("registrationFailed", () => {
      // Usually the line's password changed (this page opened in another
      // tab, which asked for the line again): ask for it once more.
      if (!this.retriedCredentials) {
        this.retriedCredentials = true;
        void this.refreshLine();
        return;
      }
      this.set({ status: "unavailable", problem: "Your phone line couldn't sign in. Reload the page to try again." });
    });
    ua.on("newRTCSession", (e: RTCSessionEvent) => {
      if (e.originator === "remote") this.incoming(e.session);
    });
    ua.start();
  }

  private async refreshLine() {
    const { data } = await api.POST("/api/v1/me/web-phone");
    if (data && !this.stopped) this.connect(data);
  }

  private useTurn(t: TurnCredentials) {
    this.turn = t;
    window.clearTimeout(this.turnTimer);
    const wait = Math.max(30_000, new Date(t.expires_at).getTime() - Date.now() - TURN_REFRESH_BEFORE_MS);
    this.turnTimer = window.setTimeout(async () => {
      const { data } = await api.GET("/api/v1/me/turn-credentials");
      if (data && !this.stopped) {
        this.useTurn(data);
        this.session?.connection?.setConfiguration(this.pcConfig());
      }
    }, wait);
  }

  private pcConfig(): RTCConfiguration {
    const t = this.turn;
    return {
      // The browser tries a direct route first, then the relay over UDP,
      // then over TLS on 443 (docs/WEB.md §6).
      iceServers: t && t.urls.length > 0 ? [{ urls: t.urls, username: t.username, credential: t.credential }] : [],
      // Not "max-bundle": Asterisk's offers have no BUNDLE group, which
      // max-bundle refuses. Calls are one audio stream, so nothing is lost.
      rtcpMuxPolicy: "require",
    };
  }

  private mediaConstraints(): MediaStreamConstraints {
    const id = this.settings.microphoneId;
    return { audio: id ? { deviceId: id } : true, video: false };
  }

  /** Names callers the way the Team list does (the person on the extension). */
  setDirectory(lookup: (number: string) => string | undefined) {
    this.directory = lookup;
  }

  /** Settings changed on the Settings screen. */
  applySettings(s: AudioSettings) {
    this.settings = s;
    void this.setSpeaker();
  }

  private async setSpeaker() {
    const a = this.audio as HTMLAudioElement & { setSinkId?: (id: string) => Promise<void> };
    if (a.setSinkId) {
      try {
        await a.setSinkId(this.settings.speakerId);
      } catch {
        // The chosen speaker is gone; the default plays instead.
      }
    }
  }

  // --- Calls ---

  call(number: string, name?: string) {
    const target = number.trim();
    if (!this.ua || this.state.status !== "ready" || this.session || !target) return;
    const peer = { name: name ?? (target === ECHO_TEST ? "Test sound" : target), number: target };
    let session: RTCSession;
    try {
      session = this.ua.call(`sip:${target}@${this.domain}`, {
        mediaConstraints: this.mediaConstraints(),
        pcConfig: this.pcConfig(),
      });
    } catch {
      this.set({ problem: "That call couldn't start. Reload the page and try again." });
      return;
    }
    this.track(session, "outgoing", peer);
    this.setCall({ id: session.id, direction: "outgoing", peer, phase: "calling", muted: false });
  }

  private incoming(session: RTCSession) {
    if (this.session) {
      session.terminate({ status_code: 486, reason_phrase: "Busy Here" });
      return;
    }
    const id = session.remote_identity;
    const peer = { name: this.directory(id.uri.user) || id.display_name || id.uri.user, number: id.uri.user };
    this.track(session, "incoming", peer);
    this.setCall({ id: session.id, direction: "incoming", peer, phase: "ringing", muted: false });
    this.ringtone.start();
  }

  answer() {
    const s = this.session;
    if (!s || this.state.call?.phase !== "ringing") return;
    this.ringtone.stop();
    s.answer({ mediaConstraints: this.mediaConstraints(), pcConfig: this.pcConfig() });
  }

  decline() {
    this.ringtone.stop();
    this.session?.terminate({ status_code: 486, reason_phrase: "Busy Here" });
  }

  hangUp() {
    this.ringtone.stop();
    this.session?.terminate();
  }

  toggleMute() {
    const s = this.session;
    if (!s || this.state.call?.phase === "calling" || this.state.call?.phase === "ringing") return;
    if (s.isMuted().audio) s.unmute({ audio: true });
    else s.mute({ audio: true });
    this.setCall({ muted: s.isMuted().audio ?? false });
  }

  sendTone(tone: string) {
    if (this.state.call?.phase !== "active" && this.state.call?.phase !== "reconnecting") return;
    this.session?.sendDTMF(tone, { transportType: "RFC2833" as DTMF_TRANSPORT });
  }

  private track(session: RTCSession, direction: Call["direction"], peer: Peer) {
    this.session = session;
    let answered = false;
    let gathered = false;

    session.on("sdp", (e) => {
      if (e.originator === "local") e.sdp = preferOpusFecDtx(e.sdp);
    });
    // Asterisk doesn't take candidates one at a time, so JsSIP waits for
    // them all; a relay that can't be reached (UDP blocked) would hold the
    // call for a long time. Send once we have a relay candidate, or after
    // ICE_GATHER_LIMIT_MS whatever we have.
    let limit: number | undefined;
    session.on("icecandidate", (e) => {
      const done = () => {
        if (gathered) return;
        gathered = true;
        window.clearTimeout(limit);
        e.ready();
      };
      if (limit === undefined) limit = window.setTimeout(done, ICE_GATHER_LIMIT_MS);
      if (e.candidate.type === "relay") window.setTimeout(done, 300);
    });
    const attach = (pc: RTCPeerConnection) => {
      pc.addEventListener("track", (ev) => {
        const [stream] = ev.streams;
        this.audio.srcObject = stream ?? new MediaStream([ev.track]);
        void this.setSpeaker();
        void this.audio.play().catch(() => undefined);
      });
      pc.addEventListener("iceconnectionstatechange", () => this.iceChanged(pc));
    };
    session.on("peerconnection", (e) => attach(e.peerconnection));
    if (session.connection) attach(session.connection);

    session.on("accepted", () => {
      answered = true;
      this.ringtone.stop();
      this.setCall({ phase: "active", answeredAt: Date.now() });
      this.startStats();
    });
    const finish = (missed: boolean) => {
      this.ringtone.stop();
      window.clearInterval(this.statsTimer);
      window.clearTimeout(this.reconnectTimer);
      if (this.session === session) this.session = null;
      this.audio.srcObject = null;
      const kind: RecentCall["kind"] = direction === "outgoing" ? "outgoing" : missed ? "missed" : "incoming";
      if (peer.number !== ECHO_TEST) {
        this.set({ recent: [{ id: session.id, peer, kind, at: Date.now() }, ...this.state.recent].slice(0, 20) });
      }
      this.setCall(null);
    };
    session.on("ended", () => finish(false));
    session.on("failed", () => finish(direction === "incoming" && !answered));
  }

  // --- Connection quality and recovery ---

  private startStats() {
    window.clearInterval(this.statsTimer);
    const tick = async () => {
      const pc = this.session?.connection;
      if (!pc) return;
      const connection = await readConnection(pc);
      if (connection && this.state.call) this.setCall({ connection });
    };
    void tick();
    this.statsTimer = window.setInterval(tick, STATS_EVERY_MS);
  }

  private iceChanged(pc: RTCPeerConnection) {
    const call = this.state.call;
    if (!call || call.phase === "ringing" || call.phase === "calling") return;
    switch (pc.iceConnectionState) {
      case "connected":
      case "completed":
        window.clearTimeout(this.reconnectTimer);
        this.reconnectTimer = undefined;
        if (call.phase === "reconnecting") this.setCall({ phase: "active" });
        break;
      case "disconnected":
        // Often recovers by itself within a few seconds; restart if not.
        this.setCall({ phase: "reconnecting" });
        window.clearTimeout(this.reconnectTimer);
        this.reconnectTimer = window.setTimeout(() => this.restartIce(), 3000);
        break;
      case "failed":
        this.setCall({ phase: "reconnecting" });
        this.restartIce();
        break;
    }
  }

  private onOnline() {
    // The network changed (Wi-Fi to mobile data, say): find a new route.
    if (this.state.call?.phase === "active" || this.state.call?.phase === "reconnecting") {
      this.setCall({ phase: "reconnecting" });
      this.restartIce();
    }
  }

  private restartIce() {
    const s = this.session;
    if (!s || !s.isEstablished()) return;
    s.renegotiate({ rtcOfferConstraints: { iceRestart: true } });
  }

  private onUnload() {
    this.stop();
  }

  /** Signs the line out (page closing, or the person signing out). */
  stop() {
    this.stopped = true;
    this.ringtone.stop();
    window.clearTimeout(this.turnTimer);
    window.clearInterval(this.statsTimer);
    window.clearTimeout(this.reconnectTimer);
    window.removeEventListener("online", this.onOnline);
    window.removeEventListener("pagehide", this.onUnload);
    this.session?.terminate();
    this.ua?.stop();
    this.ua = null;
  }
}

function randomHost(): string {
  const b = new Uint8Array(8);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => (x % 36).toString(36)).join("");
}

/** Direct or relayed, the round-trip time, and audio received so far. */
export async function readConnection(pc: RTCPeerConnection): Promise<Connection | null> {
  const stats = await pc.getStats();
  let pairId: string | undefined;
  let audioBytesIn = 0;
  stats.forEach((r) => {
    if (r.type === "transport" && r.selectedCandidatePairId) pairId = r.selectedCandidatePairId;
    if (r.type === "inbound-rtp" && r.kind === "audio") audioBytesIn += r.bytesReceived ?? 0;
  });
  if (!pairId) {
    stats.forEach((r) => {
      if (!pairId && r.type === "candidate-pair" && r.nominated && r.state === "succeeded") pairId = r.id;
    });
  }
  const pair = pairId ? stats.get(pairId) : undefined;
  if (!pair) return null;
  const local = stats.get(pair.localCandidateId);
  const relayed = local?.candidateType === "relay";
  return {
    mode: relayed ? "relayed" : "direct",
    rttMs: typeof pair.currentRoundTripTime === "number" ? Math.round(pair.currentRoundTripTime * 1000) : undefined,
    relayProtocol: relayed ? (local?.relayProtocol ?? undefined) : undefined,
    audioBytesIn,
  };
}
