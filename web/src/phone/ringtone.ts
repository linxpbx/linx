// A ring made with Web Audio (no sound file to download): two short
// tones every 3 seconds, at the volume chosen in Settings.
export class Ringtone {
  private ctx: AudioContext | null = null;
  private timer: number | undefined;

  constructor(private volume: () => number) {}

  start() {
    if (this.timer !== undefined) return;
    try {
      this.ctx ??= new AudioContext();
      void this.ctx.resume();
    } catch {
      return; // no audio output; the overlay still shows the call
    }
    const ring = () => {
      const ctx = this.ctx;
      if (!ctx) return;
      const gain = ctx.createGain();
      gain.gain.value = Math.max(0, Math.min(1, this.volume())) * 0.25;
      gain.connect(ctx.destination);
      for (const [at, freq] of [[0, 480], [0.45, 440]] as const) {
        const osc = ctx.createOscillator();
        osc.frequency.value = freq;
        osc.connect(gain);
        osc.start(ctx.currentTime + at);
        osc.stop(ctx.currentTime + at + 0.4);
      }
    };
    ring();
    this.timer = window.setInterval(ring, 3000);
  }

  stop() {
    if (this.timer !== undefined) window.clearInterval(this.timer);
    this.timer = undefined;
  }
}

// The ringing tone a browser plays to itself while a call it is making
// rings at the other end. Asterisk answers an extension's INVITE with a
// bare 180 and no sound in it — the standard leaves the ringing tone to
// the side that dialled — so without this a call to an extension was
// silent until it was picked up (owner, 2026-10-21). A call out through a
// phone line is different: the provider sends its own ringing in a 183,
// and that is heard instead.
//
// 425 Hz, a second on and three off: the ringing tone of the UAE and most
// of the world outside North America (ITU-T E.180). At a fixed, modest
// volume, not the ring's — a ring is meant to be heard across a room, this
// one is in your ear.
export class Ringback {
  private ctx: AudioContext | null = null;
  private timer: number | undefined;

  start() {
    if (this.timer !== undefined) return;
    try {
      this.ctx ??= new AudioContext();
      void this.ctx.resume();
    } catch {
      return; // no audio output; the call still shows on the screen
    }
    const ring = () => {
      const ctx = this.ctx;
      if (!ctx) return;
      const gain = ctx.createGain();
      gain.gain.value = 0.2;
      gain.connect(ctx.destination);
      const osc = ctx.createOscillator();
      osc.frequency.value = 425;
      osc.connect(gain);
      osc.start(ctx.currentTime);
      osc.stop(ctx.currentTime + 1);
    };
    ring();
    this.timer = window.setInterval(ring, 4000);
  }

  stop() {
    if (this.timer !== undefined) window.clearInterval(this.timer);
    this.timer = undefined;
  }
}
