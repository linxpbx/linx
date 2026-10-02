// Small words for voicemail screens (Voicemail, its settings, System → Settings).

/** m:ss */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** "4 MB", "120 KB" */
export function space(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${Math.round(bytes / (1024 * 1024))} MB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KB`;
  return bytes > 0 ? "under 1 KB" : "nothing yet";
}
