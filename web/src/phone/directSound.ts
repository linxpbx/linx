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
/** Readings with no sound at all before the direct link counts as gone:
 * four seconds. Opus sends a packet about every 400 ms in silence (DTX) and
 * a browser pauses the microphone for a moment when its audio device
 * changes, so anything shorter mistakes a quiet talker for a broken route
 * -- which made the sound flip to the phone system and back every few
 * seconds (the owner's calls, 2026-10-22). The route itself saying it is in
 * trouble (`linkUp`) is the honest signal. */
export const GONE_FOR = 8;
/** Readings with new sound before going direct again: a second. Going back
 * is cheap now that neither side quiets what it sends through the phone
 * system. */
export const TO_RETURN = 2;
/** How many times "can" is said again while the other side still hasn't
 * answered "sending" -- ten seconds at SOUND_EVERY_MS. The words are the
 * only way each side learns the other can take sound on the link, and one
 * of them can be lost for good: `calllink.Hub.Relay` drops a message sent
 * before the two ends are paired, and a link that reconnects resets both
 * sides' state while the words are only said once. Saying it again heals
 * that. It stops after this many because a peer that knows nothing of the
 * direct sound never answers at all, and must not be asked for ever. */
export const SOUND_ANNOUNCES = 20;

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

  /** The direct link's sound packets from the other side so far, whether
   * its own route is still carrying (`linkUp`) and whether the phone
   * system's route is (`otherUp`); the route to play from now on, when it
   * changes.
   *
   * The direct link is the better route -- one stream end to end, where the
   * phone system's is two legs stitched in the middle, so each leg's loss
   * adds up. Leaving it needs it to have really stopped **and** the other
   * route to be carrying: falling back to a route that is itself dropped or
   * restarting buys nothing and costs the call its sound. */
  reading(packets: number, linkUp = true, otherUp = true): SoundRoute | null {
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
      if (!this.theySend || !linkUp || this.arriving < (this.fellBack ? TO_RETURN : TO_SWITCH)) return null;
      this.playing = "direct";
      return "direct";
    }
    if (!otherUp || (linkUp && this.quiet < GONE_FOR)) return null;
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
