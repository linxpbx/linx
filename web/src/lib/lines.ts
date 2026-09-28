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
