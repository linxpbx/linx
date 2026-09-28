// The web install's own small API on port 6464 (docs/INSTALL.md §3,
// internal/install): not the Linx API, which doesn't exist yet at this
// point. Every call carries the claimed browser's cookie; a 404 means the
// link or its hour is over.

export type Where = "home" | "rented";
export type FrontDoor = "pangolin" | "nginx" | "http-proxy" | "linx-443" | "home-only";
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
  email: string;
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

export type DNSState = "missing" | "wrong" | "ok" | "error";
export interface DNSRecord { type: string; name: string; value: string }

/** The certificate page (docs/ui/INSTALL_SCREENS.md §2.6–2.7), as the server tells it. */
export interface CertView {
  mode: "port443" | "token";
  domain: string;
  front_door: FrontDoor;
  /** The DNS records to add by hand: meet. and turn. */
  add_records?: DNSRecord[];
  setup?: { files?: SetupFile[]; steps?: string[]; done?: boolean };
  dns: { state?: DNSState; names?: { name: string; state: DNSState; seen?: string[] }[]; checked_at?: string };
  prepare: Stage;
  reach: Stage;
  records: Stage;
  certificate: Stage;
  token_saved?: boolean;
  secure_url?: string;
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
  /** This is the secure page (https://meet.<domain>). */
  secure?: boolean;
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

/** On the secure page: swap the handoff for this page's own session. */
export async function redeemHandoff(handoff: string): Promise<void> {
  await post("/install/api/redeem", { handoff });
}

export const emptyAnswers: Answers = { where: "", front_door: "", domain: "", name: "", email: "", time_zone: "", agreed_to_terms: false };

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

/** "47:12" for a countdown. */
export function mmss(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** Front doors that are another program at an address on the home network. */
export const proxyKinds: FrontDoor[] = ["pangolin", "nginx", "http-proxy"];

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
  if (!e.trim()) return "Give your email address.";
  if (!EMAIL.test(e.trim())) return "That doesn't look like an email address.";
  return "";
}
