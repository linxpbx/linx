// A group call (ADR-088, docs/VIDEO_CALLS.md §4): Add person in a call
// between two Linx devices moves it onto Linx's video server, LiveKit, and
// rings the new person. Everyone's sound and picture go through the video
// server from then on, and each person leaves on their own.
//
// livekit-client is loaded only when a group call starts, so the rest of
// the web app carries none of it. It joins through this page's own server
// (/livekit/, which the control plane passes on) and through Linx's own
// relay, so it needs nothing the phone line doesn't.
import type { LocalParticipant, Participant, RemoteParticipant, Room, Track } from "livekit-client";

/** One person in the group call, as the screen shows them. */
export interface GroupPerson {
  id: string;
  name: string;
  me: boolean;
  /** Their picture is coming through. */
  video: boolean;
  /** Their microphone is on. */
  sound: boolean;
  speaking: boolean;
}

export interface GroupState {
  room: string;
  phase: "joining" | "in" | "failed";
  people: GroupPerson[];
  camera: boolean;
  muted: boolean;
  problem?: string;
}

export interface GroupJoin {
  room: string;
  token: string;
  /** The path on this server where the video server's signalling is. */
  url: string;
}

export interface GroupOptions {
  iceServers: RTCIceServer[];
  microphoneId?: string;
  cameraId?: string;
  speakerId?: string;
  /** Start with the camera on (it was on in the call before). */
  camera: boolean;
  onChange: () => void;
}

export class GroupCall {
  state: GroupState;
  private room: Room | null = null;
  private left = false;
  private audio = new Map<string, HTMLAudioElement>();

  constructor(
    private readonly join: GroupJoin,
    private readonly opts: GroupOptions,
  ) {
    this.state = { room: join.room, phase: "joining", people: [], camera: opts.camera, muted: false };
  }

  async start() {
    try {
      const lk = await import("livekit-client");
      if (this.left) return;
      const room = new lk.Room({
        adaptiveStream: true,
        dynacast: true,
        audioCaptureDefaults: { deviceId: this.opts.microphoneId || undefined, echoCancellation: true, noiseSuppression: true },
        videoCaptureDefaults: { deviceId: this.opts.cameraId || undefined, resolution: lk.VideoPresets.h540.resolution },
        publishDefaults: { simulcast: true, videoCodec: "vp8", dtx: true, red: true },
      });
      this.room = room;
      const update = () => this.refresh();
      for (const e of [
        lk.RoomEvent.ParticipantConnected, lk.RoomEvent.ParticipantDisconnected, lk.RoomEvent.TrackMuted,
        lk.RoomEvent.TrackUnmuted, lk.RoomEvent.LocalTrackPublished, lk.RoomEvent.LocalTrackUnpublished,
        lk.RoomEvent.ActiveSpeakersChanged, lk.RoomEvent.TrackUnsubscribed,
      ]) {
        room.on(e, update);
      }
      room.on(lk.RoomEvent.TrackSubscribed, (track: Track, _pub: unknown, p: RemoteParticipant) => {
        if (track.kind === "audio") this.play(p.identity, track);
        update();
      });
      room.on(lk.RoomEvent.Disconnected, () => {
        if (!this.left) this.set({ phase: "failed", problem: "The group call dropped." });
      });
      const url = `wss://${window.location.host}${this.join.url.replace(/\/$/, "")}`;
      await room.connect(url, this.join.token, { rtcConfig: { iceServers: this.opts.iceServers } });
      if (this.left) {
        await room.disconnect();
        return;
      }
      await room.localParticipant.setMicrophoneEnabled(true);
      if (this.opts.camera) await room.localParticipant.setCameraEnabled(true);
      this.set({ phase: "in" });
      this.refresh();
    } catch {
      this.set({ phase: "failed", problem: "Linx couldn't join the group call." });
    }
  }

  async setCamera(on: boolean) {
    try {
      await this.room?.localParticipant.setCameraEnabled(on);
      this.set({ camera: on, problem: undefined });
    } catch {
      this.set({ problem: "Linx couldn't start your camera. Check that this page may use it." });
    }
    this.refresh();
  }

  async setMuted(muted: boolean) {
    await this.room?.localParticipant.setMicrophoneEnabled(!muted);
    this.set({ muted });
  }

  /** Attaches a person's picture to a video element (or detaches it). */
  attach(id: string, el: HTMLVideoElement | null) {
    const p = this.participant(id);
    const pub = p?.getTrackPublication("camera" as Track.Source);
    const track = pub?.track;
    if (!el || !track) return;
    track.attach(el);
  }

  async leave() {
    this.left = true;
    for (const a of this.audio.values()) a.srcObject = null;
    this.audio.clear();
    await this.room?.disconnect().catch(() => undefined);
    this.room = null;
  }

  private participant(id: string): Participant | undefined {
    const room = this.room;
    if (!room) return undefined;
    if (room.localParticipant.identity === id) return room.localParticipant;
    return room.remoteParticipants.get(id);
  }

  private play(id: string, track: Track) {
    const el = (track.attach() as HTMLAudioElement) ?? document.createElement("audio");
    const sink = el as HTMLAudioElement & { setSinkId?: (id: string) => Promise<void> };
    if (this.opts.speakerId && sink.setSinkId) void sink.setSinkId(this.opts.speakerId).catch(() => undefined);
    this.audio.set(id + ":" + track.sid, el);
  }

  private refresh() {
    const room = this.room;
    if (!room) return;
    const speaking = new Set(room.activeSpeakers.map((p) => p.identity));
    const person = (p: Participant, me: boolean): GroupPerson => ({
      id: p.identity,
      name: p.name || p.identity,
      me,
      video: !!p.getTrackPublication("camera" as Track.Source)?.track && !p.getTrackPublication("camera" as Track.Source)?.isMuted,
      sound: !!p.getTrackPublication("microphone" as Track.Source) && !p.getTrackPublication("microphone" as Track.Source)?.isMuted,
      speaking: speaking.has(p.identity),
    });
    const me = room.localParticipant as LocalParticipant;
    this.set({
      people: [person(me, true), ...Array.from(room.remoteParticipants.values()).map((p) => person(p, false))],
      camera: me.isCameraEnabled,
    });
  }

  private set(patch: Partial<GroupState>) {
    this.state = { ...this.state, ...patch };
    this.opts.onChange();
  }
}
