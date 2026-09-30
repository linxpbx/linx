// The web install's own small API on port 6464 (docs/INSTALL.md §3,
// internal/install): not the Linx API, which doesn't exist yet at this
// point. Every call carries the claimed browser's cookie; a 404 means the
// link or its hour is over.

export type Where = "home" | "rented";
/** "pangolin" and "nginx" are older setups' names for "proxy" (ADR-062). */
export type FrontDoor = "proxy" | "pangolin" | "nginx" | "http-proxy" | "linx-443" | "home-only";
export type Step = "welcome" | "where" | "front_door" | "domain" | "you" | "checked";

export interface Facts {
  public_address?: string;
  lan_address?: string;
  lan_network?: string;
  where: Where;
  port_443?: string;
  /** This server's own clock setting, e.g. Etc/UTC. */
  time_zone?: string;
  hardware?: string;
}

export interface Answers {
  where: Where | "";
  front_door: FrontDoor | "";
  proxy_address?: string;
  turn_udp_port?: number;
  domain: string;
  name: string;
  /** Let's Encrypt's contact for certificate notices. */
  email: string;
  /** Signs in to the first system admin account; needn't be the certificate's. */
  admin_email: string;
  /** The time zone for schedules (backups, office hours), e.g. Asia/Dubai. */
  time_zone: string;
  agreed_to_terms: boolean;
}

export interface FieldError {
  step: Exclude<Step, "welcome" | "checked">;
  field: string;
  message: string;
}

/** What the page keeps on the server so the same browser returns to the same step. */
export interface Draft {
  step: Step;
  answers: Answers;
}

export type StageState = "" | "running" | "ok" | "failed";
export type ProblemKind = "connection" | "dns" | "wrong_answer" | "rate_limited" | "other";

export interface Stage {
  state?: StageState;
  kind?: ProblemKind;
  detail?: string;
  at?: string;
}

export interface SetupFile {
  title: string;
  path?: string;
  text: string;
}

/** The front-door card (docs/ui/SCREENS_PHASE1F.md §1.2): the three facts and "How to do this in…". */
export interface DoorCard {
  routes: { name: string; address: string; proxy_protocol: boolean }[];
  proxy: string;
  pick?: string;
  guides: { id: string; title: string; note?: string; steps: string[]; files?: SetupFile[] }[];
}

export type DNSState = "missing" | "wrong" | "ok" | "error";
export interface DNSRecord { type: string; name: string; value: string }

/** The certificate page (docs/ui/INSTALL_SCREENS.md §2.6–2.7), as the server tells it. */
export interface CertView {
  mode: "port443" | "token";
  domain: string;
  front_door: FrontDoor;
  /** The DNS records to add by hand: the domain itself and turn. */
  add_records?: DNSRecord[];
  setup?: { files?: SetupFile[]; steps?: string[]; card?: DoorCard; done?: boolean };
  dns: {
    state?: DNSState; names?: { name: string; state: DNSState; seen?: string[] }[]; checked_at?: string;
    /** Where the records go, told by the domain's name servers; company "" when Linx doesn't know it. */
    zone?: string; name_servers?: string[]; company?: string;
  };
  prepare: Stage;
  reach: Stage;
  records: Stage;
  certificate: Stage;
  token_saved?: boolean;
  secure_url?: string;
}

/** One row of the install's progress list (§3.5). */
export interface InstallStep {
  title: string;
  state?: StageState;
  detail?: string;
}

/** Something to write down now: shown once, never kept on the server. */
export interface KeepItem {
  title: string;
  value: string;
  note?: string;
}

export interface ProfileOption {
  name: string;
  description: string;
}

/** The secure page's steps (docs/ui/INSTALL_SCREENS.md §3.2–3.5), as the server tells them. */
export interface FinishView {
  /** "" (not answered), saved or skipped. */
  token?: "" | "saved" | "skipped";
  skip_allowed?: boolean;
  provider?: "cloudflare" | "duckdns";
  extras?: { profile: string; portainer: boolean };
  profiles?: ProfileOption[];
  profile_pick?: string;
  profile_reason?: string;
  portainer_allowed?: boolean;
  steps?: InstallStep[];
  install: Stage;
  /** The full Linx is starting in this page's place. */
  switching?: boolean;
  /** /setup/<token>: the first sign-in, ready before the switch. */
  sign_in_path?: string;
  keep?: KeepItem[];
}

export interface InstallState {
  facts: Facts;
  draft?: Draft;
  accepted?: Answers;
  expires_at: string;
  /** Seconds left, by the server's clock. */
  expires_in: number;
  connected: boolean;
  cert?: CertView;
  /** This is the secure page (https://<domain>). */
  secure?: boolean;
  /** The secure page's steps: only ever sent there. */
  finish?: FinishView;
}

export class LinkClosed extends Error {}

const json = { "Content-Type": "application/json" };

export async function getState(): Promise<InstallState> {
  const r = await fetch("/install/api/state", { credentials: "same-origin" });
  if (r.status === 404) throw new LinkClosed();
  if (!r.ok) throw new Error(`state: ${r.status}`);
  return (await r.json()) as InstallState;
}

export async function saveDraft(d: Draft): Promise<void> {
  const r = await fetch("/install/api/draft", { method: "PUT", headers: json, body: JSON.stringify(d), credentials: "same-origin" });
  if (r.status === 404) throw new LinkClosed();
}

export type CheckResult = { ok: true } | { ok: false; errors: FieldError[] } | { ok: false; problem: string };

export async function checkAnswers(a: Answers): Promise<CheckResult> {
  const r = await fetch("/install/api/check", { method: "POST", headers: json, body: JSON.stringify(a), credentials: "same-origin" });
  if (r.status === 404) throw new LinkClosed();
  if (r.ok) return { ok: true };
  const body = (await r.json().catch(() => ({}))) as { errors?: FieldError[]; detail?: string };
  if (r.status === 422 && body.errors) return { ok: false, errors: body.errors };
  return { ok: false, problem: body.detail ?? "Setup on the server couldn't check your answers. Try again." };
}

/** A problem the server explained, in plain words. */
export class Problem extends Error {}

async function post(path: string, body: unknown = {}): Promise<Response> {
  const r = await fetch(path, { method: "POST", headers: json, body: JSON.stringify(body), credentials: "same-origin" });
  if (r.status === 404) throw new LinkClosed();
  if (r.ok) return r;
  const b = (await r.json().catch(() => ({}))) as { errors?: FieldError[]; detail?: string };
  throw new Problem(b.errors?.[0]?.message ?? b.detail ?? "Setup on the server couldn't do that. Try again.");
}

/** "I've done this" for the front door's own steps. */
export async function frontDoorReady(): Promise<void> {
  await post("/install/api/door-ready");
}

/** Run a failed step again. */
export async function retryCertificate(): Promise<void> {
  await post("/install/api/retry");
}

/** The DNS company's token (only when port 443 can't reach Linx, §2.6). */
export async function sendToken(token: string): Promise<void> {
  await post("/install/api/token", { token });
}

/** A new one-time link to the secure page (two minutes). */
export async function newHandoff(): Promise<string> {
  const r = await post("/install/api/handoff");
  return ((await r.json()) as { handoff: string }).handoff;
}

/** Whether this browser can open the secure page yet (its DNS may lag). */
export async function canOpen(secureURL: string): Promise<boolean> {
  try {
    await fetch(`${secureURL}/install/api/ping`, { mode: "no-cors", cache: "no-store" });
    return true;
  } catch {
    return false;
  }
}

/** Secure page: no DNS token (a rented server where Linx takes 443). */
export async function skipToken(): Promise<void> {
  await post("/install/api/skip-token");
}

/** Secure page: the size of server and Portainer. */
export async function saveExtras(extras: { profile: string; portainer: boolean }): Promise<void> {
  await post("/install/api/extras", extras);
}

/** Secure page: install Linx (again, after a failure). */
export async function startInstall(): Promise<void> {
  await post("/install/api/install");
}

/**
 * After the switch: whether the full Linx answers at this address yet
 * (its public sign-in options; the installer's page never has them).
 */
export async function linxAnswers(): Promise<boolean> {
  try {
    const r = await fetch("/api/v1/sign-in-options", { cache: "no-store", credentials: "omit" });
    return r.ok && (r.headers.get("Content-Type") ?? "").includes("json");
  } catch {
    return false;
  }
}

/**
 * Whether the first sign-in's link works yet: true, false (not there, or
 * not yet: the first admin is made just after Linx starts), or undefined
 * (can't tell). Each "no" counts like a failed sign-in, so the caller asks
 * at most every few seconds.
 */
export async function setupLinkReady(path: string): Promise<boolean | undefined> {
  const token = path.replace(/^\/setup\//, "");
  try {
    const r = await fetch(`/api/v1/setup-links/${encodeURIComponent(token)}`, { cache: "no-store", credentials: "omit" });
    if (r.ok) return true;
    if (r.status === 400 || r.status === 404) return false;
    return undefined;
  } catch {
    return undefined;
  }
}

/** On the secure page: swap the handoff for this page's own session. */
export async function redeemHandoff(handoff: string): Promise<void> {
  await post("/install/api/redeem", { handoff });
}

export const emptyAnswers: Answers = { where: "", front_door: "", domain: "", name: "", email: "", admin_email: "", time_zone: "", agreed_to_terms: false };

/** This browser's time zone: where the person setting up is. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** Every time zone this browser knows, UTC first. */
export function timeZones(): string[] {
  let all: string[] = [];
  try {
    all = Intl.supportedValuesOf("timeZone");
  } catch {
    all = [];
  }
  return ["UTC", ...all.filter((z) => z !== "UTC")];
}

/** "47:12", or "3:47:12" past an hour, for a countdown. */
export function mmss(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

/** Front doors that are another program at an address on the home network. */
export const proxyKinds: FrontDoor[] = ["proxy", "pangolin", "nginx", "http-proxy"];

// Plain-words checks next to each field, before anything goes to the
// server (which checks everything again with setup.yaml's own rules).

const DOMAIN = /^(?=.{4,243}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const EMAIL = /^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$/;
const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;

export function domainProblem(raw: string): string {
  const d = raw.trim().toLowerCase();
  if (!d) return "Give your domain, like example.com.";
  if (IPV4.test(d) || /\.\d+$/.test(d)) return "That's an address, not a domain. It should look like example.com.";
  if (!DOMAIN.test(d)) return "That isn't a domain. It should look like example.com.";
  if (d.endsWith(".duckdns.org") && d.split(".").length !== 3) return "DuckDNS names look like yourname.duckdns.org.";
  return "";
}

export function proxyAddressProblem(raw: string): string {
  const a = raw.trim();
  if (!IPV4.test(a) || a.split(".").some((p) => Number(p) > 255)) return `"${a}" isn't an address like 192.168.1.30.`;
  const [x = 0, y = 0] = a.split(".").map(Number);
  if (!(x === 10 || (x === 172 && y >= 16 && y <= 31) || (x === 192 && y === 168))) return `${a} isn't a home-network address.`;
  return "";
}

export function portProblem(p: number): string {
  if (p === 443) return "";
  if (!Number.isInteger(p) || p < 1024 || p > 65535) return "Use 443, or a port from 1024 to 65535 (3478 is the usual one).";
  if (p === 5060 || p === 5061 || p === 5349 || p === 8443 || (p >= 10000 && p <= 10199)) return "Linx already uses that port (3478 is the usual one).";
  return "";
}

export function nameProblem(n: string): string {
  if (!n.trim()) return "Give your name.";
  if ([...n.trim()].length > 100) return "That name is too long.";
  return "";
}

export function emailProblem(e: string): string {
  if (!e.trim()) return "Give an email address.";
  if (!EMAIL.test(e.trim())) return "That doesn't look like an email address.";
  return "";
}
