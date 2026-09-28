// A stand-in Linx server for screenshot tests: the API, the Team websocket
// and a tiny SIP registrar on /sip, all answered inside Playwright so the
// screens can be seen in every state without the real stack. The real
// stack is exercised by the browser call suite (e2e/calls.spec.ts).
import type { Page, WebSocketRoute } from "@playwright/test";

export const DOMAIN = "sip.linx.test";
export const ME = { name: "Mohammed Al Mansoori", email: "mohammed@example.com", extension: "1001", username: "d_Web00001" };

export const TEAM = [
  { extension: "1024", name: "Sara Haddad", status: "on_call", since: new Date(Date.now() - 252_000).toISOString() },
  { extension: "1040", name: "Daniel Reyes", status: "ringing", since: new Date().toISOString() },
  { extension: "1042", name: "Aisha Rahman", status: "available" },
  { extension: "1044", name: "Yusuf Nasser", status: "away" },
  { extension: "1045", name: "Priya Menon", status: "dnd" },
  { extension: "1031", name: "Omar Khalil", status: "available" },
  { extension: "1001", name: ME.name, status: "available" },
  { extension: "1047", name: "Chen Wei", status: "offline" },
];

type Json = Record<string, unknown>;

export interface FakeOptions {
  signedIn?: boolean;
  // code: an authenticator app and a passkey; passkey: a passkey only;
  // enroll: an admin with no second step yet.
  pending?: "code" | "passkey" | "enroll";
  // Company sign-in (Google) on the sign-in page, linked in My account;
  // companyRequired: "people must use company sign-in".
  company?: boolean;
  companyRequired?: boolean;
  // The admin home page and setup wizard (docs/ui/ADMIN_SCREENS_PHASE1E.md):
  // broadens the fake session's scopes and answers the settings/system
  // endpoints they need. setupStep is the wizard's saved resume point
  // (GET /setup): 1, like the real server, when nothing's done yet.
  admin?: boolean;
  setupStep?: number;
  setupCompleted?: boolean;
  // A system admin (the setup wizard's first screen offers a restore), and
  // a restore from a backup already under way (docs/BACKUP.md §4).
  systemAdmin?: boolean;
  restore?: "pending" | "running" | "failed";
  // System → Backups (docs/BACKUP.md §8 step 5): a few runs of history, and
  // the backup file's state (none by default).
  backups?: boolean;
  download?: "preparing" | "ready" | "failed" | "others";
  // System → Status (docs/ADMIN.md §9): the server helper isn't running,
  // or the phone system isn't answering.
  helperMissing?: boolean;
  phoneSystemDown?: boolean;
}

const GOOGLE = { id: "0199c1", kind: "google", name: "Google" };
const MICROSOFT = { id: "0199c2", kind: "microsoft", name: "Microsoft" };

export const PASSKEYS = [
  { id: "0199a1", name: "Mohammed's iPhone", synced: true, created_at: "2026-09-01T09:00:00Z", last_used_at: new Date().toISOString() },
  { id: "0199a2", name: "Mac — Safari", synced: true, created_at: "2026-09-02T09:00:00Z", last_used_at: new Date(Date.now() - 3 * 86_400_000).toISOString() },
];

// People, Extensions and devices (docs/ui/ADMIN_SCREENS_PHASE1E.md §4-5):
// enough seed data and CRUD to screenshot every list/status/sheet state.
interface FakeExtension {
  id: string; number: string; display_name: string; enabled: boolean; created_at: string; updated_at: string; etag: string;
}
interface FakeUser {
  id: string; email: string; name: string; role: string; extension_id?: string; mfa_enabled: boolean; passkeys: number;
  has_password: boolean; password_only: boolean; company_sign_in: string[]; disabled: boolean; locked: boolean;
  created_at: string; updated_at: string; etag: string;
}
interface FakeDevice {
  id: string; extension_id: string; name: string; kind: string; sip_username: string; enabled: boolean; online: boolean;
  revoked_at?: string; created_at: string; updated_at: string; etag: string;
}

function seedPeople(): { extensions: FakeExtension[]; users: FakeUser[]; devices: FakeDevice[] } {
  const now = new Date().toISOString();
  const extensions: FakeExtension[] = [
    { id: "e1001", number: "1001", display_name: ME.name, enabled: true, created_at: now, updated_at: now, etag: '"1"' },
    { id: "e1024", number: "1024", display_name: "Sara Haddad", enabled: true, created_at: now, updated_at: now, etag: '"1"' },
    { id: "e1110", number: "1110", display_name: "Reception", enabled: true, created_at: now, updated_at: now, etag: '"1"' },
  ];
  const users: FakeUser[] = [
    { id: "u1001", email: ME.email, name: ME.name, role: "system_admin", extension_id: "e1001", mfa_enabled: true, passkeys: 2,
      has_password: true, password_only: false, company_sign_in: [], disabled: false, locked: false, created_at: now, updated_at: now, etag: '"1"' },
    { id: "u1024", email: "sara@example.com", name: "Sara Haddad", role: "user", extension_id: "e1024", mfa_enabled: false, passkeys: 0,
      has_password: false, password_only: false, company_sign_in: ["Google"], disabled: false, locked: false, created_at: now, updated_at: now, etag: '"1"' },
    { id: "u1042", email: "aisha@example.com", name: "Aisha Rahman", role: "admin", mfa_enabled: false, passkeys: 0,
      has_password: false, password_only: false, company_sign_in: [], disabled: false, locked: false, created_at: now, updated_at: now, etag: '"1"' },
    { id: "u1044", email: "yusuf@example.com", name: "Yusuf Nasser", role: "user", mfa_enabled: false, passkeys: 0,
      has_password: true, password_only: true, company_sign_in: [], disabled: false, locked: true, created_at: now, updated_at: now, etag: '"1"' },
    { id: "u1047", email: "chen@example.com", name: "Chen Wei", role: "reporter", mfa_enabled: false, passkeys: 0,
      has_password: true, password_only: false, company_sign_in: [], disabled: true, locked: false, created_at: now, updated_at: now, etag: '"1"' },
  ];
  const devices: FakeDevice[] = [
    { id: "d1", extension_id: "e1024", name: "Sara's desk", kind: "softphone", sip_username: "d_x7k2m9", enabled: true, online: true, created_at: now, updated_at: now, etag: '"1"' },
  ];
  return { extensions, users, devices };
}

export async function fakeServer(page: Page, opts: FakeOptions = {}) {
  if (process.env.LINX_E2E_DEBUG) {
    page.on("console", (m) => console.log(`[page] ${m.type()}: ${m.text()}`));
    page.on("pageerror", (e) => console.log(`[page] uncaught: ${e.stack ?? e.message}`));
  }
  let restore: Json | undefined = opts.restore && {
    status: opts.restore, source: "folder", location: "/var/backups/linx", snapshot: "latest", requested_at: new Date().toISOString(),
    error: opts.restore === "failed" ? "couldn't read that backup: wrong password, or not a backup folder" : undefined,
  };
  let restoreFinished = false;
  const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
  let backupSettings: Json = { frequency: opts.backups ? "daily" : "off", time_of_day: 180, day_of_week: 0, day_of_month: 1 };
  const backupRuns = opts.backups ? [
    { id: "r3", trigger: "manual", started_at: ago(30), finished_at: ago(29), status: "partial",
      destinations: [{ name: "local", ok: true, snapshot_id: "8c1d42e7a0b9", size: 401_234 }, { name: "office-nas", ok: false, error: "preparing the repository: restic init: subprocess ssh: ssh: connect to host 192.168.1.250 port 22: Connection refused Fatal: create repository at sftp:backup@192.168.1.250:/srv/linx failed: unable to start the sftp session, error: error receiving version packet from server: server unexpectedly closed connection: unexpected EOF" }] },
    { id: "r2", trigger: "scheduled", started_at: ago(60 * 20), finished_at: ago(60 * 20 - 1), status: "success",
      destinations: [{ name: "local", ok: true, snapshot_id: "4f2a9c1e5d6b", size: 398_870 }, { name: "office-nas", ok: true, snapshot_id: "77e0aa12cc03", size: 398_870 }] },
    { id: "r1", trigger: "scheduled", started_at: ago(60 * 44), finished_at: ago(60 * 44 - 1), status: "failure",
      destinations: [], error: "Couldn't prepare the backup: dumping the database: the database container isn't running" },
  ] : [];
  let backupDownload: Json = opts.download === "ready" || opts.download === "others"
    ? { status: "ready", mine: opts.download === "ready", size: 356_000_000, snapshot_time: ago(2), requested_at: ago(3), expires_at: new Date(Date.now() + 57 * 60_000).toISOString() }
    : opts.download === "preparing" ? { status: "preparing", mine: true, requested_at: ago(1) }
    : opts.download === "failed" ? { status: "failed", mine: true, requested_at: ago(1), error: "backups don't go to a folder on this server, so there's nothing here to download." }
    : { status: "none", mine: false };
  const json = (body: unknown, status = 200) => ({
    status, contentType: status >= 400 ? "application/problem+json" : "application/json", body: JSON.stringify(body),
  });
  const seed = seedPeople();
  const people = { extensions: seed.extensions, users: seed.users, devices: seed.devices, nextId: 2000 };
  const idAfter = (p: string, prefix: string) => (p.startsWith(prefix) ? p.slice(prefix.length).split("/")[0] : null);
  const now = () => new Date().toISOString();

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const p = url.pathname;
    // Like the real server's validator (api/openapi.yaml): every PATCH but
    // a passkey's rename is application/merge-patch+json.
    const contentType = (await route.request().headerValue("content-type")) ?? "";
    if (method === "PATCH" && !p.startsWith("/api/v1/me/passkeys/") && !contentType.startsWith("application/merge-patch+json")) {
      return route.fulfill(json({ type: "about:blank", title: "Bad Request", status: 400, code: "request_invalid",
        detail: `request body has an error: header Content-Type has unexpected value "${contentType}"` }, 400));
    }
    if (p === "/api/v1/users" && method === "GET") return route.fulfill(json({ items: people.users }));
    if (p === "/api/v1/users" && method === "POST") {
      const body = route.request().postDataJSON() as { email: string; name: string; role: string; extension_id?: string };
      const id = `u${people.nextId++}`;
      people.users.push({
        id, email: body.email, name: body.name, role: body.role, extension_id: body.extension_id, mfa_enabled: false, passkeys: 0,
        has_password: false, password_only: false, company_sign_in: [], disabled: false, locked: false, created_at: now(), updated_at: now(), etag: '"1"',
      });
      return route.fulfill(json({ user: people.users.at(-1), setup_link_token: "fake-invite-token" }, 201));
    }
    const patchUserId = idAfter(p, "/api/v1/users/");
    if (patchUserId && p === `/api/v1/users/${patchUserId}` && method === "PATCH") {
      const u = people.users.find((x) => x.id === patchUserId);
      if (!u) return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "No." }, 404));
      Object.assign(u, route.request().postDataJSON(), { updated_at: now(), etag: '"2"' });
      return route.fulfill(json(u));
    }
    if (patchUserId && p === `/api/v1/users/${patchUserId}/setup-link` && method === "POST") {
      return route.fulfill(json({ setup_link_token: "fake-invite-token" }));
    }
    if (patchUserId && p === `/api/v1/users/${patchUserId}/reset-mfa` && method === "POST") {
      const u = people.users.find((x) => x.id === patchUserId);
      if (u) Object.assign(u, { mfa_enabled: false, passkeys: 0 });
      return route.fulfill(json(u));
    }
    if (patchUserId && p === `/api/v1/users/${patchUserId}/unlock` && method === "POST") {
      const u = people.users.find((x) => x.id === patchUserId);
      if (u) u.locked = false;
      return route.fulfill(json(u));
    }
    if (p === "/api/v1/extensions" && method === "GET") return route.fulfill(json({ items: people.extensions }));
    if (p === "/api/v1/extensions" && method === "POST") {
      const body = route.request().postDataJSON() as { number: string; display_name: string };
      const id = `e${people.nextId++}`;
      people.extensions.push({ id, number: body.number, display_name: body.display_name, enabled: true, created_at: now(), updated_at: now(), etag: '"1"' });
      return route.fulfill(json(people.extensions.at(-1), 201));
    }
    const extId = idAfter(p, "/api/v1/extensions/");
    if (extId && p === `/api/v1/extensions/${extId}` && method === "PATCH") {
      const e = people.extensions.find((x) => x.id === extId);
      if (!e) return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "No." }, 404));
      Object.assign(e, route.request().postDataJSON(), { updated_at: now(), etag: '"2"' });
      return route.fulfill(json(e));
    }
    if (extId && p === `/api/v1/extensions/${extId}` && method === "DELETE") {
      people.extensions = people.extensions.filter((x) => x.id !== extId);
      return route.fulfill({ status: 204 });
    }
    if (extId && p === `/api/v1/extensions/${extId}/devices` && method === "GET") {
      return route.fulfill(json({ items: people.devices.filter((d) => d.extension_id === extId) }));
    }
    if (extId && p === `/api/v1/extensions/${extId}/devices` && method === "POST") {
      const body = route.request().postDataJSON() as { name: string };
      const id = `d${people.nextId++}`;
      const device = { id, extension_id: extId, name: body.name, kind: "softphone", sip_username: `d_${id}`, enabled: true, online: false, created_at: now(), updated_at: now(), etag: '"1"' };
      people.devices.push(device);
      return route.fulfill(json({
        device, password: "not-a-real-password", server: DOMAIN, port: 5061, transport: "tls",
        settings_text: `Server: ${DOMAIN}\nPort: 5061\nTransport: TLS\nUsername: ${device.sip_username}`,
      }, 201));
    }
    if (p === "/api/v1/devices" && method === "GET") return route.fulfill(json({ items: people.devices }));
    const devId = idAfter(p, "/api/v1/devices/");
    if (devId && p === `/api/v1/devices/${devId}` && method === "DELETE") {
      const d = people.devices.find((x) => x.id === devId);
      if (d) d.revoked_at = now();
      return route.fulfill({ status: 204 });
    }
    if (devId && p === `/api/v1/devices/${devId}/reset-password` && method === "POST") {
      const d = people.devices.find((x) => x.id === devId);
      if (!d) return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "No." }, 404));
      return route.fulfill(json({
        device: d, password: "not-a-real-password-either", server: DOMAIN, port: 5061, transport: "tls",
        settings_text: `Server: ${DOMAIN}\nPort: 5061\nTransport: TLS\nUsername: ${d.sip_username}`,
      }));
    }
    if (p === "/api/v1/me") {
      if (!opts.signedIn && !opts.pending) {
        return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "unauthenticated", detail: "Sign in." }, 401));
      }
      const adminScopes = ["settings:read", "settings:write", "system:read", "calls:read", "routing:read", "users:read", "users:write",
        "extensions:read", "extensions:write", "devices:read", "devices:write", "routing:write", "backups:read", "backups:write",
        "logs:read", "system:write"];
      return route.fulfill(json({
        id: "0199", type: "user", role: opts.systemAdmin ? "system_admin" : "admin", scopes: opts.pending ? [] : ["team:read", ...(opts.admin ? adminScopes : [])], pending: !!opts.pending,
        email: ME.email, name: ME.name, extension: ME.extension, presence: "available",
        mfa_enabled: opts.pending === "code" || !opts.pending, passkeys: opts.pending === "enroll" ? 0 : opts.pending === "code" ? 1 : PASSKEYS.length,
        has_password: true, password_only: opts.pending === "enroll", recovery_codes_left: opts.pending === "enroll" ? 0 : 8,
        company_sign_in: opts.company ? ["Google"] : [],
      }));
    }
    if (p === "/api/v1/system/services/asterisk/log" && method === "GET") {
      const t0 = Date.now() - 40 * 60_000;
      const lines = [
        "Asterisk 22.11.0 built by linx",
        "Loading realtime configuration from the database",
        "res_odbc: Connecting linx-db... connected",
        "PJSIP: transport-tls listening on 0.0.0.0:5061",
        "PJSIP: transport-wss listening on 0.0.0.0:8089",
        "Asterisk Ready.",
        "Endpoint 1042-desk is now Reachable",
        "Endpoint 1001-web is now Reachable",
        "WARNING: res_pjsip_outbound_registration.c: No response to REGISTER for 'trunk-0199e2' (sip.telnyx.example): the provider didn't answer in 32 seconds, retrying in 60 seconds",
        "Endpoint 1042-desk is now Unreachable",
      ];
      return route.fulfill(json({
        service: "asterisk",
        lines: lines.map((text, i) => ({ time: new Date(t0 + i * 4 * 60_000).toISOString(), text })),
      }));
    }
    if (p.startsWith("/api/v1/system/services/") && p.endsWith("/restart") && method === "POST") {
      const service = p.split("/")[5];
      return route.fulfill(json({ service, result: "restarted" }));
    }
    if (p === "/api/v1/system/status" && method === "GET") {
      const started = new Date(Date.now() - 3 * 86_400_000).toISOString();
      const svc = (service: string, label: string, state = "running", extra: object = {}) => ({
        service, label, state, started_at: state === "running" ? started : undefined, restarts: 0,
        can_restart: !["postgres", "step-ca"].includes(service), optional: ["wireguard", "sni"].includes(service), ...extra,
      });
      return route.fulfill(json({
        helper: opts.helperMissing ? { connected: false } : { connected: true, checked_at: new Date(Date.now() - 6_000).toISOString() },
        containers: opts.helperMissing ? [] : [
          svc("control-plane", "Web and API"),
          opts.phoneSystemDown
            ? svc("asterisk", "Phone system", "unhealthy", { restarts: 3, started_at: new Date(Date.now() - 20 * 60_000).toISOString() })
            : svc("asterisk", "Phone system"),
          svc("coturn", "Calls-from-outside relay"),
          svc("postgres", "Database"),
          svc("certd", "Certificates"),
          svc("step-ca", "Internal certificates"),
          svc("wireguard", "WireGuard connections", "missing"),
          svc("sni", "Front door on port 443", "missing"),
        ],
        certificate: { expires_at: new Date(Date.now() + 61 * 86_400_000).toISOString() },
        services: { "control-plane": "ok", asterisk: opts.phoneSystemDown ? "degraded" : "ok", database: "ok" },
        open_alerts: [
          { severity: "critical", title: "Line down", message: 'Line "Telnyx" is down since 09:12.', since: new Date(Date.now() - 4 * 60_000).toISOString() },
          { severity: "warning", title: "Certificate", message: "Certificate renews in 9 days (normal).", since: new Date().toISOString() },
        ],
        trunks: [
          { id: "0199e1", name: "UCM", status: "registered" },
          { id: "0199e2", name: "Telnyx", status: "unreachable", status_since: new Date(Date.now() - 4 * 60_000).toISOString() },
        ],
        wireguard_profiles: [],
      }));
    }
    if (p === "/api/v1/backup-restore" && method === "GET") {
      // After a restore finishes, this session no longer exists.
      if (restoreFinished) {
        return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "unauthenticated", detail: "Sign in." }, 401));
      }
      if (!restore) return route.fulfill(json({ status: "none" }));
      if (restore.status === "pending") restoreFinished = true; // one "waiting" answer, then done
      return route.fulfill(json(restore));
    }
    if (p === "/api/v1/backup-restore/file" && method === "PUT") {
      return route.fulfill(json({ upload_id: "0199b0aa-0000-7000-8000-000000000001", size: route.request().postDataBuffer()?.length ?? 0 }, 201));
    }
    if (p === "/api/v1/backup-settings" && method === "GET") return route.fulfill(json(backupSettings));
    if (p === "/api/v1/backup-settings" && method === "PATCH") {
      backupSettings = { ...backupSettings, ...(route.request().postDataJSON() as Json) };
      return route.fulfill(json(backupSettings));
    }
    if (p === "/api/v1/backups" && method === "GET") return route.fulfill(json({ items: backupRuns }));
    if (p === "/api/v1/backups" && method === "POST") {
      backupSettings = { ...backupSettings, requested_at: now() };
      return route.fulfill({ status: 202 });
    }
    if (p === "/api/v1/backup-download" && method === "GET") return route.fulfill(json(backupDownload));
    if (p === "/api/v1/backup-download" && method === "POST") {
      backupDownload = { status: "pending", mine: true, requested_at: now() };
      return route.fulfill(json(backupDownload, 202));
    }
    if (p === "/api/v1/backup-download/password" && method === "POST") {
      return route.fulfill(json({ password: "example-backup-password-for-screenshots" }));
    }
    if (p === "/api/v1/backup-restore" && method === "POST") {
      const body = route.request().postDataJSON() as { source: "folder" | "destination" | "upload"; location: string; snapshot: string };
      restore = { status: "pending", source: body.source, location: body.location, snapshot: body.snapshot, requested_at: new Date().toISOString() };
      return route.fulfill(json(restore, 202));
    }
    if (p === "/api/v1/setup" && method === "GET") {
      return route.fulfill(json({ step: opts.setupStep ?? 1, completed: !!opts.setupCompleted }));
    }
    if (p === "/api/v1/setup" && method === "PUT") {
      const body = route.request().postDataJSON() as { step: number; complete?: boolean };
      return route.fulfill(json({ step: body.step, completed: !!body.complete }));
    }
    if (p === "/api/v1/settings" && method === "GET") {
      return route.fulfill(json({
        country: "AE", extension_digits: 3,
        extension_ranges: [{ kind: "people", from: 100, to: 599 }, { kind: "groups", from: 600, to: 699 }, { kind: "reserved", from: 700, to: 899 }],
        site_kind: "business", simple_mode: true, admin_network_restricted: false, admin_networks: [],
        default_call_permission_level_id: opts.setupCompleted ? "0199f1" : undefined, setup_step: opts.setupStep ?? 1,
      }));
    }
    if (p === "/api/v1/settings" && method === "PATCH") return route.fulfill({ status: 204 });
    if (p === "/api/v1/inbound-routes" && method === "GET") return route.fulfill(json({ items: [] }));
    if (p === "/api/v1/calls/active" && method === "GET") {
      return route.fulfill(json({
        phone_engine_connected: true,
        items: [
          { id: "0199c1", direction: "outbound", from: { extension: "1024", name: "Sara Haddad" }, to: "+971501234567", state: "answered", started_at: new Date(Date.now() - 252_000).toISOString() },
          { id: "0199c2", direction: "internal", from: { extension: "1031", name: "Omar Khalil" }, to: "1042", state: "answered", started_at: new Date().toISOString() },
        ],
      }));
    }
    if (p === "/api/v1/numbering/next" && method === "GET") return route.fulfill(json({ number: "1111" }));
    if (p === "/api/v1/call-permission-levels" && method === "GET") return route.fulfill(json({ items: [] }));
    if (p === "/api/v1/sign-in-options") {
      return route.fulfill(json({ company: opts.company ? [GOOGLE] : [], company_sign_in_required: !!opts.companyRequired, passkeys_available: true }));
    }
    if (p === "/api/v1/session/company" && method === "POST") {
      // The provider refused: straight back to the sign-in page with why.
      return route.fulfill(json({ url: "/?company_error=no_account" }));
    }
    if (p === "/api/v1/me/sso-links" && method === "GET") {
      return route.fulfill(json({
        items: opts.company ? [{ id: "0199d1", provider_id: GOOGLE.id, provider_name: "Google", email: ME.email, created_at: "2026-09-20T09:00:00Z" }] : [],
        available: [GOOGLE, MICROSOFT],
      }));
    }
    if (p.startsWith("/api/v1/me/passkeys/") && method === "DELETE") {
      return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm." }, 403));
    }
    if (p.startsWith("/api/v1/setup-links/") && method === "GET") {
      if (p.endsWith("/used")) {
        return route.fulfill(json({ type: "about:blank", title: "Bad Request", status: 400, code: "setup_link_invalid", detail: "Used." }, 400));
      }
      return route.fulfill(json({ email: ME.email, name: ME.name, role: "system_admin", has_second_step: false, passkeys_available: true }));
    }
    if (p === "/api/v1/me/passkeys" && method === "GET") return route.fulfill(json({ items: PASSKEYS }));
    if (p === "/api/v1/session" && method === "POST") {
      return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "sign_in_invalid", detail: "Wrong email or password." }, 401));
    }
    if (p === "/api/v1/session/mfa" && method === "POST") {
      // "111111" was already used; anything else finds the half-finished
      // sign-in timed out.
      const { code } = route.request().postDataJSON() as { code: string };
      if (code === "111111") {
        return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "mfa_code_used", detail: "Used." }, 401));
      }
      return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "session_expired", detail: "Expired." }, 401));
    }
    if (p === "/api/v1/session" && method === "DELETE") return route.fulfill({ status: 204 });
    if (p === "/api/v1/me/mfa" && method === "POST") {
      return route.fulfill(json({ secret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", otpauth_url: "otpauth://totp/Linx:mohammed@example.com?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Linx" }));
    }
    if (p === "/api/v1/me/mfa/confirm" && method === "POST") {
      const { code } = route.request().postDataJSON() as { code: string };
      if (code !== "123456") {
        return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "mfa_code_invalid", detail: "That code isn't right." }, 401));
      }
      return route.fulfill(json({ recovery_codes: ["k7qm-2xpd", "9fwr-t3vh", "c4zn-8bqe", "mh6s-w2ya", "p3dx-r9kf", "v8tl-5jgn", "b2ec-q7mw", "x5hy-4nsd", "f9ua-6czt", "e3kp-h8rv"] }));
    }
    if (p === "/api/v1/me/web-phone") {
      return route.fulfill(json({
        device_id: "0199", sip_username: ME.username, password: "not-a-real-password", sip_uri: `sip:${ME.username}@${DOMAIN}`,
        websocket_path: "/sip", display_name: ME.name, extension: ME.extension,
        turn: { urls: [], username: "x", credential: "x", expires_at: new Date(Date.now() + 3_600_000).toISOString() },
      }));
    }
    if (p === "/api/v1/me/presence") return route.fulfill({ status: 204 });
    return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "No." }, 404));
  });
  await page.routeWebSocket("**/api/v1/team/live", (ws) => {
    ws.send(JSON.stringify({ items: TEAM }));
  });
  const sip = new FakeSIP(page);
  await page.routeWebSocket("**/sip", (ws) => sip.attach(ws));
  return sip;
}

// --- A minimal SIP peer over the page's websocket ---

function headers(msg: string): Map<string, string> {
  const h = new Map<string, string>();
  for (const line of msg.split("\r\n\r\n")[0]!.split("\r\n").slice(1)) {
    const i = line.indexOf(":");
    if (i > 0) h.set(line.slice(0, i).trim().toLowerCase(), line.slice(i + 1).trim());
  }
  return h;
}

function withTag(to: string): string {
  return to.includes(";tag=") ? to : `${to};tag=fake${Math.random().toString(36).slice(2, 8)}`;
}

export class FakeSIP {
  private ws: WebSocketRoute | null = null;
  private pendingInvite: { msg: string; to: string } | null = null;
  private inviteWaiters: (() => void)[] = [];
  registered: Promise<void>;
  private onRegistered!: () => void;

  constructor(private page: Page) {
    this.registered = new Promise((r) => (this.onRegistered = r));
  }

  attach(ws: WebSocketRoute) {
    this.ws = ws;
    ws.onMessage((m) => {
      if (process.env.LINX_E2E_DEBUG) console.log(`[sip] ${String(m).split("\r\n", 1)[0]}`);
      void this.handle(String(m));
    });
  }

  private reply(req: string, code: number, reason: string, extra: string[] = [], body = "", toOverride?: string) {
    const h = headers(req);
    const lines = [
      `SIP/2.0 ${code} ${reason}`,
      `Via: ${h.get("via")}`,
      `From: ${h.get("from")}`,
      `To: ${toOverride ?? (code === 100 ? h.get("to") : withTag(h.get("to") ?? ""))}`,
      `Call-ID: ${h.get("call-id")}`,
      `CSeq: ${h.get("cseq")}`,
      ...extra,
      `Content-Length: ${new TextEncoder().encode(body).length}`,
      "",
      body,
    ];
    this.ws?.send(lines.join("\r\n"));
  }

  private async handle(msg: string) {
    const method = msg.split(" ", 1)[0];
    const h = headers(msg);
    if (method === "REGISTER") {
      this.reply(msg, 200, "OK", [`Contact: ${h.get("contact")};expires=300`, "Expires: 300"]);
      this.onRegistered();
    } else if (method === "INVITE") {
      this.reply(msg, 100, "Trying");
      const to = withTag(h.get("to") ?? "");
      this.reply(msg, 180, "Ringing", [], "", to);
      this.pendingInvite = { msg, to };
      this.inviteWaiters.splice(0).forEach((w) => w());
    } else if (method === "BYE" || method === "CANCEL") {
      this.reply(msg, 200, "OK");
    }
  }

  /** Answers the call the page is making, with a real WebRTC answer made in the page. */
  async answer() {
    if (!this.pendingInvite) await new Promise<void>((r) => this.inviteWaiters.push(r));
    const inv = this.pendingInvite;
    this.pendingInvite = null;
    if (!inv) throw new Error("no call to answer");
    const offer = inv.msg.split("\r\n\r\n").slice(1).join("\r\n\r\n");
    const sdp = await this.page.evaluate(async (offerSdp) => {
      const pc = new RTCPeerConnection();
      (window as unknown as { __remotePc: RTCPeerConnection }).__remotePc = pc;
      const tone = new AudioContext().createMediaStreamDestination().stream;
      for (const t of tone.getTracks()) pc.addTrack(t, tone);
      await pc.setRemoteDescription({ type: "offer", sdp: offerSdp });
      await pc.setLocalDescription(await pc.createAnswer());
      await new Promise<void>((r) => {
        if (pc.iceGatheringState === "complete") r();
        pc.onicegatheringstatechange = () => pc.iceGatheringState === "complete" && r();
        setTimeout(r, 2000);
      });
      return pc.localDescription!.sdp;
    }, offer);
    this.reply(inv.msg, 200, "OK", [`Contact: <sip:1024@fake.invalid;transport=ws>`, "Content-Type: application/sdp"], sdp, inv.to);
  }

  /** Rings the page: an incoming call from `name` at `number`. */
  async ring(name: string, number: string) {
    const offer = await this.page.evaluate(async () => {
      const pc = new RTCPeerConnection();
      const tone = new AudioContext().createMediaStreamDestination().stream;
      for (const t of tone.getTracks()) pc.addTrack(t, tone);
      await pc.setLocalDescription(await pc.createOffer());
      await new Promise((r) => setTimeout(r, 500));
      return pc.localDescription!.sdp;
    });
    const branch = "z9hG4bK" + Math.random().toString(36).slice(2);
    const lines = [
      `INVITE sip:${ME.username}@fake.invalid;transport=ws SIP/2.0`,
      `Via: SIP/2.0/WSS fake.invalid;branch=${branch}`,
      "Max-Forwards: 70",
      `From: "${name}" <sip:${number}@${DOMAIN}>;tag=caller1`,
      `To: <sip:${ME.username}@${DOMAIN}>`,
      `Call-ID: incoming-${branch}`,
      "CSeq: 1 INVITE",
      `Contact: <sip:${number}@fake.invalid;transport=ws>`,
      "Content-Type: application/sdp",
      `Content-Length: ${new TextEncoder().encode(offer).length}`,
      "",
      offer,
    ];
    this.ws?.send(lines.join("\r\n"));
  }
}

export type { Json };

// The web install's plain page (docs/ui/INSTALL_SCREENS.md §2): its own
// small API on port 6464, answered here. "rented" or "home" is what setup
// detected; the check refuses co.uk like the host does.
export type FakeCert = Record<string, unknown>;

/** A certificate page as the host would tell it (docs/ui/INSTALL_SCREENS.md §2.7). */
export function fakeCert(over: FakeCert = {}): FakeCert {
  return {
    mode: "port443", domain: "example.com", front_door: "linx-443",
    add_records: [{ type: "A", name: "meet.example.com", value: "203.0.113.5" }, { type: "A", name: "turn.example.com", value: "203.0.113.5" }],
    dns: { state: "wrong", checked_at: "2026-09-28T08:07:15Z", names: [
      { name: "meet.example.com", state: "wrong", seen: ["198.51.100.7"] }, { name: "turn.example.com", state: "missing" }] },
    prepare: { state: "ok" }, reach: {}, records: {}, certificate: {},
    ...over,
  };
}

const acceptedAnswers = {
  where: "rented", front_door: "linx-443", domain: "example.com", name: "Mohammed AlMudharreb", email: "mohammed@example.com",
  time_zone: "Asia/Dubai", agreed_to_terms: true,
};

export async function fakeInstall(page: Page, where: "rented" | "home", opts: {
  closed?: boolean; expiresIn?: number; cert?: FakeCert; accepted?: Record<string, unknown>;
} = {}) {
  const facts = where === "home"
    ? { where, public_address: "5.36.12.4", lan_address: "192.168.1.212", lan_network: "192.168.1.0/24", time_zone: "Asia/Dubai", hardware: "4 processor cores, 8 GB memory, 62 GB free" }
    : { where, public_address: "203.0.113.5", time_zone: "Etc/UTC", hardware: "4 processor cores, 8 GB memory, 62 GB free" };
  const expiresIn = opts.expiresIn ?? 2832;
  let draft: unknown;
  let accepted: unknown = opts.cert ? { ...acceptedAnswers, where, ...opts.accepted } : undefined;
  let cert: FakeCert | undefined = opts.cert;
  await page.route("**/install/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (opts.closed) return route.fulfill({ status: 404, body: "" });
    if (url.pathname === "/install/api/state") {
      return route.fulfill({ json: { facts, draft, accepted, cert, expires_at: new Date(Date.now() + expiresIn * 1000).toISOString(), expires_in: expiresIn, connected: true } });
    }
    if (url.pathname === "/install/api/draft") {
      draft = route.request().postDataJSON();
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/check") {
      const a = route.request().postDataJSON() as { domain: string };
      if (a.domain === "co.uk") {
        return route.fulfill({ status: 422, json: { errors: [{ step: "domain", field: "domain", message: "co.uk is shared by everyone. Use your own domain." }] } });
      }
      accepted = a;
      cert = fakeCert({ domain: a.domain, add_records: ["meet", "turn"].map((h) => ({ type: "A", name: `${h}.${a.domain}`, value: facts.public_address })) });
      return route.fulfill({ json: { ok: true } });
    }
    if (url.pathname === "/install/api/door-ready" && cert) {
      cert = { ...cert, setup: { ...(cert.setup as object), done: true } };
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/handoff") {
      return route.fulfill({ json: { handoff: "h".repeat(43) } });
    }
    if (url.pathname === "/install/api/token") {
      const { token } = route.request().postDataJSON() as { token: string };
      if (token.length < 20) return route.fulfill({ status: 422, json: { errors: [{ step: "token", field: "token", message: "That doesn't look like a DNS provider token (5 characters)." }] } });
      cert = { ...cert, token_saved: true, records: { state: "running" } };
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ status: 404, body: "" });
  });
}

/**
 * The secure page at https://meet.example.com, served from the test's own
 * build (the requests never leave the browser).
 */
export async function fakeSecureInstall(page: Page, base: string, opts: { usedHandoff?: boolean } = {}) {
  await page.route("https://meet.example.com/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/install/api/redeem") {
      return opts.usedHandoff
        ? route.fulfill({ status: 409, json: { detail: "That link to the secure page can't be used." } })
        : route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/state") {
      return route.fulfill({ json: { facts: { where: "rented" }, secure: true, cert: fakeCert({ certificate: { state: "ok" } }),
        expires_at: new Date(Date.now() + 3600_000).toISOString(), expires_in: 3600, connected: true } });
    }
    const path = url.pathname.startsWith("/install") ? "/" : url.pathname;
    const res = await route.fetch({ url: base + path });
    return route.fulfill({ response: res });
  });
}
