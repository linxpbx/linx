// Phone lines' and WireGuard connections' states in plain words and a dot
// tone, shared by the admin home page and System → Status.
export type Tone = "good" | "warn" | "bad" | "neutral";

export const trunkDot: Record<string, Tone> = {
  registered: "good", reachable: "good", unknown: "warn", unreachable: "bad", rejected: "bad", disabled: "neutral",
};
export const trunkWords: Record<string, string> = {
  registered: "Working", reachable: "Working", unknown: "Checking…", unreachable: "Down", rejected: "Refused", disabled: "Turned off",
};

export const tunnelDot: Record<string, Tone> = { up: "good", connecting: "warn", unknown: "warn", down: "bad" };
export const tunnelWords: Record<string, string> = { up: "Connected", connecting: "Connecting…", unknown: "Checking…", down: "Not connected" };

// A line's kind and security in plain words (docs/ui/ADMIN_SCREENS_PHASE1E.md §6.1).
export type LineLike = {
  kind: string; unencrypted: boolean; cert_trust: string; wireguard_profile_id?: string;
};
export const kindWords: Record<string, string> = {
  registers_here: "Phone system (signs in to Linx)",
  registration: "Phone company",
  ip_authenticated: "Phone company (calls in from its address)",
  lan_peer: "Phone system on your network",
};
export function securityWords(t: LineLike, simple: boolean): { text: string; warn: boolean } {
  if (t.wireguard_profile_id) return { text: simple ? "Private connection" : "Through a WireGuard connection", warn: false };
  if (t.unencrypted) return { text: "Not encrypted", warn: true };
  if (t.cert_trust === "pinned") return { text: "Encrypted, trusted certificate", warn: false };
  return { text: "Encrypted", warn: false };
}

/** "1st", "2nd", … for the outgoing order. */
export function ordinal(n: number): string {
  const s = ["th", "st", "nd", "rd"];
  const v = n % 100;
  return n + (s[(v - 20) % 10] ?? s[v] ?? s[0]!);
}

/** "since 09:12" today, else "since 28 Sep". */
export function sinceWords(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  const today = new Date();
  const same = d.toDateString() === today.toDateString();
  return same
    ? `since ${d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
    : `since ${d.toLocaleDateString([], { day: "numeric", month: "short" })}`;
}
