// The browser call suite (docs/WEB.md §8 step 5), against the real stack
// that internal/browsertest starts: two people sign in with their
// set-password links in two separate browsers, see each other on the Team
// list, call each other and hear each other. Omar's browser can't use UDP
// for calls (Chromium's "disable_non_proxied_udp" policy, as on a network
// that blocks UDP), so his audio must go through Linx's relay over TLS.
// Then calls to and from the SIPp softphone on 103, the Opus echo test, and
// signing out dropping the phone line at once. Separately, an admin sets up
// their account with a passkey (Chromium's virtual authenticator) and signs
// back in with it, no email or password.
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
  // An ordinary person choosing a password only gets no warning box.
  await page.getByRole("radio", { name: /Password only/ }).click();
  await page.getByRole("button", { name: "Continue" }).click();
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

// Check it from "outside" (docs/SIMPLER.md §2.3): the phone link the
// harness made, opened with UDP blocked, so the relay test has to go over
// TLS through the front door. The harness then checks what the admin's
// side saw. First, so the link (10 minutes) is still fresh.
test("check it: the phone link", async () => {
  const url = process.env.LINX_REACH_URL ?? "";
  test.skip(!url, "no phone link from the harness");
  const b = await launch(["--force-webrtc-ip-handling-policy=disable_non_proxied_udp"]);
  const page = await (await b.newContext()).newPage();
  await page.goto(url);
  await expect(page.getByRole("heading", { name: /You reached Linx at / })).toBeVisible();
  await expect(page.getByText(/Linx saw you coming from \d+\.\d+\.\d+\.\d+\./)).toBeVisible();
  await expect(page.getByText("Calls from here will have audio.")).toBeVisible();
  await b.close();
});

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

  // Help comes inside the image (docs/HELP.md): signed out, Omar reads only
  // the sign-in guides; Aisha reads hers, pictures and search included, and
  // gets the same 404 as a missing guide for an admin one.
  await omar.getByRole("link", { name: "Help signing in" }).click();
  await expect(omar.getByRole("link", { name: "Lost your authenticator or passkey" })).toBeVisible();
  await expect(omar.getByRole("link", { name: "Team and presence" })).toHaveCount(0);
  await aisha.goto("/help");
  await aisha.getByLabel("Search the guides").fill("who is on a call right now?");
  await expect(aisha.getByRole("link", { name: /^Team and presence/ }).first()).toBeVisible();
  await aisha.goto("/help/team-and-presence");
  const picture = aisha.locator("article img").first();
  await expect.poll(() => picture.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
  const [adminGuide, missing] = await aisha.evaluate(() => Promise.all(["extensions", "no-such-guide"].map(async (name) => {
    const r = await fetch(`/api/v1/help/guides/${name}`);
    return `${r.status} ${await r.text()}`;
  })));
  expect(adminGuide).toMatch(/^404 /);
  expect(adminGuide).toBe(missing);

  await a.close();
  await b.close();
});

test("an admin sets up with a passkey and signs in with it", async () => {
  const browser = await launch();
  const context = await browser.newContext({ baseURL: base });
  const page = await context.newPage();
  // A step that can't happen fails in 30 s with its name, not at the
  // suite's timeout.
  page.setDefaultTimeout(30_000);
  page.on("console", (m) => { if (m.type() === "error") console.log(`[admin] ${m.text()}`); });
  page.on("response", (r) => { if (r.status() >= 400) console.log(`[admin] ${r.request().method()} ${r.url()} ${r.status()}`); });
  const cdp = await context.newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: {
      protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true,
      isUserVerified: true, automaticPresenceSimulation: true,
    },
  });
  await page.goto(`/setup/${process.env.LINX_SETUP_ADMIN ?? ""}`);
  await expect(page.getByRole("radio", { name: /Passkey/ })).toBeChecked();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Create passkey" }).click();
  await expect(page.getByLabel("Name this passkey")).not.toBeEmpty();
  await page.getByRole("button", { name: "Save passkey" }).click();
  await expect(page.getByRole("list", { name: "Recovery codes" }).getByRole("listitem")).toHaveCount(10);
  await page.getByRole("checkbox", { name: "I've saved these codes" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Skip" }).click();
  // Setup isn't finished on this install, so an admin's setup link lands
  // in the setup wizard (docs/ui/ADMIN_SCREENS_PHASE1E.md §3.1).
  await expect(page).toHaveURL(/\/setup$/, { timeout: 30_000 });
  // At its first screen, against a real server's fresh database (it once
  // skipped ahead, and hid "Restore from a backup", on every real install).
  await expect(page.getByRole("heading", { name: /How do you want to start\?|Where will you use Linx\?/ })).toBeVisible();
  await page.goto("/");
  await expect(page.getByTestId("account-menu")).toBeVisible({ timeout: 30_000 });

  await page.goto("/account");
  await expect(page.getByRole("list", { name: "Your passkeys" }).getByRole("listitem")).toHaveCount(1);

  // Resolves when the page has signed in with the passkey (the answer's POST).
  const passkeySignIn = () => page.waitForResponse((r) =>
    new URL(r.url()).pathname === "/api/v1/session/passkey" && r.request().method() === "POST");
  const signedInWithPasskey = async (answered: ReturnType<typeof passkeySignIn>) => {
    expect((await answered).status()).toBe(200);
    await expect(page.getByTestId("account-menu")).toBeVisible({ timeout: 30_000 });
    const me = await page.evaluate(async () => (await fetch("/api/v1/me")).json() as Promise<{ role: string; pending: boolean; passkeys: number }>);
    expect(me).toMatchObject({ role: "admin", pending: false, passkeys: 1 });
  };
  const signOut = async () => {
    await page.getByTestId("account-menu").click();
    await page.getByRole("menuitem", { name: "Sign out" }).click();
  };

  // The email box's autofill: the virtual authenticator picks the passkey
  // by itself, as a person would from the browser's suggestion.
  let answered = passkeySignIn();
  await signOut();
  await signedInWithPasskey(answered);

  // The button, in a browser without autofill for passkeys.
  // In this page too, not only after the reload: otherwise the sign-in
  // page shown by signing out starts autofill, the virtual authenticator
  // answers it, and it can sign straight back in before the reload (CI,
  // 2026-09-30: the reload then came back signed in, with no button).
  const noAutofill = () => { PublicKeyCredential.isConditionalMediationAvailable = async () => false; };
  await page.addInitScript(noAutofill);
  await page.evaluate(noAutofill);
  await signOut();
  await expect(page.getByRole("button", { name: "Sign in with a passkey" })).toBeVisible();
  await page.reload();
  answered = passkeySignIn();
  await page.getByRole("button", { name: "Sign in with a passkey" }).click();
  await signedInWithPasskey(answered);
  await browser.close();
});
