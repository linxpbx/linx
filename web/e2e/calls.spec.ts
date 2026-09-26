// The browser call suite (docs/WEB.md §8 step 5), against the real stack
// that internal/browsertest starts: two people sign in with their
// set-password links in two separate browsers, see each other on the Team
// list, call each other and hear each other. Omar's browser can't use UDP
// for calls (Chromium's "disable_non_proxied_udp" policy, as on a network
// that blocks UDP), so his audio must go through Linx's relay over TLS.
// Then calls to and from the SIPp softphone on 103, the Opus echo test, and
// signing out dropping the phone line at once.
import { chromium, expect, test, type Browser, type Page } from "@playwright/test";

const base = process.env.LINX_BASE_URL ?? "";
const spki = process.env.LINX_TEST_SPKI ?? "";
const PASSWORD = "correct horse battery staple";

async function launch(extraArgs: string[] = []): Promise<Browser> {
  return chromium.launch({
    args: [
      "--use-fake-ui-for-media-stream",
      "--use-fake-device-for-media-stream",
      // Trust exactly the test certificate's key (HTTPS and TURN over TLS).
      `--ignore-certificate-errors-spki-list=${spki}`,
      ...extraArgs,
    ],
  });
}

const pages: Page[] = [];

async function signIn(browser: Browser, token: string): Promise<Page> {
  const context = await browser.newContext({ baseURL: base, permissions: ["microphone"] });
  // Keep every RTCPeerConnection the page makes, to explain a failure.
  await context.addInitScript(() => {
    const Original = window.RTCPeerConnection;
    const all: RTCPeerConnection[] = [];
    (window as unknown as { __pcs: RTCPeerConnection[] }).__pcs = all;
    window.RTCPeerConnection = new Proxy(Original, {
      construct(target, args: ConstructorParameters<typeof RTCPeerConnection>) {
        const pc = new target(...args);
        all.push(pc);
        return pc;
      },
    });
  });
  const page = await context.newPage();
  pages.push(page);
  page.on("console", (m) => {
    if (m.type() === "error") console.log(`[${token.slice(0, 4)}] ${m.text()}`);
  });
  await page.goto(`/setup/${token}`);
  await page.getByLabel("New password").fill(PASSWORD);
  await page.getByLabel("Type it again").fill(PASSWORD);
  await page.getByRole("button", { name: "Save password" }).click();
  await expect(page.getByTestId("account-menu")).toContainText("Available", { timeout: 30_000 });
  return page;
}

/** Waits until the call panel reports more audio received than before. */
async function hearsAudio(page: Page) {
  const conn = page.getByTestId("connection");
  const first = Number(await conn.getAttribute("data-audio-in"));
  await expect.poll(async () => Number(await conn.getAttribute("data-audio-in")), { timeout: 20_000 })
    .toBeGreaterThan(first + 2000);
}

/** Every connection's ICE state, candidates and pairs (printed on failure). */
async function iceReport(page: Page): Promise<string> {
  return page.evaluate(async () => {
    const out: string[] = [];
    for (const pc of (window as unknown as { __pcs: RTCPeerConnection[] }).__pcs ?? []) {
      out.push(`pc ice=${pc.iceConnectionState} conn=${pc.connectionState} sig=${pc.signalingState}`);
      const stats = await pc.getStats();
      stats.forEach((r) => {
        if (r.type === "local-candidate" || r.type === "remote-candidate") {
          out.push(`  ${r.type} ${r.candidateType} ${r.protocol} ${r.address}:${r.port} relayProtocol=${r.relayProtocol ?? ""}`);
        }
        if (r.type === "candidate-pair") {
          out.push(`  pair ${r.state} nominated=${r.nominated} ${stats.get(r.localCandidateId)?.candidateType}->${stats.get(r.remoteCandidateId)?.address} req=${r.requestsSent} resp=${r.responsesReceived}`);
        }
      });
    }
    return out.join("\n");
  }).catch((e) => String(e));
}

test.afterEach(async ({}, info) => {
  if (info.status === info.expectedStatus) return;
  for (const [i, p] of pages.entries()) console.log(`--- browser ${i} ICE:\n${await iceReport(p)}`);
});

/** The relay candidates this browser gets with its own relay credentials. */
async function relayCandidates(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    const r = await fetch("/api/v1/me/turn-credentials");
    const t = (await r.json()) as { urls: string[]; username: string; credential: string };
    const pc = new RTCPeerConnection({ iceServers: [{ urls: t.urls, username: t.username, credential: t.credential }] });
    pc.addTransceiver("audio");
    const found: string[] = [];
    pc.onicecandidate = (e) => {
      if (e.candidate) found.push(`${e.candidate.type}/${e.candidate.protocol}/${(e.candidate as RTCIceCandidate & { relayProtocol?: string }).relayProtocol ?? ""}`);
    };
    pc.onicecandidateerror = (e) => found.push(`error ${e.url} ${e.errorCode} ${e.errorText}`);
    await pc.setLocalDescription(await pc.createOffer());
    await new Promise<void>((done) => {
      pc.onicegatheringstatechange = () => pc.iceGatheringState === "complete" && done();
      setTimeout(done, 15_000);
    });
    pc.close();
    return found;
  });
}

test("browsers call each other, one with UDP blocked", async () => {
  const a = await launch();
  const b = await launch(["--force-webrtc-ip-handling-policy=disable_non_proxied_udp"]);
  const aisha = await signIn(a, process.env.LINX_SETUP_A ?? "");
  const omar = await signIn(b, process.env.LINX_SETUP_B ?? "");

  // Both reach Linx's relay: Aisha over UDP, Omar (no UDP) only over TLS.
  const aishaRelay = await relayCandidates(aisha);
  const omarRelay = await relayCandidates(omar);
  console.log("relay candidates:", JSON.stringify({ aisha: aishaRelay, omar: omarRelay }));
  // (type/protocol/relayProtocol: relayProtocol is how the browser reaches
  // the relay; Chromium leaves it empty for UDP.)
  expect(aishaRelay.some((c) => c.startsWith("relay/") && !c.endsWith("/tls") && !c.endsWith("/tcp"))).toBe(true);
  expect(omarRelay.length).toBeGreaterThan(0);
  expect(omarRelay.every((c) => c.startsWith("relay/") && c.endsWith("/tls"))).toBe(true);

  // The Team list is live: Aisha sees Omar available once his line is up.
  await aisha.goto("/team");
  await expect(aisha.getByTestId("account-menu")).toContainText("Available", { timeout: 30_000 });
  await expect(aisha.getByTestId("team-102")).toHaveAttribute("data-status", "available");

  // Aisha calls Omar; he answers.
  await aisha.getByRole("button", { name: "Call Omar Khalil" }).click();
  await expect(omar.getByTestId("incoming-call")).toBeVisible();
  await expect(omar.getByTestId("incoming-call")).toContainText("Aisha Rahman");
  await expect(aisha.getByTestId("team-102")).toHaveAttribute("data-status", "ringing");
  await omar.getByRole("button", { name: "Answer" }).click();
  for (const p of [aisha, omar]) {
    await expect(p.getByTestId("call-panel")).toHaveAttribute("data-phase", "active");
  }
  // Omar has no UDP: relayed, over TLS. Both hear each other.
  await expect(omar.getByTestId("connection")).toHaveAttribute("data-mode", "relayed");
  await expect(omar.getByTestId("connection")).toHaveAttribute("data-relay-protocol", "tls");
  await expect(omar.getByTestId("connection")).toContainText("Relayed");
  await expect(aisha.getByTestId("connection")).toHaveAttribute("data-mode", /relayed|direct/);
  await hearsAudio(aisha);
  await hearsAudio(omar);
  await expect(aisha.getByTestId("team-102")).toHaveAttribute("data-status", "on_call");

  // Mute, keypad tones and hang up.
  await omar.getByRole("button", { name: "Mute" }).click();
  await expect(omar.getByRole("button", { name: "Unmute" })).toHaveAttribute("aria-pressed", "true");
  await aisha.getByRole("button", { name: "Keypad" }).click();
  await aisha.getByRole("button", { name: "5" }).click();
  await aisha.keyboard.press("Escape");
  await aisha.getByRole("button", { name: "End call" }).click();
  for (const p of [aisha, omar]) await expect(p.getByTestId("call-panel")).toHaveCount(0);

  // A browser calls the softphone on 103 (SIPp over TLS on the LAN side).
  await aisha.goto("/");
  await expect(aisha.getByTestId("account-menu")).toContainText("Available", { timeout: 30_000 });
  await aisha.getByLabel("Name, extension or number").fill("103");
  await aisha.getByRole("button", { name: "Call", exact: true }).click();
  await expect(aisha.getByTestId("call-panel")).toHaveAttribute("data-phase", "active");
  await aisha.getByRole("button", { name: "End call" }).click();
  await expect(aisha.getByTestId("call-panel")).toHaveCount(0);

  // And the other way: the softphone calls Aisha (internal/browsertest starts
  // that call when it reads this line), she answers, and it hangs up after
  // a few seconds.
  console.log("LINX-TEST: softphone, call 101 now");
  await expect(aisha.getByTestId("incoming-call")).toBeVisible({ timeout: 30_000 });
  await expect(aisha.getByTestId("incoming-call")).toContainText("Desk softphone");
  await aisha.getByRole("button", { name: "Answer" }).click();
  await expect(aisha.getByTestId("call-panel")).toHaveAttribute("data-phase", "active");
  await expect(aisha.getByTestId("call-panel")).toHaveCount(0, { timeout: 30_000 });

  // The Phase 1 exit test: Omar, with UDP blocked, calls a mobile number
  // out through the phone-line provider trunk (internal/browsertest's
  // linx-browser-test-provider, a SIPp registration provider over TLS with
  // SRTP) and it answers — proving a call still gets out and back when a
  // browser's own network blocks UDP, not just calls between browsers.
  await omar.goto("/");
  await expect(omar.getByTestId("account-menu")).toContainText("Available", { timeout: 30_000 });
  await omar.getByLabel("Name, extension or number").fill("0501234567");
  await omar.getByRole("button", { name: "Call", exact: true }).click();
  await expect(omar.getByTestId("call-panel")).toHaveAttribute("data-phase", "active", { timeout: 30_000 });
  await expect(omar.getByTestId("connection")).toHaveAttribute("data-relay-protocol", "tls");
  await omar.getByRole("button", { name: "End call" }).click();
  await expect(omar.getByTestId("call-panel")).toHaveCount(0);

  // The echo test, in Opus: Omar (relayed over TLS) hears himself back.
  await omar.goto("/settings");
  await expect(omar.getByTestId("account-menu")).toContainText("Available", { timeout: 30_000 });
  await omar.getByRole("button", { name: "Test sound" }).click();
  await expect(omar.getByTestId("call-panel")).toHaveAttribute("data-phase", "active");
  await hearsAudio(omar);
  await omar.getByRole("button", { name: "End call" }).click();

  // Signing out drops Omar's line at once: Aisha sees him offline.
  await aisha.goto("/team");
  await expect(aisha.getByTestId("team-102")).toHaveAttribute("data-status", "available");
  await omar.getByTestId("account-menu").click();
  await omar.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(omar.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(aisha.getByTestId("team-102")).toHaveAttribute("data-status", "offline");

  await a.close();
  await b.close();
});
