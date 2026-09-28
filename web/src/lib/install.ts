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

export interface InstallState {
  facts: Facts;
  draft?: Draft;
  accepted?: Answers;
  expires_at: string;
  connected: boolean;
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

export const emptyAnswers: Answers = { where: "", front_door: "", domain: "", name: "", email: "", agreed_to_terms: false };

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
