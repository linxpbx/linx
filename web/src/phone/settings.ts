// This browser's own audio choices (Settings screen). Stored on this device
// only: nothing here is account data, and none of it is secret.
export interface AudioSettings {
  microphoneId: string; // "" = the browser's default
  speakerId: string;
  ringVolume: number; // 0..1
}

const KEY = "linx.audio";
const DEFAULTS: AudioSettings = { microphoneId: "", speakerId: "", ringVolume: 0.8 };

export function loadAudioSettings(): AudioSettings {
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return { ...DEFAULTS };
    const v = JSON.parse(raw) as Partial<AudioSettings>;
    return {
      microphoneId: typeof v.microphoneId === "string" ? v.microphoneId : "",
      speakerId: typeof v.speakerId === "string" ? v.speakerId : "",
      ringVolume: typeof v.ringVolume === "number" && v.ringVolume >= 0 && v.ringVolume <= 1 ? v.ringVolume : DEFAULTS.ringVolume,
    };
  } catch {
    return { ...DEFAULTS };
  }
}

export function saveAudioSettings(s: AudioSettings) {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(s));
  } catch {
    // Private windows may refuse storage; the choice still applies until reload.
  }
}
