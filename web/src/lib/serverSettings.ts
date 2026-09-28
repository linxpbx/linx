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

/** Through the API, as the signed-in system admin. */
export const sessionClient: ServerSettingsClient = {
  async load() {
    const { data, error, response } = await api.GET("/api/v1/server-settings");
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
  const res = await fetch(path, body === undefined ? { credentials: "same-origin" } : {
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
  "linx-443": "Linx takes port 443 itself", pangolin: "Pangolin", nginx: "nginx or HAProxy", "http-proxy": "Caddy or Nginx Proxy Manager",
  "home-only": "Nothing: home network only", none: "Nothing yet",
};

/** Front doors that are another program at an address on the home network. */
export const PROXY_DOORS = ["pangolin", "nginx", "http-proxy"];
