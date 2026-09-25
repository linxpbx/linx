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
