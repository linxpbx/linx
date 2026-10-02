// A stand-in Linx server for screenshot tests: the API, the Team websocket
// and a tiny SIP registrar on /sip, all answered inside Playwright so the
// screens can be seen in every state without the real stack. The real
// stack is exercised by the browser call suite (e2e/calls.spec.ts).
import type { Page, WebSocketRoute } from "@playwright/test";
import doorSetupFixture from "./door-setup.json" with { type: "json" };

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
  // Check it (docs/SIMPLER.md §2.3): the phone link still waiting, or the
  // phone came (from outside, or with Wi-Fi still on) and tested the relay.
  reach?: "waiting" | "reached" | "wifi" | "wrong-dns";
  // System → Server settings (docs/INSTALL.md §7): open (sudo linx setup
  // has it open) at home or on a rented server; closed by default.
  serverSettings?: "home" | "rented";
  // The repair page on port 6464 (docs/INSTALL.md §7): its link claimed,
  // with a system admin's sign-in or without.
  repair?: "sign-in" | "no-sign-in";
  // "Moved to a new place?" (docs/INSTALL.md §8): restored from a backup
  // made at home under another domain, now on a rented server.
  moved?: boolean;
  // Phone lines (docs/ui/ADMIN_SCREENS_PHASE1E.md §6-9): none at all, for
  // the empty states; seeded lines by default.
  noLines?: boolean;
  // GET /numbering/next follows the saved people range (the setup wizard's
  // range-change test).
  followRanges?: boolean;
  // Help's written answers (docs/HELP.md §4) turned on, with Anthropic.
  answers?: boolean;
  // Email (ADR-066): "on" sends through Google Workspace; "failing" has
  // emails waiting after the password was refused; absent: not set up.
  email?: "on" | "failing";
}

// Phone lines, numbers and routing (docs/ui/ADMIN_SCREENS_PHASE1E.md §6-9).
function seedLines(none: boolean) {
  const at = new Date(Date.now() - 86_400_000).toISOString();
  const since = new Date(Date.now() - 4 * 60_000).toISOString();
  const base = { port: 5061, transport: "tls", media_encryption: "srtp", cert_trust: "public", dial_format: "e164", codecs: ["alaw", "ulaw"],
    max_calls: 4, unencrypted: false, enabled: true, created_at: at, updated_at: at, etag: '"1"' };
  const trunks: Json[] = none ? [] : [
    { ...base, id: "0199e1", name: "UCM landlines", kind: "registers_here", template: "phone_system", host: "", dial_format: "local",
      username: "trunk-0199e1a0-5278-704a-acbf-7fa42dd4bb5f", status: "registered", status_detail: "It's signed in to Linx.",
      status_since: at, outbound_priority: 1, rings_extension_id: "e1110" },
    { ...base, id: "0199e2", name: "Telnyx", kind: "registration", template: "telnyx", host: "sip.telnyx.com", username: "linx-office",
      status: "unreachable", status_detail: "Linx can't sign in to it: it refused the login or didn't answer.", status_since: since, outbound_priority: 2 },
    { ...base, id: "0199e3", name: "Old SIP", kind: "ip_authenticated", host: "203.0.113.40", port: 5060, transport: "udp", media_encryption: "none",
      unencrypted: true, unencrypted_confirmed_by: "mohammed@example.com", status: "reachable", status_detail: "It answers Linx's keep-alive checks.", status_since: at },
  ];
  const dids: Json[] = none ? [] : [
    { id: "0199f1", trunk_id: "0199e1", number: "+97142000100", label: "", extension_id: "e1001", created_at: at, updated_at: at, etag: '"1"' },
    { id: "0199f2", trunk_id: "0199e2", number: "+97142000101", label: "", extension_id: "e1110", created_at: at, updated_at: at, etag: '"1"' },
    { id: "0199f3", trunk_id: "0199e2", number: "+97142000102", label: "Sales", created_at: at, updated_at: at, etag: '"1"' },
    { id: "0199f4", trunk_id: "0199e3", number: "+97142000103", label: "", extension_id: "e1024", created_at: at, updated_at: at, etag: '"1"' },
  ];
  const level: Json = { id: "0199f1", name: "Everyone", allowed_categories: ["landline", "service", "mobile", "national", "toll_free"],
    withhold_caller_id: false, created_at: at, updated_at: at, etag: '"1"' };
  const profiles: Json[] = [];
  return { trunks, dids, level, profiles, alert: { minutes: 60, calls: 10 } };
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
  let serverSettings: { portainer: boolean; [k: string]: unknown } | undefined = opts.serverSettings && {
    where: opts.serverSettings, front_door: opts.serverSettings === "home" ? "pangolin" : "linx-443", domain: "example.com",
    provider: opts.serverSettings === "home" ? "porkbun" : "cloudflare", dns_by_hand: false,
    token_saved: opts.serverSettings === "home", profile: "lite", profile_pick: "lite", profile_reason: "4 processor cores and 8 GB memory",
    profiles: [
      { name: "lite", description: "audio first, small meetings, AI off, lighter monitoring" },
      { name: "standard", description: "all features, medium-sized meetings" },
      { name: "performance", description: "all features, large meetings, can run AI on this server" },
    ],
    portainer: false, portainer_allowed: opts.serverSettings === "home", apply: { state: "" }, steps: [], keep: [],
    expires_at: new Date(Date.now() + 4 * 3600_000).toISOString(),
    proxy_address: opts.serverSettings === "home" ? "192.168.1.30" : undefined,
    door_setup: opts.serverSettings === "home" ? doorSetup("example.com", "192.168.1.30") : undefined,
    front_doors: opts.serverSettings === "home" ? ["linx-443", "proxy", "home-only", "public-port", "http-proxy"] : ["linx-443", "proxy", "public-port"],
    address: "https://example.com",
    public_address: "203.0.113.5", lan_address: opts.serverSettings === "home" ? "192.168.1.212" : undefined,
    repair: !!opts.repair, no_sign_in: opts.repair === "no-sign-in",
    problem: opts.repair ? "x509: certificate has expired or is not yet valid" : undefined,
  };
  type Change = { profile: string; portainer: boolean; token?: string; domain?: string; front_door?: string; proxy_address?: string; door_done?: boolean;
    public_port?: number; turn_udp_port?: number;
    dns_key?: { provider: string; token?: string; key?: Record<string, string> }; dns_by_hand?: boolean };
  const companyName = (id?: string) => ({ porkbun: "Porkbun", cloudflare: "Cloudflare", ovh: "OVH" } as Record<string, string>)[id ?? ""] ?? id;
  // What setup on the server says a change needs (docs/INSTALL.md §7).
  const settingsPreview = (body: Change) => {
    const key = body.dns_key;
    const errors: { field: string; message: string }[] = key && Object.values(key.key ?? { token: key.token ?? "" }).some((v) => v === "bad-key" || v.length < 5)
      ? [{ field: "token", message: `${companyName(key.provider)} didn't let this key read example.com's records (${companyName(key.provider)}: Invalid API key).` }] : [];
    const home = serverSettings?.where === "home";
    const domain = body.domain ?? "example.com";
    const warnings = body.domain ? [
      `Linx moves to https://${domain}. https://example.com stops working, and everyone signs in again at the new address.`,
      "Passkeys only work at the address they were made for. Before you apply, check you can sign in with your password and authenticator app (or a recovery code), then add new passkeys at the new address. A system admin with only a passkey gets back in with  sudo linx user setup-link EMAIL  on the server.",
      `If you use company sign-in, change its redirect address at Google or Microsoft to https://${domain}/api/v1/sso/callback.`,
      ...(home ? [`Desk phones and phone apps set up with sip.example.com need sip.${domain} as their server: change it on each one.`] : []),
    ] : [];
    // Another public port (docs/ui/SCREENS_PHASE1F.md §4.3).
    const port = body.front_door === "public-port" ? body.public_port ?? 0 : 0;
    const address = port ? `https://${domain}:${port}` : `https://${domain}`;
    if (port && !body.domain) {
      warnings.push(
        `Linx moves to ${address}. Links already sent for https://example.com (invites, setup links) stop working once nothing forwards it here: send new ones.`,
        "Passkeys keep working: they belong to example.com, whatever the port.",
        `If you use company sign-in, add ${address}/api/v1/sso/callback as a redirect address at Google or Microsoft. Until you do, “Continue with Google” or Microsoft shows “redirect URI mismatch”.`,
        "Browser calls from networks that only allow standard web traffic, like some hotels, workplaces, public Wi-Fi and mobile networks, may fail or have no audio, because this setup can't use port 443 for them. Calls from normal home and mobile networks work.",
      );
    }
    if (port && !serverSettings?.token_saved && !key) {
      errors.push({ field: "token", message: "Without port 443, Let's Encrypt can only check your domain through your DNS company: add its key below." });
    }
    const addRecords = body.domain && !serverSettings?.token_saved
      ? [{ type: "A", name: domain, value: "203.0.113.5" }, { type: "A", name: `turn.${domain}`, value: "203.0.113.5" }] : [];
    if (addRecords.length) warnings.push("Add the DNS records below at your DNS company first: Linx checks them before it asks Let's Encrypt.");
    const door = String(body.front_door ?? serverSettings?.front_door ?? "");
    const udp = body.turn_udp_port || 443;
    const setup = port
      ? { files: [], steps: home
        ? [`On your router, forward TCP ${port} to 192.168.1.212 port ${port}.`, `On your router, forward UDP ${udp} to 192.168.1.212 port ${udp}.`,
          "People at the office use the same address. If it doesn't open there, turn on NAT loopback (hairpin) on your router, or add the domain to your local DNS pointing at 192.168.1.212."]
        : [`Linx opens TCP ${port} and UDP ${udp} on this server itself: there's nothing to forward. If your server provider has a firewall of its own, open those two there.`] }
      : (body.domain || body.front_door) && PASS_THROUGH.includes(door)
        ? doorSetup(domain, body.proxy_address ?? String(serverSettings?.proxy_address ?? "192.168.1.30")) : undefined;
    if (setup) warnings.push("Until the steps below are done, Linx can't be reached from outside your network.");
    const steps = [
      ...(addRecords.length ? [`Check the DNS records for ${domain}`, `Test certificate for ${domain}, turn.${domain}`, `Certificate for ${domain}, turn.${domain}`] : []),
      "Save your settings", ...(key ? [`Your ${companyName(key.provider)} key`] : []),
      ...(body.front_door ? [`Firewall for ${body.front_door}`] : []),
      ...(body.portainer && !serverSettings?.portainer ? ["Portainer (home network only)"] : []), "Restart Linx with the new settings",
      ...((serverSettings?.token_saved && (body.domain || body.front_door)) || key || body.dns_by_hand === false
        ? [`${domain}, turn.${domain} at this network's public address`] : []),
    ];
    return { errors, add_records: addRecords, warnings, steps, address, ...(setup ? { setup } : {}) };
  };
  const settingsChange = (body: Change) => {
    const p = settingsPreview(body);
    if (p.errors.length) return json({ status: 422, code: "token_refused", detail: p.errors[0]!.message }, 422);
    serverSettings = {
      ...serverSettings!, apply: { state: "running" },
      steps: p.steps.map((title, i) => ({ title, state: i === 0 ? "ok" : i === 1 ? "running" : "" })),
      keep: body.portainer && !serverSettings!.portainer
        ? [{ title: "Portainer password", value: "Qx7-m2Pd-9vRk", note: "Open https://192.168.1.212:9443 from your home network and sign in as admin." }] : [],
    };
    return { status: 202, body: "" };
  };
  const movedTicks: Record<string, boolean> = {};
  let movedHidden = false;
  const movedChecklist = () => {
    if (!opts.moved) return {};
    const items = [
      { id: "old_server", title: "Turn the old server off", why: "Both servers would point the same names at themselves, and phones and phone lines could reach the wrong one." },
      { id: "lan_peer:0199", title: "Phone line \"UCM landlines\" is tied to the old network", link: "phone_lines",
        why: "It connects to a phone system on your home network: check its address, and that it knows this server's." },
      { id: "provider:0198", title: "Tell the provider of \"Telnyx\" your new address: 203.0.113.5", link: "phone_lines",
        why: "It lets calls in only from the address it knows, and that was the old server's." },
      { id: "desk_phones", title: "3 desk phones and phone apps were set up on the old network", link: "extensions",
        why: "Give each one this server's address, sip.example.com, and check it connects." },
      { id: "admin_networks", title: "Admins only from 192.168.1.0/24 (the old network)", link: "settings",
        why: "Admins can sign in from this server's own network, but not from other places the old list allowed." },
      { id: "passkeys", title: "New domain: add new passkeys", link: "account",
        why: "Passkeys belong to pbx.old.com, so they don't work here. Sign in with your password and authenticator app, then add new ones." },
      { id: "backups", title: "Add your backup places again", link: "backups", auto: true,
        why: "Where backups go, and their keys, stay on the old server. This ticks itself after the first backup here." },
    ].map((it) => ({ ...it, done: !!movedTicks[it.id] }));
    if (items.every((it) => it.done || it.auto) && items.some((it) => it.done) && items.filter((it) => !it.done).length === 0) return {};
    return { checklist: {
      detected_at: new Date(Date.now() - 3600_000).toISOString(),
      before: { domain: "pbx.old.com", lan_networks: ["192.168.1.0/24"], lan_address: "192.168.1.212", public_address: "198.51.100.4", front_door: "pangolin" },
      after: { domain: "example.com", lan_networks: [], lan_address: "", public_address: "203.0.113.5", front_door: "linx-443" },
      items, ...(movedHidden ? { hidden_until: new Date(Date.now() + 7 * 86_400_000).toISOString() } : {}),
    } };
  };
  const json = (body: unknown, status = 200) => ({
    status, contentType: status >= 400 ? "application/problem+json" : "application/json", body: JSON.stringify(body),
  });
  const seed = seedPeople();
  const lines = seedLines(!!opts.noLines);
  const settings: Json = {
    country: "AE", extension_digits: 3,
    extension_ranges: [{ kind: "people", from: 100, to: 599 }, { kind: "groups", from: 600, to: 699 }, { kind: "reserved", from: 700, to: 899 }],
    site_kind: "business", simple_mode: true, admin_network_restricted: false, admin_networks: [], company_sign_in_required: false,
    default_call_permission_level_id: opts.setupCompleted ? "0199f1" : undefined, setup_step: opts.setupStep ?? 1,
  };
  const minsAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();
  const system = {
    confirmed: false, allowed: false,
    alerts: [
      { id: "a1", key: "trunk.down:0199e2", severity: "critical", title: "Phone line \"Telnyx\" is down",
        message: "Linx can't sign in to it: it refused the login or didn't answer. Outgoing calls use the next line.", status: "open",
        first_seen_at: minsAgo(4), last_seen_at: minsAgo(0) },
      { id: "a2", key: "backup.failure", severity: "warning", title: "A backup failed", message: "The office NAS didn't answer.",
        status: "resolved", first_seen_at: minsAgo(60 * 20), last_seen_at: minsAgo(60 * 19), resolved_at: minsAgo(60 * 19) },
    ] as Json[],
    channels: [
      { id: "0199c1", kind: "ntfy", name: "Mohammed's phone", min_severity: "warning",
        quiet_hours: { enabled: true, start: "22:00", end: "07:00", timezone: "Asia/Dubai", bypass_critical: true },
        enabled: true, created_by: "user:0199", created_at: minsAgo(9000), updated_at: minsAgo(9000), etag: '"1"' },
    ] as Json[],
    audit: [
      { id: "l1", at: minsAgo(3), actor: "user:u1001", ip: "192.168.1.23", action: "user.create", target: "user:u1047", result: "ok", detail: { name: "Chen Wei", role: "reporter" } },
      { id: "l2", at: minsAgo(4), actor: "user:u1001", ip: "192.168.1.23", action: "user.sign_in", result: "ok", detail: { method: "passkey" } },
      { id: "l3", at: minsAgo(40), actor: "api_key:0199k1", ip: "203.0.113.9", action: "extension.update", target: "extension:e1110", result: "ok", detail: { number: "1110" } },
      { id: "l4", at: minsAgo(60 * 26), actor: "", ip: "198.51.100.77", action: "user.sign_in_code", result: "denied", detail: { reason: "wrong_code" } },
      { id: "l5", at: minsAgo(60 * 30), actor: "system:cli", action: "trunk.create", target: "trunk:0199e1", result: "ok", detail: { name: "UCM landlines", kind: "registers_here" } },
    ] as Json[],
    sessions: [
      { id: "s1", current: true, user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36",
        ip: "192.168.1.23", created_at: minsAgo(300), last_seen_at: minsAgo(0) },
      { id: "s2", current: false, user_agent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
        ip: "94.200.12.7", created_at: minsAgo(3000), last_seen_at: minsAgo(60 * 20) },
    ] as Json[],
    webhooks: [
      { id: "0199w1", url: "https://crm.example.com/hooks/linx", description: "CRM", event_types: ["call.ended", "call.missed"], enabled: true,
        last_success_at: minsAgo(90), failing_since: minsAgo(30), created_by: "user:u1001", created_at: minsAgo(9000), updated_at: minsAgo(9000), etag: '"1"' },
      { id: "0199w2", url: "https://hooks.example.org/old", description: "", event_types: [], enabled: false, disabled_reason: "failing",
        created_by: "user:u1001", created_at: minsAgo(20000), updated_at: minsAgo(20000), etag: '"1"' },
    ] as Json[],
    keys: [
      { id: "0199k1", name: "CRM", prefix: "linx_0199k1", role: "admin", scopes: ["extensions:read", "calls:read", "users:read"], allowed_ips: [],
        created_by: "user:u1001", created_at: minsAgo(20000), expires_at: new Date(Date.now() + 60 * 86_400_000).toISOString(), last_used_at: minsAgo(40), last_used_ip: "203.0.113.9" },
    ] as Json[],
    email: (opts.email ? { enabled: true, preset: "google", host: "smtp.gmail.com", port: 465, security: "tls", username: "pbx@example.com",
      from_address: "pbx@example.com", from_name: "Linx at Example Co", password_set: true, hourly_limit: 60, arrived_at: minsAgo(600), etag: '"1"',
      status: opts.email === "failing"
        ? { last_sent_at: minsAgo(300), sent_last_hour: 0, waiting: 3, last_error: "The mail server didn't accept the user name and password. (535 5.7.8 Username and Password not accepted.)" }
        : { last_sent_at: minsAgo(25), sent_last_hour: 12, waiting: 0, last_error: "" } }
      : { enabled: false, preset: "google", host: "", port: 465, security: "tls", username: "", from_address: "", from_name: "", password_set: false,
        hourly_limit: 60, etag: '"0"', status: { sent_last_hour: 0, waiting: 0, last_error: "" } }) as Json,
    answers: { enabled: !!opts.answers, provider: "anthropic", base_url: "", model: "claude-haiku-4-5", api_key_set: !!opts.answers,
      person_daily_limit: 200, server_daily_limit: 1000, used_today: opts.answers ? 14 : 0, etag: '"1"' } as Json,
    providers: [
      { id: "0199b1", kind: "google", name: "Google", issuer: "https://accounts.google.com", client_id: "1234.apps.googleusercontent.com",
        client_secret_set: true, enabled: true, shown: true, position: 0, redirect_uri: "https://example.com/api/v1/sso/callback",
        created_at: minsAgo(9000), updated_at: minsAgo(9000), etag: '"1"' },
    ] as Json[],
  };
  const notFound = () => json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "No." }, 404);
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
      const body = route.request().postDataJSON() as { email: string; name: string; role: string; extension_id?: string; send_email?: boolean };
      const id = `u${people.nextId++}`;
      people.users.push({
        id, email: body.email, name: body.name, role: body.role, extension_id: body.extension_id, mfa_enabled: false, passkeys: 0,
        has_password: false, password_only: false, company_sign_in: [], disabled: false, locked: false, created_at: now(), updated_at: now(), etag: '"1"',
      });
      return route.fulfill(json({ user: people.users.at(-1), setup_link_token: "fake-invite-token",
        ...(body.send_email ? { email: { to: body.email, queued: !!opts.email } } : {}) }, 201));
    }
    const patchUserId = idAfter(p, "/api/v1/users/");
    if (patchUserId && p === `/api/v1/users/${patchUserId}` && method === "PATCH") {
      const u = people.users.find((x) => x.id === patchUserId);
      if (!u) return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "No." }, 404));
      Object.assign(u, route.request().postDataJSON(), { updated_at: now(), etag: '"2"' });
      return route.fulfill(json(u));
    }
    if (patchUserId && p === `/api/v1/users/${patchUserId}/setup-link` && method === "POST") {
      const send = (route.request().postDataJSON() as { send_email?: boolean } | null)?.send_email;
      const u = people.users.find((x) => x.id === patchUserId);
      return route.fulfill(json({ setup_link_token: "fake-invite-token", ...(send && u ? { email: { to: u.email, queued: !!opts.email } } : {}) }));
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
        "logs:read", "system:write", "trunks:read", "trunks:write", "alerts:read", "alerts:write", "audit:read", "sso:read", "sso:write",
        "outbound_allowlist:write", "webhooks:read", "webhooks:write", "api_keys:read", "api_keys:write", "oauth_clients:read", "oauth_clients:write"];
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
    if (p.startsWith("/api/v1/reach/") && method === "POST") {
      if (p.endsWith("/relay")) return route.fulfill({ status: 204 });
      if (p.endsWith("/USEDCODE22")) {
        return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "reach_link_invalid",
          detail: "This link can't be used. Links work once, for 10 minutes: make a new one with Check it." }, 404));
      }
      return route.fulfill(json({ domain: "example.com", address: "5.194.33.12",
        turn: { urls: ["turns:turn.example.com:443?transport=tcp"], username: "1790000000:linx-reach-c0de", credential: "x" } }));
    }
    if (p === "/api/v1/system/reach-check" && method === "POST") {
      return route.fulfill(json({ checked_at: new Date().toISOString(), lines: [
        { state: "ok", text: "example.com points to 94.200.1.10 (your home's address)" },
        opts.reach === "wrong-dns"
          ? { state: "fail", text: "turn.example.com points to 94.200.1.9, not 94.200.1.10 (your home's address).",
            meaning: "People outside are sent somewhere else.", fix: "records" }
          : { state: "ok", text: "turn.example.com points to 94.200.1.10 (your home's address)" },
        { state: "ok", text: "example.com answers with Linx's certificate" },
        { state: "fail", text: "turn.example.com doesn't answer (connection refused).",
          meaning: "Your front door isn't sending turn.example.com to port 5349. Calls from outside will have no audio.", fix: "steps" },
        { state: "info", text: "Your router can't reach its own address from inside, so this server can't test the rest.", meaning: "Use your phone below." },
      ] }));
    }
    if (p === "/api/v1/system/dns-records" && method === "GET") {
      // Kept right automatically when Server settings has a key (home), by hand otherwise.
      const auto = !!serverSettings?.token_saved && !serverSettings?.dns_by_hand;
      return route.fulfill(json({ domain: "example.com", zone: "example.com", company: "Porkbun",
        name_servers: ["curitiba.ns.porkbun.com", "maceio.ns.porkbun.com"], checked_at: new Date().toISOString(), records: auto ? [
          { use: "web", name: "example.com", type: "A", value: "94.200.1.10", state: "ok", seen: ["94.200.1.10"], kept: true },
          { use: "turn", name: "turn.example.com", type: "A", value: "94.200.1.10", state: "ok", seen: ["94.200.1.10"], kept: true },
          { use: "sip", name: "sip.example.com", type: "A", value: "192.168.1.212", state: "ok", seen: ["192.168.1.212"], kept: true },
        ] : [
          { use: "web", name: "example.com", type: "A", value: "94.200.1.10", state: "ok", seen: ["94.200.1.10"], kept: false },
          { use: "turn", name: "turn.example.com", type: "A", value: "94.200.1.10", state: "wrong", seen: ["94.200.1.9"], kept: false },
          { use: "sip", name: "sip.example.com", type: "A", value: "192.168.1.212", state: "missing", seen: [], kept: false },
        ], ...(auto ? { automatic: { company: "Porkbun", address: "94.200.1.10", previous: "94.200.1.9",
          changed_at: new Date(Date.now() - 3 * 86_400_000).toISOString(), checked_at: new Date().toISOString() } } : {}) }));
    }
    const reachLink = (state: string, extra: object = {}) => ({ id: "0199d0c2-7a00-7000-8000-00000000c0de", url: "https://example.com/reach/7K2QHM4XRB",
      state, expires_at: new Date(Date.now() + 10 * 60_000).toISOString(), version: state === "waiting" ? 0 : 2, ...extra });
    if (p === "/api/v1/system/reach-links" && method === "POST") return route.fulfill(json(reachLink("waiting"), 201));
    if (p.startsWith("/api/v1/system/reach-links/") && method === "GET") {
      if (opts.reach === "reached") return route.fulfill(json(reachLink("reached", { address: "5.194.33.12", seen: "outside", relay: { ok: true } })));
      if (opts.reach === "wifi") return route.fulfill(json(reachLink("reached", { address: "94.200.1.10", seen: "home", relay: { ok: true } })));
      // Still waiting: the real server holds this for 25 s.
      await new Promise((r) => setTimeout(r, 5_000));
      return route.fulfill(json(reachLink("waiting"))).catch(() => {});
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
    if (p === "/api/v1/server-settings" && method === "GET") {
      return route.fulfill(json(serverSettings ? { open: true, settings: serverSettings } : { open: false }));
    }
    if (p === "/api/v1/moved-checklist" && method === "GET") return route.fulfill(json(movedChecklist()));
    if (p === "/api/v1/moved-checklist" && method === "PATCH") {
      const body = route.request().postDataJSON() as { ticks?: Record<string, boolean>; hide?: boolean };
      Object.assign(movedTicks, body.ticks ?? {});
      if (body.hide !== undefined) movedHidden = body.hide;
      return route.fulfill(json(movedChecklist()));
    }
    if (p === "/api/v1/server-settings/preview" && method === "POST" && serverSettings) {
      return route.fulfill(json(settingsPreview(route.request().postDataJSON() as Change)));
    }
    if (p === "/api/v1/server-settings" && method === "POST" && serverSettings) {
      return route.fulfill(settingsChange(route.request().postDataJSON() as Change));
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
    if (p === "/api/v1/settings" && method === "GET") return route.fulfill(json(settings));
    if (p === "/api/v1/settings" && method === "PATCH") {
      const body = route.request().postDataJSON() as Json;
      if ((body.admin_network_restricted !== undefined || body.company_sign_in_required !== undefined) && !system.confirmed) {
        return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      }
      Object.assign(settings, body);
      return route.fulfill(json(settings));
    }
    if (p === "/api/v1/alerts" && method === "GET") return route.fulfill(json({ items: system.alerts }));
    if (p === "/api/v1/alert-channels" && method === "GET") return route.fulfill(json({ items: system.channels }));
    if (p === "/api/v1/alert-channels" && method === "POST") {
      const body = route.request().postDataJSON() as { kind: string; name: string; config: Json; min_severity?: string; quiet_hours?: Json };
      const host = String(body.config.server_url ?? body.config.url ?? "");
      if (/\/\/(192\.168|10\.)/.test(host) && !system.allowed) {
        return route.fulfill(json({ type: "about:blank", title: "Unprocessable", status: 422, code: "url_blocked",
          detail: "Server: that address is on a private network. Allow it on the outbound allowlist first." }, 422));
      }
      const c = { id: `0199c${people.nextId++}`, kind: body.kind, name: body.name, min_severity: body.min_severity ?? "info",
        quiet_hours: body.quiet_hours, enabled: true, created_by: "user:0199", created_at: now(), updated_at: now(), etag: '"1"' };
      system.channels.unshift(c);
      return route.fulfill(json({ alert_channel: c }, 201));
    }
    const chId = idAfter(p, "/api/v1/alert-channels/");
    if (chId && p.endsWith("/test") && method === "POST") return route.fulfill(json({ succeeded: true, status_code: 200, duration_ms: 240 }));
    if (chId && method === "PATCH") {
      const c = system.channels.find((x) => x.id === chId);
      if (!c) return route.fulfill(notFound());
      Object.assign(c, route.request().postDataJSON(), { etag: '"2"' });
      return route.fulfill(json(c));
    }
    if (chId && method === "DELETE") { system.channels = system.channels.filter((x) => x.id !== chId); return route.fulfill({ status: 204 }); }
    if (p === "/api/v1/outbound-allowlist" && method === "POST") {
      if (!system.confirmed) return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      system.allowed = true;
      return route.fulfill(json({ id: "0199d9", value: "192.168.1.40", kind: "cidr", description: "", created_by: "user:0199", created_at: now() }, 201));
    }
    if (p === "/api/v1/session/confirm" && method === "POST") { system.confirmed = true; return route.fulfill({ status: 204 }); }
    if (p === "/api/v1/audit-log" && method === "GET") return route.fulfill(json({ items: system.audit }));
    if (p === "/api/v1/me/password/check" && method === "POST") {
      const body = route.request().postDataJSON() as { password: string };
      return route.fulfill(body.password === "correct horse battery" ? { status: 204 }
        : json({ type: "about:blank", title: "Unauthorized", status: 401, code: "password_invalid", detail: "Your current password is incorrect." }, 401));
    }
    if (p === "/api/v1/me/email" && method === "PUT") {
      if (!system.confirmed) return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      const body = route.request().postDataJSON() as { email: string };
      if (people.users.some((u) => u.email === body.email.toLowerCase())) {
        return route.fulfill(json({ type: "about:blank", title: "Conflict", status: 409, code: "email_taken", detail: "Someone already uses that email." }, 409));
      }
      return route.fulfill(json({ id: "0199", email: body.email.toLowerCase(), name: ME.name, role: "admin" }));
    }
    if (p === "/api/v1/me/sessions" && method === "GET") return route.fulfill(json({ items: system.sessions }));
    if (p === "/api/v1/me/sessions/sign-out-others" && method === "POST") {
      system.sessions = system.sessions.filter((x) => x.current);
      return route.fulfill({ status: 204 });
    }
    if (p.startsWith("/api/v1/me/sessions/") && method === "DELETE") {
      const id = p.split("/").pop();
      system.sessions = system.sessions.filter((x) => x.id !== id);
      return route.fulfill({ status: 204 });
    }
    if (p === "/api/v1/event-types" && method === "GET") {
      return route.fulfill(json({ items: ["user.created", "user.updated", "extension.created", "device.registered", "call.started", "call.ended",
        "call.missed", "trunk.status_changed"].map((name) => ({ name, description: name })) }));
    }
    if (p === "/api/v1/webhooks" && method === "GET") return route.fulfill(json({ items: system.webhooks }));
    if (p === "/api/v1/webhooks" && method === "POST") {
      const body = route.request().postDataJSON() as { url: string; description?: string; event_types?: string[] };
      const w = { id: `0199w${people.nextId++}`, url: body.url, description: body.description ?? "", event_types: body.event_types ?? [],
        enabled: true, created_by: "user:0199", created_at: now(), updated_at: now(), etag: '"1"' };
      system.webhooks.push(w);
      return route.fulfill(json({ webhook: w, secret: "whsec_EXAMPLEEXAMPLEEXAMPLEEXAMPLE0000" }, 201));
    }
    const whId = idAfter(p, "/api/v1/webhooks/");
    if (whId && p.endsWith("/test") && method === "POST") {
      return route.fulfill(json({ id: "0199x1", webhook_id: whId, event_id: "0199e0", event_type: "test", status: "succeeded", attempts: 1, max_attempts: 1,
        created_at: now(), log: [{ at: now(), status_code: 200, duration_ms: 183 }] }));
    }
    if (whId && p.endsWith("/deliveries") && method === "GET") {
      return route.fulfill(json({ items: [
        { id: "0199x2", webhook_id: whId, event_id: "e2", event_type: "call.ended", status: "failed", attempts: 8, max_attempts: 8, created_at: minsAgo(30) },
        { id: "0199x3", webhook_id: whId, event_id: "e3", event_type: "user.created", status: "succeeded", attempts: 1, max_attempts: 8, created_at: minsAgo(90) },
      ] }));
    }
    if (p === "/api/v1/api-keys" && method === "GET") return route.fulfill(json({ items: system.keys }));
    if (p === "/api/v1/oauth-clients" && method === "GET") return route.fulfill(json({ items: [] }));
    if (p === "/api/v1/api-keys" && method === "POST") {
      const body = route.request().postDataJSON() as { name: string; scopes: string[]; expires_at: string };
      if (body.scopes.includes("users:write") && !system.confirmed) {
        return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      }
      const k = { id: `0199k${people.nextId++}`, name: body.name, prefix: "linx_0199k9", role: "admin", scopes: body.scopes, allowed_ips: [],
        created_by: "user:u1001", created_at: now(), expires_at: body.expires_at };
      system.keys.push(k);
      return route.fulfill(json({ api_key: k, key: "linx_0199k9_EXAMPLEEXAMPLEEXAMPLE" }, 201));
    }
    if (p === "/api/v1/sso-providers" && method === "GET") return route.fulfill(json({ items: system.providers }));
    if (p === "/api/v1/sso-providers" && method === "POST") {
      if (!system.confirmed) return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      const body = route.request().postDataJSON() as { kind: string; name?: string; client_id: string; shown?: boolean };
      const pr = { id: `0199b${people.nextId++}`, kind: body.kind, name: body.name ?? (body.kind === "microsoft" ? "Microsoft" : "Google"),
        issuer: "https://accounts.google.com", client_id: body.client_id, client_secret_set: true, enabled: true, shown: body.shown ?? true,
        position: system.providers.length, redirect_uri: "https://example.com/api/v1/sso/callback", created_at: now(), updated_at: now(), etag: '"1"' };
      system.providers.push(pr);
      return route.fulfill(json(pr, 201));
    }
    if (p === "/api/v1/inbound-routes" && method === "GET") return route.fulfill(json({ items: lines.dids }));
    if (p === "/api/v1/trunks" && method === "GET") return route.fulfill(json({ items: lines.trunks }));
    if (p === "/api/v1/trunks" && method === "POST") {
      const body = route.request().postDataJSON() as Json;
      const id = `0199e${people.nextId++}`;
      const signsIn = body.kind === "registers_here";
      const t: Json = {
        port: 5061, transport: "tls", media_encryption: "srtp", cert_trust: "public", dial_format: signsIn ? "local" : "e164", codecs: ["alaw", "ulaw"],
        max_calls: 4, unencrypted: false, enabled: body.enabled ?? true, created_at: now(), updated_at: now(), etag: '"1"', host: "",
        ...body, id, username: signsIn ? `trunk-${id}-0000-7000-8000-000000000000` : body.username,
        status: body.enabled === false ? "disabled" : "unknown", status_detail: signsIn ? "Waiting for it to sign in to Linx." : "Linx is signing in to it.",
      };
      delete t.password;
      lines.trunks.push(t);
      return route.fulfill(json({
        ...t, ...(signsIn ? { login: { username: t.username, password: "EXAMPLE-NOT-A-REAL-PASSWORD", server: DOMAIN, port: 5061, transport: "tls",
          settings_text: "Type: SIP trunk that registers" } } : {}),
      }, 201));
    }
    const trunkId = idAfter(p, "/api/v1/trunks/");
    const trunk = trunkId ? lines.trunks.find((t) => t.id === trunkId) : undefined;
    if (trunkId && p === `/api/v1/trunks/${trunkId}` && method === "GET") return route.fulfill(trunk ? json(trunk) : notFound());
    if (trunkId && p === `/api/v1/trunks/${trunkId}` && method === "PATCH") {
      if (!trunk) return route.fulfill(notFound());
      const body = route.request().postDataJSON() as Json;
      delete body.password;
      Object.assign(trunk, body, { updated_at: now(), etag: '"2"' });
      if (body.enabled === true) Object.assign(trunk, { status: "registered", status_detail: "Linx is signed in to it." });
      if (body.enabled === false) Object.assign(trunk, { status: "disabled", status_detail: "It's turned off." });
      return route.fulfill(json(trunk));
    }
    if (trunkId && p === `/api/v1/trunks/${trunkId}` && method === "DELETE") {
      lines.trunks = lines.trunks.filter((t) => t.id !== trunkId);
      lines.dids = lines.dids.filter((d) => d.trunk_id !== trunkId);
      return route.fulfill({ status: 204 });
    }
    if (trunk && p === `/api/v1/trunks/${trunkId}/test` && method === "POST") {
      if (trunk.kind === "registers_here") {
        const ok = trunk.status === "registered";
        return route.fulfill(json({ ok, steps: [{ name: "signed_in", result: ok ? "ok" : "failed",
          words: ok ? "It's signed in to Linx." : "It isn't signed in to Linx. On the phone system, check the server, port 5061, TLS, the username and password." }] }));
      }
      const pinned = trunk.cert_trust === "pinned";
      return route.fulfill(json({
        ok: pinned,
        steps: [
          { name: "address", result: "ok", words: `${String(trunk.host)} is 198.51.100.20.` },
          { name: "connection", result: "ok", words: "Connected to port 5061." },
          pinned ? { name: "certificate", result: "ok", words: "Its certificate is the one you trusted, and names it." }
            : { name: "certificate", result: "failed", words: "Its certificate isn't signed by a company Linx trusts." },
          ...(pinned ? [{ name: "sip", result: "ok", words: "It answers." }, { name: "login", result: "ok", words: "It accepted the login." }] : []),
        ],
        ...(pinned ? {} : {
          untrusted: true,
          certificates: [{ subject: String(trunk.host), issuer: "Example Voice Private CA", names: [trunk.host], not_after: "2027-09-01T00:00:00Z",
            sha256: "3A:9F:12:C4:55:8B:70:1D:E2:6A:99:04:BB:31:7C:0E:5F:A8:21:6D:43:90:1B:77:C8:2E:65:D0:19:AF:84:C2",
            self_signed: false, pem: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n" }],
        }),
      }));
    }
    if (trunk && p === `/api/v1/trunks/${trunkId}/reset-password` && method === "POST") {
      return route.fulfill(json({ ...trunk, login: { username: trunk.username, password: "EXAMPLE-NEW-PASSWORD", server: DOMAIN, port: 5061, transport: "tls", settings_text: "" } }));
    }
    if (trunk && p === `/api/v1/trunks/${trunkId}/dids` && method === "GET") return route.fulfill(json({ items: lines.dids.filter((d) => d.trunk_id === trunkId) }));
    if (trunk && p === `/api/v1/trunks/${trunkId}/dids` && method === "POST") {
      const body = route.request().postDataJSON() as Json;
      const d = { id: `0199f${people.nextId++}`, trunk_id: trunkId, label: "", created_at: now(), updated_at: now(), etag: '"1"', ...body };
      lines.dids.push(d);
      return route.fulfill(json(d, 201));
    }
    const didId = idAfter(p, "/api/v1/dids/");
    if (didId && method === "PATCH") {
      const d = lines.dids.find((x) => x.id === didId);
      if (!d) return route.fulfill(notFound());
      const body = route.request().postDataJSON() as Json;
      if (body.extension_id === "") { delete d.extension_id; delete body.extension_id; }
      Object.assign(d, body, { etag: '"2"' });
      return route.fulfill(json(d));
    }
    if (didId && method === "DELETE") {
      lines.dids = lines.dids.filter((x) => x.id !== didId);
      return route.fulfill({ status: 204 });
    }
    if (p === "/api/v1/outbound-routing" && method === "GET") {
      const ordered = lines.trunks.filter((t) => t.outbound_priority !== undefined).sort((a, b) => Number(a.outbound_priority) - Number(b.outbound_priority));
      return route.fulfill(json({ country: "AE", trunks: ordered, international_alert: lines.alert }));
    }
    if (p === "/api/v1/outbound-routing" && method === "PUT") {
      const body = route.request().postDataJSON() as { order: string[]; international_alert?: { minutes: number; calls: number } };
      for (const t of lines.trunks) delete t.outbound_priority;
      body.order.forEach((id, i) => { const t = lines.trunks.find((x) => x.id === id); if (t) t.outbound_priority = i + 1; });
      if (body.international_alert) lines.alert = body.international_alert;
      const ordered = body.order.map((id) => lines.trunks.find((t) => t.id === id)).filter(Boolean);
      return route.fulfill(json({ country: "AE", trunks: ordered, international_alert: lines.alert }));
    }
    if (p === `/api/v1/call-permission-levels/${String(lines.level.id)}` && method === "GET") return route.fulfill(json(lines.level));
    if (p === `/api/v1/call-permission-levels/${String(lines.level.id)}` && method === "PATCH") {
      const body = route.request().postDataJSON() as { allowed_categories?: string[] };
      const before = lines.level.allowed_categories as string[];
      const costly = (body.allowed_categories ?? []).some((c) => (c === "international" || c === "premium") && !before.includes(c));
      if (costly) return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      Object.assign(lines.level, body, { etag: '"2"' });
      return route.fulfill(json(lines.level));
    }
    if (p === "/api/v1/route-test" && method === "POST") {
      const body = route.request().postDataJSON() as { direction?: string; number: string; from?: string };
      if (body.direction === "inbound") {
        const d = lines.dids.find((x) => x.number === body.number);
        const ext = d?.extension_id ? people.extensions.find((e) => e.id === d.extension_id) : undefined;
        return route.fulfill(json(ext
          ? { category: "landline", kind: "Landline number in the UAE", reason: "routed", extension: { number: ext.number, display_name: ext.display_name },
            words: `Rings extension ${ext.number} (${ext.display_name}).` }
          : { category: "landline", kind: "Landline number in the UAE", reason: "not_assigned", words: "Rings nobody: callers hear the number isn't available." }));
      }
      const n = body.number.replace(/\D/g, "");
      if (n.startsWith("00")) {
        return route.fulfill(json({ category: "international", kind: "International number (United Kingdom)", region: "GB", allowed: false, reason: "not_permitted",
          words: "Not allowed. International number (United Kingdom). Your phones can't call abroad." }));
      }
      if (n === "999") {
        return route.fulfill(json({ category: "emergency", kind: "Emergency (Police)", allowed: true, reason: "emergency",
          lines: [{ trunk: "UCM landlines", number: "999" }], words: "Always allowed: emergency (Police). Goes out on \"UCM landlines\" as 999." }));
      }
      return route.fulfill(json({ category: "mobile", kind: "Mobile number in the UAE", region: "AE", e164: `+971${n.slice(1)}`, allowed: true, reason: "allowed",
        lines: [{ trunk: "UCM landlines", number: n, caller_id: "+97142000100" }, { trunk: "Telnyx", number: `+971${n.slice(1)}`, caller_id: "+97142000101" }],
        words: `Mobile number in the UAE. Your phones can call mobiles.` }));
    }
    if (p === "/api/v1/wireguard-profiles" && method === "GET") return route.fulfill(json({ items: lines.profiles }));
    if (p === "/api/v1/wireguard-profiles" && method === "POST") {
      const body = route.request().postDataJSON() as { name: string };
      const w = { id: `0199a${people.nextId++}`, name: body.name, address: "10.6.0.2/32", public_key: "q1Xg8y2nTTmW1k1TOl0nH1sQzA5Y1bY6tJfPq6JmH2E=",
        peer_public_key: "Zp3X7lqk9q8sXxZKx8r3M5y1v2m0dYk8x9nqQx1kQ0c=", peer_endpoint_host: "vpn.example-voice.com", peer_endpoint_port: 51820,
        status: "connecting", status_detail: "Waiting for the first handshake.", created_at: now(), updated_at: now(), etag: '"1"',
        notes: ["The file's AllowedIPs (0.0.0.0/0) are ignored: only the phone lines' own addresses go through it."] };
      lines.profiles.push(w);
      return route.fulfill(json(w, 201));
    }
    if (p === "/api/v1/calls/active" && method === "GET") {
      return route.fulfill(json({
        phone_engine_connected: true,
        items: [
          { id: "0199c1", direction: "outbound", from: { extension: "1024", name: "Sara Haddad" }, to: "+971501234567", state: "answered", started_at: new Date(Date.now() - 252_000).toISOString() },
          { id: "0199c2", direction: "internal", from: { extension: "1031", name: "Omar Khalil" }, to: "1042", state: "answered", started_at: new Date().toISOString() },
        ],
      }));
    }
    if (p === "/api/v1/numbering/next" && method === "GET") {
      // The first free number in the people range (1111 when the fake's
      // ranges don't say, as the older screens expect).
      const range = (settings.extension_ranges as { kind: string; from: number; to: number }[]).find((x) => x.kind === "people");
      if (!opts.followRanges || !range) return route.fulfill(json({ number: "1111" }));
      let n = range.from;
      while (people.extensions.some((e) => e.number === String(n))) n++;
      return route.fulfill(json({ number: String(n) }));
    }
    if (p === "/api/v1/call-permission-levels" && method === "GET") return route.fulfill(json({ items: opts.setupCompleted ? [lines.level] : [] }));
    // Help (docs/HELP.md): the sign-in guides without a session, more with one.
    if (p === "/api/v1/help/guides") {
      const guides = opts.signedIn ? HELP_GUIDES : HELP_GUIDES.filter((g) => HELP_PUBLIC.includes(g.name));
      return route.fulfill(json({ guides, ...(opts.signedIn && system.answers.enabled ? { answers_by: "Anthropic (Claude)" } : {}) }));
    }
    if (p === "/api/v1/help/answer" && method === "POST") {
      const lines = [...HELP_ANSWER.map((text) => ({ text })),
        { done: true, guides: [{ name: "desk-phones-and-phone-apps", title: "Desk phones and phone apps" }], by: "Anthropic (Claude)" }];
      return route.fulfill({ status: 200, contentType: "application/x-ndjson", body: lines.map((l) => JSON.stringify(l)).join("\n") + "\n" });
    }
    if (p === "/api/v1/email" && method === "GET") return route.fulfill(json(system.email));
    if (p === "/api/v1/email" && method === "PATCH") {
      if (!system.confirmed) {
        return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      }
      const { password, arrived, ...rest } = route.request().postDataJSON() as Json;
      if (rest.host === "mail.home.lan") {
        return route.fulfill(json({ type: "about:blank", title: "Unprocessable Entity", status: 422, code: "host_blocked",
          detail: "mail.home.lan resolves to 192.168.1.30, a private address. Add it to the outbound allowlist if it's a device on your network." }, 422));
      }
      Object.assign(system.email, rest, password !== undefined ? { password_set: password !== "" } : {},
        arrived ? { arrived_at: new Date().toISOString() } : {}, { etag: '"2"' });
      return route.fulfill(json(system.email));
    }
    if (p === "/api/v1/email/test" && method === "POST") {
      const e = system.email as { host: string };
      return route.fulfill(json(opts.email === "failing"
        ? { to: "mohammed@example.com", ok: false, passed: ["connect", "encrypt", "certificate"], stage: "sign_in",
          error: "The mail server didn't accept the user name and password. (535 5.7.8 Username and Password not accepted.)" }
        : { to: "mohammed@example.com", ok: true, passed: ["connect", "encrypt", "certificate", "sign_in", "send"], host: e.host }));
    }
    if (p === "/api/v1/help-answers" && method === "GET") return route.fulfill(json(system.answers));
    if (p === "/api/v1/help-answers" && method === "PATCH") {
      if (!system.confirmed) {
        return route.fulfill(json({ type: "about:blank", title: "Forbidden", status: 403, code: "confirm_required", detail: "Confirm it's you." }, 403));
      }
      const body = route.request().postDataJSON() as Json;
      const { api_key: key, ...rest } = body;
      Object.assign(system.answers, rest, key !== undefined ? { api_key_set: key !== "" } : {}, { etag: '"2"' });
      return route.fulfill(json(system.answers));
    }
    if (p === "/api/v1/help-answers/test" && method === "POST") {
      return route.fulfill(json({ question: "How do I add a desk phone?", ok: true, answer: HELP_ANSWER.join(""), by: "Anthropic (Claude)",
        guides: [{ name: "desk-phones-and-phone-apps", title: "Desk phones and phone apps" }] }));
    }
    if (p.startsWith("/api/v1/help/guides/")) {
      const name = p.slice("/api/v1/help/guides/".length);
      const g = HELP_GUIDES.find((x) => x.name === name && (opts.signedIn || HELP_PUBLIC.includes(x.name)));
      if (!g) return route.fulfill(json({ type: "about:blank", title: "Not Found", status: 404, code: "not_found", detail: "There's no such page in Help." }, 404));
      return route.fulfill(json({ ...g, blocks: helpBlocks(g) }));
    }
    if (p === "/api/v1/help/search") {
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      return route.fulfill(json({ results: q.includes("desk") ? HELP_DESK_RESULTS : [] }));
    }
    if (p.startsWith("/api/v1/help/pictures/")) {
      return route.fulfill({ path: `../docs/help/pictures/${p.slice("/api/v1/help/pictures/".length)}`, contentType: "image/webp" });
    }
    if (p === "/api/v1/sign-in-options") {
      return route.fulfill(json({ company: opts.company ? [GOOGLE] : [], company_sign_in_required: !!opts.companyRequired, passkeys_available: true,
        password_reset: !!opts.email, ...(opts.moved ? { passkeys_moved: true } : {}) }));
    }
    // "Forgot your password?" (ADR-067): the same answer for any email; the
    // reset link "used" is gone, "nostep" is for an account with no second
    // step, any other has an authenticator app and a passkey. The new
    // password is refused at the second step when it's a common one.
    if (p === "/api/v1/password-reset" && method === "POST") return route.fulfill({ status: 202 });
    if (p.startsWith("/api/v1/reset-links/") && method === "GET") {
      if (p.endsWith("/used")) {
        return route.fulfill(json({ type: "about:blank", title: "Bad Request", status: 400, code: "reset_link_invalid", detail: "Used." }, 400));
      }
      return route.fulfill(json({ email: "sara@example.com", methods: p.endsWith("/nostep") ? [] : ["authenticator", "passkey", "recovery_code"] }));
    }
    if (p.startsWith("/api/v1/reset-links/") && method === "POST") {
      const { password, code } = route.request().postDataJSON() as { password: string; code?: string };
      if (password === "password123456") {
        return route.fulfill(json({ type: "about:blank", title: "Bad Request", status: 400, code: "password_invalid",
          detail: "That password is too easy to guess. Choose another." }, 400));
      }
      if (code !== undefined && code !== "123456") {
        return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "mfa_code_invalid", detail: "That code isn't right." }, 401));
      }
      return route.fulfill(json({ status: "signed_in" }));
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
  // The repair page's own endpoints (docs/INSTALL.md §7).
  await page.route("**/repair/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    const method = route.request().method();
    if (!opts.repair) return route.fulfill({ status: 404, body: "" });
    if (p === "/repair/api/state") {
      return route.fulfill(json({ no_sign_in: opts.repair === "no-sign-in", domain: "example.com", problem: serverSettings?.problem,
        expires_at: new Date(Date.now() + 4 * 3600_000).toISOString(), expires_in: 4 * 3600 - 83 }));
    }
    if (opts.repair !== "no-sign-in") return route.fulfill({ status: 404, body: "" });
    if (p === "/repair/api/server-settings" && method === "GET") {
      return route.fulfill(json(serverSettings ? { open: true, settings: serverSettings } : { open: false }));
    }
    if (p === "/repair/api/server-settings/preview" && method === "POST") {
      return route.fulfill(json(settingsPreview(route.request().postDataJSON() as Change)));
    }
    if (p === "/repair/api/server-settings" && method === "POST") {
      return route.fulfill(settingsChange(route.request().postDataJSON() as Change));
    }
    return route.fulfill({ status: 404, body: "" });
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
    add_records: [{ type: "A", name: "example.com", value: "203.0.113.5" }, { type: "A", name: "turn.example.com", value: "203.0.113.5" }],
    dns: { state: "wrong", checked_at: "2026-09-28T08:07:15Z", zone: "example.com", company: "Cloudflare",
      name_servers: ["ada.ns.cloudflare.com", "bob.ns.cloudflare.com"], names: [
      { name: "example.com", state: "wrong", seen: ["198.51.100.7"] }, { name: "turn.example.com", state: "missing" }] },
    prepare: { state: "ok" }, reach: {}, records: {}, certificate: {},
    ...over,
  };
}

const acceptedAnswers = {
  where: "rented", front_door: "linx-443", domain: "example.com", name: "Mohammed AlMudharreb", email: "mohammed@example.com", admin_email: "mohammed@example.com",
  time_zone: "Asia/Dubai", agreed_to_terms: true,
};

export async function fakeInstall(page: Page, where: "rented" | "home", opts: {
  closed?: boolean; expiresIn?: number; cert?: FakeCert; accepted?: Record<string, unknown>;
} = {}) {
  const facts = where === "home"
    ? { where, public_address: "5.36.12.4", lan_address: "192.168.1.212", lan_network: "192.168.1.0/24", time_zone: "Asia/Dubai", hardware: "4 processor cores, 8 GB memory, 62 GB free" }
    : { where, public_address: "203.0.113.5", time_zone: "Etc/UTC", hardware: "4 processor cores, 8 GB memory, 62 GB free" };
  const expiresIn = opts.expiresIn ?? 13632;
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
      cert = fakeCert({ domain: a.domain, add_records: [a.domain, `turn.${a.domain}`].map((name) => ({ type: "A", name, value: facts.public_address })) });
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
      // On the port 443 page too: the page moves to token mode.
      cert = { ...cert, mode: "token", add_records: undefined, token_saved: true, records: { state: "running" } };
      return route.fulfill({ status: 204 });
    }
    return route.fulfill({ status: 404, body: "" });
  });
}

/**
 * The secure page at https://example.com, served from the test's own
 * build (the requests never leave the browser).
 */
export async function fakeSecureInstall(page: Page, base: string, opts: {
  usedHandoff?: boolean; where?: "rented" | "home"; failAt?: number; installed?: boolean;
} = {}) {
  const where = opts.where ?? "rented";
  const facts = where === "home"
    ? { where, public_address: "5.36.12.4", lan_address: "192.168.1.212", lan_network: "192.168.1.0/24" }
    : { where, public_address: "203.0.113.5" };
  const titles = where === "home"
    ? ["Save your settings", "Firewall and phone ports on 192.168.1.212", "Internal certificate authority", "Portainer (home network only)",
      "Download Linx's services", "Certificate for example.com and *.example.com", "Start Linx (this setup page closes)",
      "Your system admin account (mohammed@example.com)", "Phone system and call audio",
      "example.com, turn.example.com at this network's public address; sip.example.com at 192.168.1.212 (for desk phones at home)",
      "Helpers: backups, status, firewall sync", "Finish"]
    : ["Save your settings", "Firewall", "Internal certificate authority", "Download Linx's services", "Certificate (renews through port 443)",
      "Start Linx (this setup page closes)", "Your system admin account (mohammed@example.com)", "Phone system and call audio",
      "Helpers: backups, status, firewall sync", "Finish"];
  const finish: Record<string, unknown> = {
    provider: "cloudflare", skip_allowed: where === "rented", portainer_allowed: where === "home",
    profiles: [
      { name: "lite", description: "audio first, small meetings, AI off, lighter monitoring" },
      { name: "standard", description: "all features, medium-sized meetings" },
      { name: "performance", description: "all features, large meetings, can run AI on this server" },
    ],
    profile_pick: "standard", profile_reason: "4 processor cores and 8 GB memory", install: {},
  };
  const install = (failAt?: number) => {
    const at = failAt ?? 3;
    finish.install = { state: failAt === undefined ? "running" : "failed", detail: failAt === undefined ? undefined : "Download Linx's services: no space left" };
    finish.sign_in_path = "/setup/" + "s".repeat(43);
    finish.steps = titles.map((title, i) => ({
      title, state: i < at ? "ok" : i === at ? (failAt === undefined ? "running" : "failed") : "",
      detail: i === at && failAt !== undefined ? "Download the Linx service images: write /var/lib/docker: no space left on device" : undefined,
    }));
    finish.keep = [{ title: "Certificate authority backup passphrase", value: "K7QM-2XPD-9RTA-LW4E-HB6N-C3VY",
      note: "Linx's internal certificate authority keeps its master key only as a backup locked with this passphrase, in /etc/linx/ca-backup on the server. Write the passphrase down, copy that folder somewhere safe, then delete it from the server." }];
  };
  if (opts.installed) {
    finish.token = "skipped";
    finish.extras = { profile: "", portainer: false };
    install(opts.failAt);
  }
  await page.route("https://example.com/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/install/api/redeem") {
      return opts.usedHandoff
        ? route.fulfill({ status: 409, json: { detail: "That link to the secure page can't be used." } })
        : route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/state") {
      return route.fulfill({ json: { facts, secure: true, cert: fakeCert({ certificate: { state: "ok" } }), accepted: { ...acceptedAnswers, where },
        expires_at: new Date(Date.now() + 14_400_000).toISOString(), expires_in: 14400, connected: true, finish } });
    }
    if (url.pathname === "/install/api/token") {
      const { token } = route.request().postDataJSON() as { token: string };
      if (token.length < 20) {
        return route.fulfill({ status: 422, json: { errors: [{ step: "token", field: "token",
          message: "That token can't see example.com at Cloudflare. It needs Zone → Zone → Read and Zone → DNS → Edit on that zone." }] } });
      }
      finish.token = "saved";
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/skip-token") {
      finish.token = "skipped";
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/extras") {
      finish.extras = route.request().postDataJSON();
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/install/api/install") {
      install(opts.failAt);
      return route.fulfill({ status: 204 });
    }
    const path = url.pathname.startsWith("/install") ? "/" : url.pathname;
    const res = await route.fetch({ url: base + path });
    return route.fulfill({ response: res });
  });
  return {
    /** The full Linx takes over: the switch, then the first sign-in. */
    switchOver(opts: { keepWiped?: boolean } = {}) {
      finish.switching = true;
      // The server wipes what to write down when the install finishes; a
      // later update can come without it (found on the VPS demo).
      if (opts.keepWiped) finish.keep = [];
      (finish.steps as { state: string }[]).forEach((s, i) => { if (i < titles.indexOf("Start Linx (this setup page closes)")) s.state = "ok"; });
    },
  };
}

// Help's stand-in guides: real titles and screens, a short body each.
const HELP_PUBLIC = ["signing-in", "passkeys-and-authenticator", "lost-authenticator", "locked-out", "set-password-link"];
const HELP_GUIDES: { name: string; title: string; section: string; screens: string[] }[] = [
  { name: "whats-new", title: "What's new", section: "whats-new", screens: [] },
  { name: "signing-in", title: "Signing in", section: "everyday", screens: ["/"] },
  { name: "passkeys-and-authenticator", title: "Passkeys and authenticator apps", section: "everyday", screens: [] },
  { name: "lost-authenticator", title: "Lost your authenticator or passkey", section: "everyday", screens: [] },
  { name: "locked-out", title: "Locked out", section: "everyday", screens: [] },
  { name: "set-password-link", title: "Your invite or set-password link", section: "everyday", screens: ["/setup/<link>"] },
  { name: "calls-in-the-browser", title: "Calls in the browser", section: "everyday", screens: ["/"] },
  { name: "team-and-presence", title: "Team and presence", section: "everyday", screens: ["/team"] },
  { name: "using-help", title: "Using Help", section: "everyday", screens: ["/help"] },
  { name: "people-and-invites", title: "People and invite links", section: "admin", screens: ["/admin/people"] },
  { name: "extensions", title: "Extensions", section: "admin", screens: ["/admin/extensions"] },
  { name: "desk-phones-and-phone-apps", title: "Desk phones and phone apps", section: "admin", screens: [] },
  { name: "phone-lines", title: "Phone lines", section: "admin", screens: ["/admin/lines"] },
  { name: "backups", title: "Backups", section: "running", screens: ["/admin/system/backups"] },
  { name: "system-status", title: "System status and Restart", section: "running", screens: ["/admin/system", "/admin/system/status"] },
  { name: "phone-wont-register", title: "A phone won't register", section: "running", screens: [] },
  { name: "install-linx", title: "Installing Linx", section: "install", screens: ["/install", "/install/continue"] },
];

const text = (t: string) => ({ type: "text", text: t });
const bold = (t: string) => ({ type: "bold", text: t });

// A written answer, in the pieces it arrives in.
const HELP_ANSWER = [
  "1. Open Desk phones and press Add a phone.\n",
  "2. Pick the person and the phone's model.\n",
  "3. Scan the code on the next screen with the phone, or type the settings box into it.\n",
  "The phone signs in within a minute and shows as ready.",
];

function helpBlocks(g: { name: string; title: string }): Json[] {
  if (g.name !== "extensions") {
    return [
      { type: "heading", level: 1, text: g.title, anchor: "title" },
      { type: "paragraph", inlines: [text("A short guide in plain words, with the buttons named as they are on the screen, like "), bold("Sign in"), text(".")] },
    ];
  }
  return [
    { type: "heading", level: 1, text: "Extensions", anchor: "extensions" },
    { type: "paragraph", inlines: [text("An extension is a number that rings a phone: for a person, or a shared one like a reception desk.")] },
    { type: "picture", picture: "extensions", alt: "Extensions" },
    { type: "heading", level: 2, text: "Adding one", anchor: "adding-one" },
    { type: "paragraph", inlines: [bold("+ Add"), text(", then:")] },
    { type: "list", items: [
      { inlines: [bold("Quick add"), text(": just a name. It gets the next free number.")] },
      { inlines: [bold("Guide me"), text(": number, name, person and a phone, step by step.")] },
    ] },
    { type: "heading", level: 2, text: "Phones on an extension", anchor: "phones-on-an-extension" },
    { type: "paragraph", inlines: [text("An extension can ring a desk phone or a phone app as well as the browser. Open it and choose "),
      bold("+ Add a desk phone or phone app"), text(". See "), { type: "link", text: "Desk phones and phone apps", guide: "desk-phones-and-phone-apps" }, text(".")] },
    { type: "heading", level: 2, text: "From the server", anchor: "from-the-server" },
    { type: "code", text: "sudo linx doctor" },
  ];
}

const HELP_DESK_RESULTS = [
  { guide: "desk-phones-and-phone-apps", title: "Desk phones and phone apps", heading: "Adding one", anchor: "adding-one",
    lines: ["Open the extension, then + Add a desk phone or phone app.", "Choose the kind: a desk phone, or a phone app on a mobile or computer."] },
  { guide: "extensions", title: "Extensions", heading: "Phones on an extension", anchor: "phones-on-an-extension",
    lines: ["An extension can ring a desk phone or a phone app as well as the browser."] },
  { guide: "phone-wont-register", title: "A phone won't register", lines: ["Check the server, port 5061, TLS, the username and the password on the desk phone."] },
];

const PASS_THROUGH = ["proxy", "pangolin", "nginx"];

/** The front door's steps as setup makes them (web/e2e/door-setup.json, kept in step by a Go test), for domain and proxy. */
export function doorSetup(domain = "example.com", proxy = "192.168.1.20"): typeof doorSetupFixture {
  // "192.168.1.212" (this server) never contains "192.168.1.20" (the proxy).
  const text = JSON.stringify(doorSetupFixture).replaceAll("192.168.1.20", proxy)
    .replaceAll("example-com", domain.replaceAll(".", "-")).replaceAll("example.com", domain);
  return JSON.parse(text);
}
