// Screenshots of every Phase 1C screen against a stand-in server
// (e2e/fakes.ts), for comparison with docs/ui (the front-end rule in
// CLAUDE.md). `npm run screens` writes them to e2e/screenshots/.
import { expect, test, type Page } from "@playwright/test";
import { fakeServer } from "./fakes";

// The account row says "Available" once the phone line has signed in.
const lineReady = (page: Page) => expect(page.getByTestId("account-menu")).toContainText("Available");

const shot = (page: Page, name: string) =>
  page.screenshot({ path: `e2e/screenshots/${name}.png`, animations: "disabled", caret: "hide", timeout: 15_000 });

for (const scheme of ["light", "dark"] as const) {
  test.describe(scheme, () => {
    test.use({ colorScheme: scheme });

    test("sign-in", async ({ page }) => {
      await fakeServer(page);
      await page.goto("/");
      await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
      await shot(page, `${scheme}-signin`);
      await page.getByLabel("Email").fill("mohammed@example.com");
      await page.getByLabel("Password").fill("wrong password here");
      await page.getByRole("button", { name: "Sign in", exact: true }).click();
      await expect(page.getByRole("alert")).toHaveText("Wrong email or password.");
      await shot(page, `${scheme}-signin-error`);
    });

    test("authenticator code", async ({ page }) => {
      await fakeServer(page, { pending: "code" });
      await page.goto("/");
      await expect(page.getByRole("heading", { name: "Enter your code" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Use a passkey instead" })).toBeVisible();
      await shot(page, `${scheme}-signin-code`);
    });

    test("passkey as the second step", async ({ page }) => {
      await fakeServer(page, { pending: "passkey" });
      await page.goto("/");
      await expect(page.getByRole("heading", { name: "Use your passkey" })).toBeVisible();
      await shot(page, `${scheme}-signin-passkey-step`);
    });

    test("authenticator code: used, timed out, start over", async ({ page }) => {
      await fakeServer(page, { pending: "code" });
      await page.goto("/");
      await page.getByLabel("6-digit code").fill("111111");
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("alert")).toHaveText("That code was already used. Wait for the next one in your app.");
      await page.getByLabel("6-digit code").fill("222222");
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
      await expect(page.getByRole("status")).toHaveText("Your sign-in timed out. Enter your password again.");
      await shot(page, `${scheme}-signin-timed-out`);

      await page.goto("/");
      await page.getByRole("button", { name: "Start over" }).click();
      await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
      await expect(page.getByRole("status")).toHaveCount(0);
    });

    test("first sign-in", async ({ page }) => {
      await fakeServer(page);
      await page.goto("/setup/abc123");
      await expect(page.getByRole("heading", { name: "Welcome to Linx" })).toBeVisible();
      await expect(page.getByRole("radio", { name: /Passkey/ })).toBeChecked();
      await shot(page, `${scheme}-setup-choose`);

      await page.getByRole("radio", { name: /Password only/ }).click();
      await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled();
      await shot(page, `${scheme}-setup-password-only`);
      await page.getByRole("checkbox", { name: "I understand, use a password only" }).click();
      await expect(page.getByRole("button", { name: "Continue" })).toBeEnabled();

      await page.getByRole("radio", { name: /authenticator app/ }).click();
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { name: "Choose a password" })).toBeVisible();
      await page.getByLabel("New password").fill("correct horse");
      await shot(page, `${scheme}-setup-password`);
      await page.getByRole("button", { name: "Choose another way" }).click();
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { name: "Create your passkey" })).toBeVisible();
      await shot(page, `${scheme}-setup-passkey`);
    });

    test("used setup link", async ({ page }) => {
      await fakeServer(page);
      await page.goto("/setup/used");
      await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
      await expect(page.getByLabel("New password")).toHaveCount(0);
      await shot(page, `${scheme}-setup-link-used`);
    });

    test("second step setup", async ({ page }) => {
      await fakeServer(page, { pending: "enroll" });
      await page.goto("/");
      await expect(page.getByRole("heading", { name: "Add a second way to sign in" })).toBeVisible();
      await shot(page, `${scheme}-setup-second-step`);
      await page.getByRole("radio", { name: /authenticator app/ }).click();
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByAltText("QR code for your authenticator app")).toBeVisible();
      await shot(page, `${scheme}-setup-mfa`);
    });

    test("my account", async ({ page }) => {
      await fakeServer(page, { signedIn: true });
      await page.goto("/account");
      await expect(page.getByRole("list", { name: "Your passkeys" })).toContainText("Mohammed's iPhone");
      await shot(page, `${scheme}-account`);
      await page.getByRole("button", { name: "Remove" }).first().click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await shot(page, `${scheme}-account-remove-passkey`);
    });

    test("team, dialer, settings, calls", async ({ page }) => {
      const sip = await fakeServer(page, { signedIn: true });
      await page.goto("/team");
      await sip.registered;
      await expect(page.getByTestId("team-1042")).toBeVisible();
      await shot(page, `${scheme}-team`);

      await page.goto("/");
      await lineReady(page);
      await expect(page.getByRole("button", { name: "Call", exact: true })).toBeVisible();
      await shot(page, `${scheme}-dialer`);

      await page.getByLabel("Search people or dial a number").fill("Aisha");
      await shot(page, `${scheme}-search`);
      await page.getByRole("button", { name: /Aisha Rahman/ }).first().click();
      await expect(page.getByTestId("call-panel")).toHaveAttribute("data-phase", "calling");
      await shot(page, `${scheme}-calling`);
      await sip.answer();
      await expect(page.getByTestId("call-panel")).toHaveAttribute("data-phase", "active");
      await expect(page.getByTestId("connection")).toHaveAttribute("data-mode", "direct", { timeout: 10_000 });
      await shot(page, `${scheme}-active-call`);
      await page.getByRole("button", { name: "End call" }).click();
      await expect(page.getByTestId("call-panel")).toHaveCount(0);

      await sip.ring("Sara Haddad", "1024");
      await expect(page.getByTestId("incoming-call")).toBeVisible();
      await expect(page).toHaveTitle("Ringing: Sara Haddad · Linx");
      await shot(page, `${scheme}-incoming`);
      await page.getByRole("button", { name: "Decline" }).click();

      await page.goto("/settings");
      await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
      await shot(page, `${scheme}-settings`);
    });
  });
}

test.describe("phone width", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("sign-in and my account", async ({ page }) => {
    await fakeServer(page);
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Sign in with a passkey" })).toBeVisible();
    await shot(page, "phone-signin");
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await fakeServer(page, { signedIn: true });
    await page.goto("/account");
    await expect(page.getByRole("list", { name: "Your passkeys" })).toBeVisible();
    await shot(page, "phone-account");
  });

  test("team and a call", async ({ page }) => {
    const sip = await fakeServer(page, { signedIn: true });
    await page.goto("/team");
    await lineReady(page);
    await expect(page.getByTestId("team-1042")).toBeVisible();
    await shot(page, "phone-team");
    await page.getByRole("button", { name: "Call Aisha Rahman" }).click();
    await sip.answer();
    await expect(page.getByTestId("call-panel")).toHaveAttribute("data-phase", "active");
    await shot(page, "phone-active-call");
  });
});
