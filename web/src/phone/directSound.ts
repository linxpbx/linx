// The sound of a Linx-to-Linx call on the direct link (ADR-092,
// docs/DIRECT_SOUND.md): the same rules as the app's DirectSound.swift.
// The call starts on the phone system's route and stays connected there;
// this decides, from what actually arrives, when this browser plays the
// other side's sound from the direct link instead, and when it goes back.

/** Which route a call's sound is on. */
export type SoundRoute = "linx" | "direct";

/** What the two sides tell each other about the sound, on the call link.
 * One that knows nothing of it says nothing, and is never sent sound on the
 * direct link: it would play it on top of the phone system's, and every
 * word would be heard twice. */
export type SoundWord = "can" | "sending" | "direct" | "linx";

export const SOUND_WORDS: readonly SoundWord[] = ["can", "sending", "direct", "linx"];

/** How often the direct link's sound is read, in ms. */
export const SOUND_EVERY_MS = 500;
/** Readings with new sound before playing the direct link: a second of it. */
export const TO_SWITCH = 2;
/** Readings with nothing new before going back. Opus sends a packet at
 * least every 400 ms even in silence (DTX), so a second and a half with
 * none means the route has stopped, not that nobody spoke. */
export const TO_FALL_BACK = 3;
/** Readings with new sound before going direct again after falling back:
 * five seconds, so a route that comes and goes isn't followed. */
export const TO_RETURN = 10;

export class DirectSound {
  /** The other side said its microphone is on the direct link. */
  theySend = false;
  /** Which route this browser plays the other side's sound from. */
  playing: SoundRoute = "linx";
  /** Which route the other side plays this browser's sound from. */
  theyPlay: SoundRoute = "linx";
  private lastPackets: number | null = null;
  private arriving = 0;
  private quiet = 0;
  private fellBack = false;

  /** The other side said something about the sound. */
  heard(word: SoundWord) {
    switch (word) {
      case "sending":
        // What arrived before this was silence: counted afresh from here.
        this.theySend = true;
        this.arriving = 0;
        break;
      case "direct":
      case "linx":
        this.theyPlay = word;
    }
  }

  /** The direct link's sound packets from the other side so far; the route
   * to play from now on, when it changes. */
  reading(packets: number): SoundRoute | null {
    const fresh = this.lastPackets !== null && packets > this.lastPackets;
    this.lastPackets = packets;
    if (fresh) {
      this.arriving += 1;
      this.quiet = 0;
    } else {
      this.quiet += 1;
      this.arriving = 0;
    }
    if (this.playing === "linx") {
      if (!this.theySend || this.arriving < (this.fellBack ? TO_RETURN : TO_SWITCH)) return null;
      this.playing = "direct";
      return "direct";
    }
    if (this.quiet < TO_FALL_BACK) return null;
    this.playing = "linx";
    this.fellBack = true;
    return "linx";
  }

  /** The link went: back to the phone system at once, both ways. Whether
   * anything changed. */
  linkGone(): boolean {
    const changed = this.playing === "direct" || this.theyPlay === "direct";
    Object.assign(this, new DirectSound());
    return changed;
  }
}

/** Their sound packets that have arrived on a connection so far. */
export function soundPackets(report: RTCStatsReport): number {
  let n = 0;
  report.forEach((s: { type?: string; kind?: string; packetsReceived?: number }) => {
    if (s.type === "inbound-rtp" && s.kind === "audio") n += s.packetsReceived ?? 0;
  });
  return n;
}
