// System → Server settings (docs/INSTALL.md §7): the page talks to the API
// with a system admin's session, or, on a repair link that skips the
// sign-in (docs/ui/INSTALL_SCREENS.md §5.3), to the repair page's own
// endpoints, which the link's cookie alone opens.
import { api } from "@/api/client";
import type { components } from "@/api/schema";

export type ServerSettings = components["schemas"]["ServerSettingsView"];
export type ServerSettingsState = components["schemas"]["ServerSettings"];
export type ServerChange = components["schemas"]["ServerSettingsChange"];
export type ServerPreview = components["schemas"]["ServerSettingsPreview"];
export type FrontDoorKind = NonNullable<ServerChange["front_door"]>;

/** What a request came back with: data, or the problem and its status. */
export type Result<T> = { data?: T; error?: unknown; status: number };

export interface ServerSettingsClient {
  load(): Promise<Result<ServerSettingsState>>;
  preview(c: ServerChange): Promise<Result<ServerPreview>>;
  change(c: ServerChange): Promise<Result<null>>;
}

// How long the page waits for the server before saying Linx is restarting:
// a port a move closed may drop the request rather than refuse it, and
// then nothing would ever answer (Demo A, the VPS back to 443).
export const LOAD_TIMEOUT_MS = 8000;

/** Whether the page can open movedTo by itself once it answers: the same
 *  name on another port (the page's policy lets it check only that; a new
 *  domain needs signing in again there anyway). */
export function opensBySelf(movedTo: string, here: string): boolean {
  if (!movedTo) return false;
  const to = new URL(movedTo), at = new URL(here);
  return to.hostname === at.hostname && to.origin !== at.origin;
}

/** Through the API, as the signed-in system admin. */
export const sessionClient: ServerSettingsClient = {
  async load() {
    const { data, error, response } = await api.GET("/api/v1/server-settings", { signal: AbortSignal.timeout(LOAD_TIMEOUT_MS) });
    return { data, error, status: response.status };
  },
  async preview(body) {
    const { data, error, response } = await api.POST("/api/v1/server-settings/preview", { body });
    return { data, error, status: response.status };
  },
  async change(body) {
    const { error, response } = await api.POST("/api/v1/server-settings", { body });
    return { data: response.ok ? null : undefined, error, status: response.status };
  },
};

/** The repair page's own endpoints, for a link that skips the sign-in. */
export const REPAIR_SETTINGS = "/repair/api/server-settings";

async function repairFetch<T>(path: string, body?: ServerChange): Promise<Result<T>> {
  const res = await fetch(path, body === undefined ? { credentials: "same-origin", signal: AbortSignal.timeout(LOAD_TIMEOUT_MS) } : {
    method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  });
  const text = await res.text();
  const parsed: unknown = text ? JSON.parse(text) : null;
  return res.ok ? { data: parsed as T, status: res.status } : { error: parsed, status: res.status };
}

export const repairClient: ServerSettingsClient = {
  load: () => repairFetch<ServerSettingsState>(REPAIR_SETTINGS),
  preview: (body) => repairFetch<ServerPreview>(REPAIR_SETTINGS + "/preview", body),
  async change(body) {
    const r = await repairFetch<null>(REPAIR_SETTINGS, body);
    return r.error === undefined ? { data: null, status: r.status } : r;
  },
};

/** Front doors as the page names them. */
export const DOORS: Record<string, string> = {
  "linx-443": "Nothing else uses port 443 — Linx takes it", proxy: "Another program passes Linx through",
  pangolin: "Pangolin passes Linx through", nginx: "nginx or HAProxy passes Linx through",
  "home-only": "Nothing: only at home (no internet)", "http-proxy": "A proxy that unlocks the traffic (advanced)", none: "Nothing yet",
  "public-port": "Nothing here can pass Linx through on 443: another public port (advanced)",
};

/** Front doors shown only under Advanced. */
export const ADVANCED_DOORS = ["public-port", "http-proxy"];

/** Front doors that are another program at an address on the home network. */
export const PROXY_DOORS = ["proxy", "pangolin", "nginx", "http-proxy"];

/** The choice an older setup's "pangolin" or "nginx" is now: "proxy". */
export function doorChoice(kind: string): string {
  return kind === "pangolin" || kind === "nginx" ? "proxy" : kind;
}
