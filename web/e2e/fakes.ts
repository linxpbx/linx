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
  pending?: "code" | "enroll";
}

export async function fakeServer(page: Page, opts: FakeOptions = {}) {
  if (process.env.LINX_E2E_DEBUG) {
    page.on("console", (m) => console.log(`[page] ${m.type()}: ${m.text()}`));
    page.on("pageerror", (e) => console.log(`[page] uncaught: ${e.stack ?? e.message}`));
  }
  const json = (body: unknown, status = 200) => ({
    status, contentType: status >= 400 ? "application/problem+json" : "application/json", body: JSON.stringify(body),
  });
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const p = url.pathname;
    if (p === "/api/v1/me") {
      if (!opts.signedIn && !opts.pending) {
        return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "unauthenticated", detail: "Sign in." }, 401));
      }
      return route.fulfill(json({
        id: "0199", type: "user", role: "admin", scopes: opts.pending ? [] : ["team:read"], pending: !!opts.pending,
        email: ME.email, name: ME.name, extension: ME.extension, presence: "available", mfa_enabled: opts.pending !== "enroll",
      }));
    }
    if (p === "/api/v1/session" && method === "POST") {
      return route.fulfill(json({ type: "about:blank", title: "Unauthorized", status: 401, code: "sign_in_invalid", detail: "Wrong email or password." }, 401));
    }
    if (p === "/api/v1/me/mfa" && method === "POST") {
      return route.fulfill(json({ secret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", otpauth_url: "otpauth://totp/Linx:mohammed@example.com?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Linx" }));
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
