// linxpbx.com's only server code: TestFlight requests (owner, 2026-10-07).
// Everything else on the site is static files served by Cloudflare.
//
//   POST /api/beta          a tester's request (name, Apple ID email, Turnstile)
//   GET  /api/beta          whether requests are open (the form asks)
//   GET  /beta/review?r=..  the owner's review page: Approve or Reject
//   POST /beta/review       the owner's decision
//
// Nothing is stored. A request is emailed to the owner with one link, and
// the request itself travels inside it, signed (HMAC-SHA256 with
// REVIEW_SECRET) and dated: the link is the only copy, it works for 14 days,
// and only someone holding it can approve. Opening the link only shows the
// request — mail scanners open links — and only the Approve button, a POST,
// acts. Approving adds the person to the TestFlight group through the App
// Store Connect API (a key with the App Manager role), and Apple emails them
// the invitation. Rejecting does nothing, because nothing was kept.

import { EmailMessage } from "cloudflare:email";

interface Env {
  ASSETS: { fetch(req: Request): Promise<Response> };
  MAIL?: { send(message: EmailMessage): Promise<void> };
  OWNER_EMAIL?: string; // where requests go: a verified Email Routing destination
  MAIL_FROM?: string; // an address on linxpbx.com, e.g. testflight@linxpbx.com
  REVIEW_SECRET?: string; // 32+ random bytes, hex or base64
  TURNSTILE_SITE_KEY?: string;
  TURNSTILE_SECRET?: string;
  ASC_KEY_ID?: string;
  ASC_ISSUER_ID?: string;
  ASC_PRIVATE_KEY?: string; // the .p8 file's contents
  ASC_BETA_GROUP_ID?: string; // the external testing group testers join
}

const LINK_DAYS = 14;

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    try {
      if (url.pathname === "/api/beta") {
        if (req.method === "GET") return json({ open: isOpen(env), turnstile_site_key: env.TURNSTILE_SITE_KEY ?? "" });
        if (req.method === "POST") return await request(req, env);
        return new Response(null, { status: 405, headers: { allow: "GET, POST" } });
      }
      if (url.pathname === "/beta/review") {
        if (req.method === "GET") return await reviewPage(url, env);
        if (req.method === "POST") return await decide(req, env);
        return new Response(null, { status: 405, headers: { allow: "GET, POST" } });
      }
    } catch (e) {
      console.error("beta:", e instanceof Error ? e.message : e);
      return page("Something went wrong", "<p>That didn't work. Please try again in a minute.</p>", 500);
    }
    return env.ASSETS.fetch(req);
  },
};

function isOpen(env: Env): boolean {
  return !!(env.MAIL && env.OWNER_EMAIL && env.MAIL_FROM && env.REVIEW_SECRET && env.TURNSTILE_SECRET && env.TURNSTILE_SITE_KEY &&
    env.ASC_KEY_ID && env.ASC_ISSUER_ID && env.ASC_PRIVATE_KEY && env.ASC_BETA_GROUP_ID);
}

// --- A tester asks ------------------------------------------------------------

type Ask = { first: string; last: string; email: string; note: string; at: number };

const EMAIL = /^[^\s@<>"']{1,64}@[^\s@<>"']+\.[^\s@<>"']{2,}$/;

async function request(req: Request, env: Env): Promise<Response> {
  if (!isOpen(env)) return json({ ok: false, message: "Requests aren't open yet. Please try again soon." }, 503);
  if (!(req.headers.get("content-type") ?? "").includes("application/json")) return json({ ok: false, message: "Send JSON." }, 415);
  const body = (await req.json().catch(() => null)) as Record<string, unknown> | null;
  const str = (k: string, max: number) => (typeof body?.[k] === "string" ? (body[k] as string).trim().slice(0, max) : "");
  const ask: Ask = { first: str("first_name", 60), last: str("last_name", 60), email: str("email", 120).toLowerCase(), note: str("note", 300), at: Date.now() };
  if (!ask.first || !EMAIL.test(ask.email)) return json({ ok: false, message: "Please give your first name and the email of your Apple ID." }, 400);
  if (!(await humanCheck(str("turnstile", 2048), req.headers.get("cf-connecting-ip") ?? "", env))) {
    return json({ ok: false, message: "The check that you're a person didn't pass. Please try again." }, 400);
  }
  const token = await seal(ask, env.REVIEW_SECRET!);
  const link = `${new URL(req.url).origin}/beta/review?r=${token}`;
  await mailOwner(env, `TestFlight request: ${ask.first} ${ask.last}`.trim(), [
    `${ask.first} ${ask.last} <${ask.email}> would like to test the Linx app.`,
    ask.note ? `\nThey wrote: ${ask.note}` : "",
    `\nApprove or reject (the link works for ${LINK_DAYS} days): ${link}`,
    "\nApproving adds them to your TestFlight testers; Apple then emails them the invitation.",
  ].join("\n"));
  return json({ ok: true, message: "Thanks! If you're approved, TestFlight will email you an invitation." });
}

async function humanCheck(token: string, ip: string, env: Env): Promise<boolean> {
  if (!token) return false;
  const form = new FormData();
  form.append("secret", env.TURNSTILE_SECRET!);
  form.append("response", token);
  if (ip) form.append("remoteip", ip);
  const r = await fetch("https://challenges.cloudflare.com/turnstile/v0/siteverify", { method: "POST", body: form });
  const out = (await r.json().catch(() => ({}))) as { success?: boolean };
  return out.success === true;
}

// --- The owner decides ----------------------------------------------------------

async function reviewPage(url: URL, env: Env): Promise<Response> {
  if (!env.REVIEW_SECRET) return page("Not open", "<p>TestFlight requests aren't set up on this site.</p>", 503);
  const token = url.searchParams.get("r") ?? "";
  const ask = await open(token, env.REVIEW_SECRET);
  if (!ask) return page("Link not valid", `<p>This link isn't valid or is more than ${LINK_DAYS} days old.</p>`, 400);
  const who = `${esc(ask.first)} ${esc(ask.last)}`.trim();
  return page("TestFlight request", `
    <p><strong>${who}</strong> &lt;${esc(ask.email)}&gt; asked to test the Linx app on ${new Date(ask.at).toUTCString()}.</p>
    ${ask.note ? `<blockquote>${esc(ask.note)}</blockquote>` : ""}
    <form method="post" action="/beta/review">
      <input type="hidden" name="r" value="${esc(token)}">
      <button name="decision" value="approve" class="approve">Approve</button>
      <button name="decision" value="reject" class="reject">Reject</button>
    </form>
    <p class="small">Approving adds them to your TestFlight testers and Apple emails them the invitation. Rejecting sends nothing; nothing about the request was kept.</p>`);
}

async function decide(req: Request, env: Env): Promise<Response> {
  if (!isOpen(env)) return page("Not open", "<p>TestFlight requests aren't set up on this site.</p>", 503);
  // A form post from this site only: a page elsewhere can't press Approve.
  const origin = req.headers.get("origin");
  if (origin && origin !== new URL(req.url).origin) return page("Not allowed", "<p>That came from another site.</p>", 403);
  const form = await req.formData();
  const token = String(form.get("r") ?? "");
  const ask = await open(token, env.REVIEW_SECRET!);
  if (!ask) return page("Link not valid", `<p>This link isn't valid or is more than ${LINK_DAYS} days old.</p>`, 400);
  const who = `${esc(ask.first)} ${esc(ask.last)}`.trim();
  if (form.get("decision") !== "approve") {
    return page("Rejected", `<p>${who}'s request is rejected. Nothing was sent to them, and nothing about it was kept.</p>`);
  }
  const result = await addTester(ask, env);
  if (!result.ok) {
    return page("Apple refused", `<p>App Store Connect didn't add ${who}: ${esc(result.message)}</p><p class="small">You can press Back and try again, or add them by hand in App Store Connect → TestFlight.</p>`, 502);
  }
  return page("Approved", `<p>${who} &lt;${esc(ask.email)}&gt; ${result.already ? "was already a tester and is" : "is"} now in your TestFlight testing group. Apple emails them the invitation.</p>`);
}

// --- Signed requests ------------------------------------------------------------

const enc = new TextEncoder();
const b64url = (b: ArrayBuffer | Uint8Array) =>
  btoa(String.fromCharCode(...new Uint8Array(b as ArrayBuffer))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const unb64url = (s: string) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));

async function hmacKey(secret: string): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign", "verify"]);
}

async function seal(ask: Ask, secret: string): Promise<string> {
  const payload = b64url(enc.encode(JSON.stringify(ask)));
  const sig = await crypto.subtle.sign("HMAC", await hmacKey(secret), enc.encode(payload));
  return `${payload}.${b64url(sig)}`;
}

async function open(token: string, secret: string): Promise<Ask | null> {
  const [payload, sig] = token.split(".");
  if (!payload || !sig || token.length > 4096) return null;
  let good = false;
  try {
    good = await crypto.subtle.verify("HMAC", await hmacKey(secret), unb64url(sig), enc.encode(payload));
  } catch {
    return null;
  }
  if (!good) return null;
  const ask = JSON.parse(new TextDecoder().decode(unb64url(payload))) as Ask;
  if (typeof ask.at !== "number" || Date.now() - ask.at > LINK_DAYS * 86_400_000) return null;
  return ask;
}

// --- App Store Connect ----------------------------------------------------------

const ASC = "https://api.appstoreconnect.apple.com";

async function ascToken(env: Env): Promise<string> {
  const pem = env.ASC_PRIVATE_KEY!.replace(/-----[^-]+-----/g, "").replace(/\s+/g, "");
  const key = await crypto.subtle.importKey("pkcs8", unb64url(pem.replace(/\+/g, "-").replace(/\//g, "_")),
    { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
  const now = Math.floor(Date.now() / 1000);
  const head = b64url(enc.encode(JSON.stringify({ alg: "ES256", kid: env.ASC_KEY_ID, typ: "JWT" })));
  const body = b64url(enc.encode(JSON.stringify({ iss: env.ASC_ISSUER_ID, iat: now, exp: now + 600, aud: "appstoreconnect-v1" })));
  // WebCrypto's ECDSA signature is already r||s, which is what ES256 wants.
  const sig = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, enc.encode(`${head}.${body}`));
  return `${head}.${body}.${b64url(sig)}`;
}

async function addTester(ask: Ask, env: Env): Promise<{ ok: boolean; already?: boolean; message: string }> {
  const auth = { authorization: `Bearer ${await ascToken(env)}`, "content-type": "application/json" };
  const group = { type: "betaGroups", id: env.ASC_BETA_GROUP_ID! };
  const created = await fetch(`${ASC}/v1/betaTesters`, {
    method: "POST", headers: auth,
    body: JSON.stringify({ data: { type: "betaTesters",
      attributes: { email: ask.email, firstName: ask.first, lastName: ask.last || undefined },
      relationships: { betaGroups: { data: [group] } } } }),
  });
  if (created.ok) return { ok: true, message: "" };
  if (created.status !== 409) return { ok: false, message: await ascProblem(created) };
  // Already a tester of this team: put them in the group too.
  const found = await fetch(`${ASC}/v1/betaTesters?filter[email]=${encodeURIComponent(ask.email)}&limit=1`, { headers: auth });
  const id = ((await found.json().catch(() => ({}))) as { data?: { id: string }[] }).data?.[0]?.id;
  if (!id) return { ok: false, message: "they seem to be a tester already, but Apple didn't find them" };
  const added = await fetch(`${ASC}/v1/betaGroups/${encodeURIComponent(group.id)}/relationships/betaTesters`, {
    method: "POST", headers: auth, body: JSON.stringify({ data: [{ type: "betaTesters", id }] }),
  });
  return added.ok ? { ok: true, already: true, message: "" } : { ok: false, message: await ascProblem(added) };
}

async function ascProblem(r: Response): Promise<string> {
  const j = (await r.json().catch(() => ({}))) as { errors?: { title?: string; detail?: string }[] };
  const e = j.errors?.[0];
  return e ? `${e.title ?? ""}${e.detail ? ` — ${e.detail}` : ""} (HTTP ${r.status})` : `HTTP ${r.status}`;
}

// --- Mail and pages -------------------------------------------------------------

async function mailOwner(env: Env, subject: string, text: string): Promise<void> {
  const id = `${crypto.randomUUID()}@linxpbx.com`;
  const raw = [
    `From: Linx website <${env.MAIL_FROM}>`,
    `To: <${env.OWNER_EMAIL}>`,
    `Subject: ${subject.replace(/[\r\n]+/g, " ")}`,
    `Message-ID: <${id}>`,
    `Date: ${new Date().toUTCString()}`,
    "MIME-Version: 1.0",
    "Content-Type: text/plain; charset=utf-8",
    "Content-Transfer-Encoding: 8bit",
    "",
    text,
  ].join("\r\n");
  await env.MAIL!.send(new EmailMessage(env.MAIL_FROM!, env.OWNER_EMAIL!, raw));
}

function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", "cache-control": "no-store" } });
}

function page(title: string, html: string, status = 200): Response {
  return new Response(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>${esc(title)} · Linx</title><style>
:root{--cobalt:#1F5FD6;--ink:#17191E;--muted:#4A4F57;--bg:#F4F3EF;--card:#fff;--line:#E2E0D9;--end:#C53030}
@media (prefers-color-scheme:dark){:root{--ink:#F2F3F5;--muted:#B6BBC3;--bg:#0f1216;--card:#161a20;--line:#262b33;--cobalt:#7FB0FF}}
body{margin:0;background:var(--bg);color:var(--ink);font:17px/1.6 system-ui,sans-serif}
main{max-width:560px;margin:10vh auto;padding:28px;background:var(--card);border:1px solid var(--line);border-radius:16px}
h1{font-size:1.6rem;margin:0 0 .6rem}p{color:var(--muted)}.small{font-size:.9rem}
blockquote{margin:1rem 0;padding:.5rem 1rem;border-left:3px solid var(--cobalt)}
form{display:flex;gap:.8rem;margin:1.4rem 0}button{font:600 1rem system-ui;padding:.65em 1.4em;border-radius:999px;border:0;cursor:pointer}
.approve{background:var(--cobalt);color:#fff}.reject{background:transparent;color:var(--end);border:1.5px solid var(--end)}
</style></head><body><main><h1>${esc(title)}</h1>${html}</main></body></html>`,
    { status, headers: { "content-type": "text/html; charset=utf-8", "cache-control": "no-store", "x-frame-options": "DENY", "referrer-policy": "no-referrer" } });
}
