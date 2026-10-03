// Screenshots of every Phase 1C screen against a stand-in server
// (e2e/fakes.ts), for comparison with docs/ui (the front-end rule in
// CLAUDE.md). `npm run screens` writes them to e2e/screenshots/.
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { doorSetup, fakeCert, fakeInstall, fakeSecureInstall, fakeServer, fakeToken } from "./fakes";

// The account row says "Available" once the phone line has signed in.
const lineReady = (page: Page) => expect(page.getByTestId("account-menu")).toContainText("Available");

// No page ever scrolls sideways, at any width (owner rule, 2026-09-27): long
// text wraps inside its column instead. Every screenshot checks the page and
// everything on it that could scroll sideways.
async function noSidewaysScroll(page: Page, name: string) {
  const offenders = await page.evaluate(() => {
    const out: string[] = [];
    const root = document.scrollingElement;
    if (root && root.scrollWidth > root.clientWidth + 1) out.push(`the page (${root.scrollWidth}px in ${root.clientWidth}px)`);
    for (const el of Array.from(document.querySelectorAll<HTMLElement>("body *"))) {
      const x = getComputedStyle(el).overflowX;
      if ((x === "auto" || x === "scroll") && el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 1) {
        const label = el.getAttribute("aria-label") ?? el.dataset.slot ?? el.tagName.toLowerCase();
        out.push(`${label} (${el.scrollWidth}px in ${el.clientWidth}px): ${(el.textContent ?? "").trim().slice(0, 60)}`);
      }
    }
    return out;
  });
  expect(offenders, `${name} scrolls sideways`).toEqual([]);
}

// The shots the help guides show (`![…](screen:<name>)` in docs/help) are
// also saved small, as WebP, into docs/help/pictures, which the
// control-plane image carries (docs/HELP.md §2). A picture is saved again
// only when the screen really changed, not when just a clock or an "ago" on
// it moved, so re-running make screens leaves git alone.
const guidePictures = new Set(
  readdirSync("../docs/help").filter((f) => f.endsWith(".md"))
    .flatMap((f) => [...readFileSync(`../docs/help/${f}`, "utf8").matchAll(/\]\(screen:([a-z0-9-]+)\)/g)].map((m) => m[1])),
);
// The share of pixels that must differ before a picture is saved again.
const PICTURE_CHANGED = 0.002;

async function savePicture(page: Page, name: string, png: Buffer) {
  const file = `../docs/help/pictures/${name}.webp`;
  const before = existsSync(file) ? readFileSync(file).toString("base64") : "";
  const { webp, changed } = await page.evaluate(async ([b64, old]) => {
    const load = async (src: string) => {
      const img = new Image();
      img.src = src;
      await img.decode();
      const canvas = document.createElement("canvas");
      canvas.width = img.naturalWidth;
      canvas.height = img.naturalHeight;
      const ctx = canvas.getContext("2d", { willReadFrequently: true })!;
      ctx.drawImage(img, 0, 0);
      return { canvas, pixels: ctx.getImageData(0, 0, canvas.width, canvas.height).data };
    };
    const now = await load(`data:image/png;base64,${b64}`);
    const webp = now.canvas.toDataURL("image/webp", 0.8).split(",")[1] ?? "";
    if (!old) return { webp, changed: 1 };
    const was = await load(`data:image/webp;base64,${old}`);
    if (was.canvas.width !== now.canvas.width || was.canvas.height !== now.canvas.height) return { webp, changed: 1 };
    let differ = 0;
    for (let i = 0; i < now.pixels.length; i += 4) {
      if (Math.abs(now.pixels[i]! - was.pixels[i]!) > 48 || Math.abs(now.pixels[i + 1]! - was.pixels[i + 1]!) > 48
        || Math.abs(now.pixels[i + 2]! - was.pixels[i + 2]!) > 48) differ++;
    }
    return { webp, changed: differ / (now.pixels.length / 4) };
  }, [png.toString("base64"), before] as const);
  if (process.env.LINX_E2E_DEBUG) console.log(`picture ${name}: ${(changed * 100).toFixed(3)}% changed`);
  if (changed < PICTURE_CHANGED) return;
  mkdirSync("../docs/help/pictures", { recursive: true });
  writeFileSync(file, Buffer.from(webp, "base64"));
}

// Every page has the Light / Dark / Match device switch (owner, 2026-09-30).
async function hasThemeSwitch(page: Page, name: string) {
  expect(await page.locator('[aria-label="Appearance"]').count(), `${name} has no Appearance switch`).toBeGreaterThan(0);
}

const shot = async (page: Page, name: string) => {
  await noSidewaysScroll(page, name);
  await hasThemeSwitch(page, name);
  const png = await page.screenshot({ path: `e2e/screenshots/${name}.png`, animations: "disabled", caret: "hide", timeout: 15_000 });
  const picture = name.replace(/^(light|dark)-/, "");
  if (picture !== name && guidePictures.has(picture)) await savePicture(page, name, png);
};

test.describe("appearance", () => {
  test.use({ colorScheme: "light" });

  test("Light / Dark / Match this device, kept in this browser", async ({ page }) => {
    await fakeServer(page);
    await page.goto("/");
    const html = page.locator("html");
    await expect(html).toHaveAttribute("data-theme", "light");
    await page.getByRole("button", { name: "Appearance" }).click();
    await expect(page.getByRole("menuitemradio", { name: "Match this device" })).toBeChecked();
    await shot(page, "appearance-menu");
    await page.getByRole("menuitemradio", { name: "Dark" }).click();
    await expect(html).toHaveAttribute("data-theme", "dark");
    await page.reload();
    await expect(html).toHaveAttribute("data-theme", "dark");
    await shot(page, "appearance-dark-on-light-device");
    await page.getByRole("button", { name: "Appearance" }).click();
    await page.getByRole("menuitemradio", { name: "Match this device" }).click();
    await expect(html).toHaveAttribute("data-theme", "light");
    await page.emulateMedia({ colorScheme: "dark" });
    await expect(html).toHaveAttribute("data-theme", "dark");
  });

  test("signed in: the switch is in the top bar", async ({ page }) => {
    await fakeServer(page, { signedIn: true });
    await page.goto("/");
    await page.locator("header").getByRole("button", { name: "Appearance" }).click();
    await page.getByRole("menuitemradio", { name: "Dark" }).click();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await shot(page, "appearance-signed-in-dark");
  });
});

for (const scheme of ["light", "dark"] as const) {
  test.describe(scheme, () => {
    test.use({ colorScheme: scheme });

    test("system status: check it", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, reach: "reached" });
      await page.goto("/admin/system/status");
      const card = page.locator("section", { has: page.getByRole("heading", { name: "Reachable from outside" }) });
      await card.getByRole("button", { name: "Check it" }).click();
      await expect(card.getByText("Calls from outside will have audio.")).toBeVisible();
      await card.scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-status-check-it`);
    });

    test("server settings: dns records", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
      await page.goto("/admin/system/server");
      const card = page.getByRole("region", { name: "Records for example.com" });
      await expect(card.getByText("(so: Porkbun)", { exact: false })).toBeVisible();
      await card.scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-server-dns`);
    });

    test("server settings: dns kept right automatically", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
      await page.goto("/admin/system/server");
      const card = page.getByRole("region", { name: "Records for example.com" });
      await expect(card.getByText("Linx updates these at Porkbun for you, even when your address changes.")).toBeVisible();
      await expect(card.getByText("(94.200.1.9 → 94.200.1.10)", { exact: false })).toBeVisible();
      await card.scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-server-dns-automatic`);
    });

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

    test("forgot your password", async ({ page }) => {
      await fakeServer(page, { email: "on" });
      await page.goto("/");
      await page.getByLabel("Email").fill("sara@example.com");
      await page.getByLabel("Password").fill("wrong password here");
      await page.getByRole("button", { name: "Sign in", exact: true }).click();
      await expect(page.getByRole("alert")).toHaveText("Wrong email or password. Forgot your password?");
      await shot(page, `${scheme}-sign-in-forgot-link`);
      await page.getByRole("alert").getByRole("button", { name: "Forgot your password?" }).click();
      await expect(page.getByRole("heading", { name: "Forgot your password?" })).toBeVisible();
      await shot(page, `${scheme}-sign-in-forgot`);
      await page.getByLabel("Email").fill("sara@example.com");
      await page.getByRole("button", { name: "Send the link" }).click();
      await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
      await shot(page, `${scheme}-sign-in-forgot-sent`);
      await page.getByRole("button", { name: "← Back to sign-in" }).click();
      await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
    });

    test("no forgot link without email", async ({ page }) => {
      await fakeServer(page);
      await page.goto("/");
      await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Forgot your password?" })).toHaveCount(0);
    });

    test("reset link", async ({ page }) => {
      await fakeServer(page, { email: "on" });
      await page.goto("/reset/abc123");
      await expect(page.getByRole("heading", { name: "Choose a new password" })).toBeVisible();
      await expect(page.getByText("sara@example.com")).toBeVisible();
      await page.getByLabel("New password", { exact: true }).fill("password123456");
      await page.getByLabel("Type it again").fill("password123456");
      await shot(page, `${scheme}-sign-in-forgot-new-password`);
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { name: "Enter your code" })).toBeVisible();
      await expect(page.getByText("To finish, confirm with your authenticator app or passkey.", { exact: false })).toBeVisible();
      await expect(page.getByText("Lost your authenticator app, passkey and recovery codes?")).toBeVisible();
      await shot(page, `${scheme}-sign-in-forgot-second-step`);
      await page.getByRole("button", { name: "Ask my admin to reset it" }).click();
      await expect(page.getByText("Your admin has been asked.")).toBeVisible();
      await shot(page, `${scheme}-sign-in-forgot-asked`);
      // A common password is refused only once the second step is sent:
      // back to choosing one, with why.
      await page.getByLabel("6-digit code").fill("123456");
      await expect(page.getByRole("alert")).toHaveText("That password is too easy to guess. Choose another.");
      await page.getByLabel("New password", { exact: true }).fill("a much better passphrase");
      await page.getByLabel("Type it again").fill("a much better passphrase");
      await page.getByRole("button", { name: "Continue" }).click();
      await page.getByLabel("6-digit code").fill("000000");
      await expect(page.getByRole("alert")).toHaveText("That code isn't right.");
      await page.getByLabel("6-digit code").fill("123456");
      await expect(page.getByRole("heading", { name: "Your password is changed" })).toBeVisible();
      await expect(page.getByText("You've been signed out everywhere else.")).toBeVisible();
      await expect(page).toHaveURL(/\/$/);
      await shot(page, `${scheme}-sign-in-forgot-done`);
    });

    test("reset link, used", async ({ page }) => {
      await fakeServer(page, { email: "on" });
      await page.goto("/reset/used");
      await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
      await expect(page.getByRole("alert")).toHaveText("This link has already been used or is older than 30 minutes.");
      await shot(page, `${scheme}-sign-in-forgot-link-used`);
      await page.getByRole("button", { name: "Send a new one" }).click();
      await expect(page.getByRole("heading", { name: "Forgot your password?" })).toBeVisible();
      await expect(page).toHaveURL(/\/$/);
    });

    test("reset link, no second step", async ({ page }) => {
      await fakeServer(page, { email: "on" });
      await page.goto("/reset/nostep");
      await page.getByLabel("New password", { exact: true }).fill("a much better passphrase");
      await page.getByLabel("Type it again").fill("a much better passphrase");
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { name: "Your password is changed" })).toBeVisible();
    });

    test("company sign-in", async ({ page }) => {
      await fakeServer(page, { company: true });
      await page.goto("/");
      await expect(page.getByRole("button", { name: "Continue with Google" })).toBeVisible();
      await shot(page, `${scheme}-signin-company`);
      await page.getByRole("button", { name: "Continue with Google" }).click();
      await expect(page.getByRole("alert")).toHaveText("No Linx account uses this company account. Ask your admin to add you.");
      await expect(page).toHaveURL(/\/$/);
      await shot(page, `${scheme}-signin-company-error`);
    });

    test("company sign-in required", async ({ page }) => {
      await fakeServer(page, { company: true, companyRequired: true });
      await page.goto("/");
      await expect(page.getByRole("button", { name: "Continue with Google" })).toBeVisible();
      await expect(page.getByLabel("Email")).toHaveCount(0);
      await shot(page, `${scheme}-signin-company-required`);
      await page.getByRole("button", { name: "Sign in as a system admin" }).click();
      await expect(page.getByLabel("Email")).toBeVisible();
    });

    test("authenticator code", async ({ page }) => {
      await fakeServer(page, { pending: "code" });
      await page.goto("/");
      await expect(page.getByRole("heading", { name: "Enter your code" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Use a passkey instead" })).toBeVisible();
      await shot(page, `${scheme}-signin-code`);
      // Lost every second step: the right password lets them ask the admins.
      await page.getByRole("button", { name: "Ask my admin to reset it" }).click();
      await expect(page.getByText("sign in again with your password and set up a new one")).toBeVisible();
      await shot(page, `${scheme}-signin-code-asked`);
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
      // The sixth digit sends the code by itself; a refused code clears the
      // boxes and puts the cursor back in them.
      await page.getByLabel("6-digit code").fill("111111");
      await expect(page.getByRole("alert")).toHaveText("That code was already used. Wait for the next one in your app.");
      await expect(page.getByLabel("6-digit code")).toHaveValue("");
      await expect(page.getByLabel("6-digit code")).toBeFocused();
      await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled();
      await shot(page, `${scheme}-signin-code-refused`);
      await page.keyboard.type("22222");
      await expect(page.getByRole("heading", { name: "Enter your code" })).toBeVisible();
      await page.keyboard.type("2");
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

    // Setting up an iPhone or iPad (docs/PHASE2.md §4): my own, and an
    // admin setting one up for someone else.
    test("my phones, and setting one up", async ({ page }) => {
      await fakeServer(page, { signedIn: true });
      await page.goto("/account");
      const phones = page.getByRole("region", { name: "My phones" });
      await expect(phones).toContainText("My iPhone");
      await shot(page, `${scheme}-account-my-phones`);
      await phones.getByRole("button", { name: "Add phone" }).click();
      await expect(page.getByRole("dialog")).toContainText("Add a phone");
      await shot(page, `${scheme}-add-phone`);
      await page.getByRole("button", { name: "Show a QR code" }).click();
      await expect(page.getByAltText("The setup code as a picture to scan")).toBeVisible();
      await expect(page.getByText("Waiting for the phone…")).toBeVisible();
      await shot(page, `${scheme}-add-phone-code`);
      await page.getByRole("button", { name: "Cancel" }).click();
    });

    test("an admin sets up someone's phone, and emails the link", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, email: "on" });
      await page.goto("/admin/people");
      await page.getByRole("row", { name: /Sara Haddad/ }).click();
      await expect(page.getByRole("heading", { name: "Sara Haddad" })).toBeVisible();
      await expect(page.getByText("Sara's iPad")).toBeVisible();
      await expect(page.getByText("Set it up again")).toBeVisible();
      await shot(page, `${scheme}-person-phones`);
      await page.getByRole("button", { name: "+ Add phone" }).click();
      await page.getByRole("radio", { name: "iPad" }).click();
      await page.getByRole("button", { name: "Email a link to Sara" }).click();
      await expect(page.getByText("Emailed to sara@example.com.")).toBeVisible();
      await shot(page, `${scheme}-add-phone-emailed`);
      await page.getByRole("button", { name: "Cancel" }).click();
    });

    // The page an emailed link opens on the phone itself.
    test("the setup page an emailed link opens", async ({ page }) => {
      await fakeServer(page);
      const soon = Math.floor((Date.now() + 9 * 60_000) / 1000);
      const token = fakeToken(soon);
      await page.goto(`/set-up-phone#${token}`);
      await expect(page.getByRole("heading", { name: "Set up this phone" })).toBeVisible();
      await expect(page.getByAltText("The setup code as a picture to scan")).toBeVisible();
      await shot(page, `${scheme}-set-up-phone`);
    });

    test("my account", async ({ page }) => {
      await fakeServer(page, { signedIn: true, company: true });
      await page.goto("/account?company=linked");
      await expect(page.getByRole("list", { name: "Your passkeys" })).toContainText("Mohammed's iPhone");
      await expect(page.getByRole("list", { name: "Company accounts" })).toContainText("Google: mohammed@example.com");
      await expect(page.getByRole("button", { name: "Link Microsoft" })).toBeVisible();
      await expect(page.getByRole("status")).toHaveText("Linked.");
      await expect(page.getByText("Safari on iPhone")).toBeVisible();
      await shot(page, `${scheme}-account`);
      // Change email: it's what you sign in with, so "confirm it's you".
      const emailBox = page.getByRole("region", { name: "Email" });
      await emailBox.getByRole("button", { name: "Change", exact: true }).click();
      await page.getByLabel("New email").fill("mohammed@newcompany.example");
      await expect(page.getByText("Your company account is unlinked")).toBeVisible();
      await shot(page, `${scheme}-account-change-email`);
      await emailBox.getByRole("button", { name: "Save" }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await page.keyboard.press("Escape");
      await emailBox.getByRole("button", { name: "Cancel" }).click();
      // Change password: the current one is checked before the new one is asked.
      await page.getByRole("region", { name: "Password" }).getByRole("button", { name: "Change", exact: true }).click();
      await expect(page.getByLabel("New password")).toHaveCount(0);
      await page.getByLabel("Current password").fill("a wrong guess here");
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("alert")).toHaveText("Your current password is incorrect.");
      await page.getByLabel("Current password").fill("correct horse battery");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("New password").fill("a brand new passphrase");
      await page.getByLabel("Type it again").fill("a brand new passfrase");
      await expect(page.getByText("The two passwords don't match yet.")).toBeVisible();
      await expect(page.getByRole("button", { name: "Change password" })).toBeDisabled();
      await shot(page, `${scheme}-account-change-password`);
      await page.getByRole("button", { name: "Cancel" }).click();
      await page.getByRole("button", { name: "Remove" }).first().click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await shot(page, `${scheme}-account-remove-passkey`);
      // Removing needs "confirm it's you": a passkey, password + code, or
      // the linked company account.
      await page.getByRole("dialog").getByRole("button", { name: "Remove" }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Continue with Google" })).toBeVisible();
      await shot(page, `${scheme}-confirm-identity`);
    });

    test("help: the ? button, a guide, the guides, search", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupStep: 5 });
      await page.goto("/admin/extensions");
      await expect(page.getByRole("heading", { name: "Extensions" })).toBeVisible();
      // The ? button opens the guide about the page you're on.
      await page.getByRole("button", { name: "Help for this page" }).click();
      await expect(page).toHaveURL(/\/help\/extensions$/);
      await expect(page.getByRole("heading", { name: "Adding one" })).toBeVisible();
      const picture = page.getByRole("img", { name: "Extensions" });
      await picture.scrollIntoViewIfNeeded();
      await expect.poll(() => picture.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
      await page.getByRole("link", { name: "All guides" }).scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-help-guide`);
      await page.getByRole("link", { name: "All guides" }).click();
      await expect(page.getByRole("heading", { name: "Help", exact: true })).toBeVisible();
      await expect(page.getByRole("link", { name: "Installing Linx" })).toBeVisible();
      await shot(page, `${scheme}-help`);
      await page.getByLabel("Search the guides").fill("how do I add a desk phone?");
      await expect(page.getByRole("link", { name: "Desk phones and phone apps › Adding one" })).toBeVisible();
      await shot(page, `${scheme}-help-search`);
      await page.getByLabel("Search the guides").fill("xyzzy");
      await expect(page.getByText("Nothing in the guides matches")).toBeVisible();
    });

    test("help: a written answer", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupStep: 5, answers: true });
      await page.goto("/help");
      await page.getByLabel("Search the guides").fill("how do I add a desk phone?");
      await page.getByRole("button", { name: "Write an answer" }).click();
      const answer = page.getByRole("region", { name: "Written answer" });
      await expect(answer.getByText("The phone signs in within a minute and shows as ready.")).toBeVisible();
      await expect(answer.getByRole("link", { name: "Desk phones and phone apps" })).toBeVisible();
      await expect(answer.getByText("Answers are written by Anthropic (Claude) from Linx's guides.")).toBeVisible();
      await expect(page.getByRole("link", { name: "Desk phones and phone apps › Adding one" })).toBeVisible();
      await shot(page, `${scheme}-help-answer`);
    });

    test("help signing in, without a session", async ({ page }) => {
      await fakeServer(page);
      await page.goto("/");
      await page.getByRole("link", { name: "Help signing in" }).click();
      await expect(page.getByRole("heading", { name: "Help signing in" })).toBeVisible();
      await expect(page.getByRole("link", { name: "Lost your authenticator or passkey" })).toBeVisible();
      await expect(page.getByRole("link", { name: "Extensions" })).toHaveCount(0);
      await shot(page, `${scheme}-help-signed-out`);
      await page.getByRole("link", { name: "Back to sign in" }).click();
      await expect(page.getByRole("button", { name: "Sign in", exact: true })).toBeVisible();
    });

    test("admin home", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupStep: 5 });
      await page.goto("/admin");
      await expect(page.getByRole("heading", { name: "Getting started" })).toBeVisible();
      await expect(page.getByText("Line \"Telnyx\" is down")).toBeVisible();
      await shot(page, `${scheme}-admin-home`);
    });

    test("system backups", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, backups: true, download: "ready" });
      await page.goto("/admin/system/backups");
      await expect(page.getByRole("heading", { name: "Backups", exact: true })).toBeVisible();
      await expect(page.getByText("Your backup file is ready")).toBeVisible();
      await shot(page, `${scheme}-system-backups`);
      await page.getByRole("button", { name: "Show its password" }).click();
      await expect(page.getByText("The file's password")).toBeVisible();
      await page.getByText("The file's password").scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-backups-password`);
    });

    test("system status", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, phoneSystemDown: true });
      await page.goto("/admin/system");
      await expect(page.getByRole("heading", { name: "Linx services" })).toBeVisible();
      await expect(page.getByText("Running, not answering")).toBeVisible();
      await shot(page, `${scheme}-system-status`);
      await page.getByRole("button", { name: /Phone system/ }).click();
      await expect(page.getByRole("log", { name: "Phone system log" })).toContainText("Asterisk Ready.");
      await shot(page, `${scheme}-system-status-log`);
    });

    test("people list and detail", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true });
      await page.goto("/admin/people");
      await expect(page.getByRole("heading", { name: "People" })).toBeVisible();
      await expect(page.getByRole("cell", { name: "Invited" })).toBeVisible();
      await shot(page, `${scheme}-people`);
      await page.getByRole("row", { name: /Sara Haddad/ }).click();
      await expect(page.getByRole("heading", { name: "Sara Haddad" })).toBeVisible();
      await shot(page, `${scheme}-people-detail`);
      await page.getByRole("button", { name: "Edit" }).click();
      await page.getByLabel("Email", { exact: true }).fill("sara.h@example.com");
      await expect(page.getByText("They'll sign in with the new email from now on.")).toBeVisible();
      await shot(page, `${scheme}-people-edit-email`);
      await page.getByRole("button", { name: "Cancel" }).click();
      await page.getByRole("button", { name: "Close" }).click();

      await page.getByRole("button", { name: "+ Add" }).click();
      await expect(page.getByRole("heading", { name: "Add a person" })).toBeVisible();
      await shot(page, `${scheme}-people-add-chooser`);
      await page.getByRole("button", { name: /Guide me/ }).click();
      await expect(page.getByText("1 Name")).toBeVisible();
      await page.getByLabel("Name").fill("Priya Menon");
      await page.getByLabel("Email").fill("priya@example.com");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText("Send this to Priya Menon")).toBeVisible();
      await shot(page, `${scheme}-people-add-guided-done`);
      // Reporter isn't offered; Phone is, as in the setup wizard.
      await page.getByRole("button", { name: "Done" }).click();
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await page.getByLabel("Name").fill("Front door");
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("radio", { name: /Reporter/ })).toHaveCount(0);
      await page.getByRole("radio", { name: /Phone/ }).click();
      await shot(page, `${scheme}-people-add-type`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText("(Front door) is ready", { exact: false })).toBeVisible();
    });

    test("people: invites by email", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, email: "on" });
      await page.goto("/admin/people");
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await page.getByLabel("Name").fill("Priya Menon");
      await page.getByLabel("Email").fill("priya@example.com");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByLabel("Email this link to priya@example.com")).toBeChecked();
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText("Emailed to priya@example.com")).toBeVisible();
      await expect(page.getByText("Or send this to Priya Menon yourself.", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-people-add-emailed`);
      await page.getByRole("button", { name: "Done" }).click();
      // A new link for someone, emailed too.
      await page.getByRole("row", { name: /Sara Haddad/ }).click();
      await expect(page.getByLabel(/Email this link to/)).toBeChecked();
      await page.getByRole("button", { name: /invite link/ }).click();
      await expect(page.getByText(/Emailed to/)).toBeVisible();
    });

    test("people: email not set up", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true });
      await page.goto("/admin/people");
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Quick add/ }).click();
      await expect(page.getByLabel(/Email this link to/)).toBeDisabled();
      await expect(page.getByText("Set up email first (System → Settings).")).toBeVisible();
    });

    test("extensions list and detail", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true });
      await page.goto("/admin/extensions");
      await expect(page.getByRole("heading", { name: "Extensions" })).toBeVisible();
      await expect(page.getByRole("cell", { name: "Desk phone" })).toBeVisible();
      await shot(page, `${scheme}-extensions`);
      await page.getByRole("row", { name: /Reception/ }).click();
      await expect(page.getByRole("heading", { name: "Extension 1110" })).toBeVisible();
      await shot(page, `${scheme}-extensions-detail`);
      await page.getByRole("button", { name: "+ Add a desk phone or phone app" }).click();
      await page.getByRole("radio", { name: "Desk phone" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Name").fill("Front desk phone");
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText('Settings for "Front desk phone"')).toBeVisible();
      await shot(page, `${scheme}-extensions-add-device`);
    });

    test("phone lines: list, a phone system signs in, a company's certificate, detail", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/lines");
      await expect(page.getByRole("heading", { name: "Phone lines" })).toBeVisible();
      await expect(page.getByRole("cell", { name: "Not encrypted" })).toBeVisible();
      await shot(page, `${scheme}-lines`);

      // Guided: a phone system or gateway, which signs in to Linx.
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await expect(page.getByRole("radio", { name: /Another phone system or gateway/ })).toBeChecked();
      await shot(page, `${scheme}-lines-add-who`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Name").fill("Branch GXW");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "+ Add a number" }).click();
      await page.getByLabel("Number 1", { exact: true }).fill("+971 4 200 0110");
      await shot(page, `${scheme}-lines-add-numbers`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText('Enter this on "Branch GXW"')).toBeVisible();
      await expect(page.getByText("Waiting for it to sign in…")).toBeVisible();
      await shot(page, `${scheme}-lines-add-login`);
      await page.getByRole("button", { name: "Done" }).click();

      // Guided: a phone company, from what it sent; its certificate isn't
      // from a company Linx knows, so the test offers to trust it.
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await page.getByRole("radio", { name: /An internet phone company/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Or paste what the company sent you").fill("SIP server: sip.example-voice.com\nUsername: linx01\nPassword: s3cret-Pw\nYour DID: +971 4 200 0120");
      await expect(page.getByText("Linx understood:")).toBeVisible();
      await shot(page, `${scheme}-lines-add-paste`);
      await page.getByRole("button", { name: "Use these" }).click();
      await expect(page.getByLabel("Server")).toHaveValue("sip.example-voice.com");
      await page.getByRole("button", { name: "Save and test" }).click();
      await expect(page.getByText("Something needs fixing")).toBeVisible();
      await expect(page.getByText("3A:9F:12:C4", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-lines-add-test-untrusted`);
      await page.getByRole("button", { name: "Trust this certificate" }).click();
      await expect(page.getByText("It works")).toBeVisible();
      await shot(page, `${scheme}-lines-add-test-ok`);
      await page.keyboard.press("Escape");

      // A line's detail.
      await page.getByRole("row", { name: /UCM landlines/ }).click();
      await expect(page.getByRole("heading", { name: "UCM landlines" })).toBeVisible();
      await expect(page.getByText("Calls for any other number ring")).toBeVisible();
      await shot(page, `${scheme}-lines-detail`);
    });

    test("voicemail", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true, email: "on" });
      await page.goto("/voicemail");
      await expect(page.getByRole("heading", { name: "Voicemail", exact: true })).toBeVisible();
      await expect(page.getByRole("heading", { name: "NEW" })).toBeVisible();
      await expect(page.getByText("Heard by Sara Haddad", { exact: false })).toBeVisible();
      // The badge: two new in my box and Sales'; the tab's title says so too.
      await expect(page.getByRole("button", { name: "Voicemail" })).toContainText("2");
      await expect(page).toHaveTitle("(2) Linx");
      await shot(page, `${scheme}-voicemail`);

      // Playing to the end marks it heard.
      await page.getByRole("button", { name: "Play the message from 0501234567" }).click();
      await expect(page.getByRole("region", { name: "HEARD" })).toContainText("0501234567");
      // So does taking the slider to the end without playing the rest
      // (found in Demo B).
      const left = page.getByRole("region", { name: "NEW" });
      await left.getByRole("slider", { name: "Position" }).first().focus();
      await page.keyboard.press("End");
      await expect(page.getByRole("region", { name: "NEW" })).toHaveCount(0);

      // Settings: my greeting, recorded; email on.
      await page.getByRole("main").getByRole("button", { name: "Settings" }).click();
      await expect(page.getByText("To mohammed@example.com", { exact: false })).toBeVisible();
      await expect(page.getByText("recorded", { exact: false }).first()).toBeVisible();
      await shot(page, `${scheme}-voicemail-settings`);
      await page.getByRole("button", { name: "Record" }).last().click();
      await expect(page.getByRole("heading", { name: /Record your .we.re closed. greeting/ })).toBeVisible();
      await shot(page, `${scheme}-voicemail-record`);
      await page.keyboard.press("Escape");
    });

    test("voicemail, empty", async ({ page }) => {
      await fakeServer(page, { signedIn: true, noVoicemail: true });
      await page.goto("/voicemail");
      await expect(page.getByText("No voicemail")).toBeVisible();
      await shot(page, `${scheme}-voicemail-empty`);
    });

    test("call history", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/");
      // The badge: two missed since I last looked.
      await expect(page.getByRole("button", { name: "Call history" })).toContainText("2");
      await page.goto("/calls");
      await expect(page.getByRole("heading", { name: "Call history", exact: true })).toBeVisible();
      await expect(page.getByText("· rang Sales")).toBeVisible();
      await expect(page.getByText("Missed · left a voicemail").first()).toBeVisible();
      await expect(page.getByRole("button", { name: "Call history" })).not.toContainText("2");
      await page.getByRole("button", { name: "+971501234567 · rang Sales" }).click();
      await expect(page.getByText("nobody answered in 25 s", { exact: false })).toBeVisible();
      await expect(page.getByRole("button", { name: "Listen to the voicemail for Sales" })).toBeVisible();
      await shot(page, `${scheme}-call-history`);
      await page.getByRole("button", { name: "Missed", exact: true }).click();
      await expect(page.getByTestId("call-row")).toHaveCount(3);
    });

    test("call history, empty", async ({ page }) => {
      await fakeServer(page, { signedIn: true, noCalls: true });
      await page.goto("/calls");
      await expect(page.getByText("No calls yet")).toBeVisible();
      await shot(page, `${scheme}-call-history-empty`);
    });

    test("admin calls", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/calls");
      await expect(page.getByRole("heading", { name: "Calls", exact: true })).toBeVisible();
      await expect(page.getByTestId("admin-call-row")).toHaveCount(8);
      await expect(page.getByRole("link", { name: "Download (CSV)" })).toHaveAttribute("href", /\/api\/v1\/calls\/csv\?from=/);
      await shot(page, `${scheme}-admin-calls`);
      await page.getByTestId("admin-call-row").first().click();
      await expect(page.getByRole("dialog")).toContainText("Went to the voicemail for Sales");
      await shot(page, `${scheme}-admin-calls-detail`);
    });

    test("ring groups", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/ring-groups");
      await expect(page.getByRole("heading", { name: "Ring groups" })).toBeVisible();
      await expect(page.getByRole("row", { name: /Support/ })).toContainText("2 people, in turn");
      await shot(page, `${scheme}-ring-groups`);

      // Guide me, as far as "How should it ring?" with one after another.
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await page.getByLabel("Name").fill("Accounts");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("checkbox", { name: /Sara Haddad/ }).click();
      await page.getByRole("checkbox", { name: /Reception/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByText("One after another").click();
      await expect(page.getByRole("button", { name: "Move Reception up" })).toBeVisible();
      await shot(page, `${scheme}-ring-groups-add-how`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("combobox").click();
      await expect(page.getByRole("option", { name: /^6000 Sales/ })).toBeVisible();
      await shot(page, `${scheme}-destination-picker`);
      await page.getByRole("option", { name: /^6000 Sales/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByText("Calls ring Sara Haddad, then Reception, 15 seconds each. If nobody answers, the call goes to Sales.")).toBeVisible();
      await page.getByRole("button", { name: "Done" }).click();
      // "Saved. Undo" after every routing change (ADR-071).
      await expect(page.getByRole("status").getByText("Saved.")).toBeVisible();
      await shot(page, `${scheme}-undo-toast`);
      await page.getByRole("status").getByRole("button", { name: "Undo" }).click();
      await expect(page.getByRole("status").getByRole("button", { name: "Redo" })).toBeVisible();

      // Sales' detail: Support sends its unanswered calls there, so
      // choosing Support for Sales is greyed as a loop.
      await page.getByRole("row", { name: /^Sales/ }).click();
      await expect(page.getByText("Support (if nobody answers)")).toBeVisible();
      await expect(page.getByText("Our own").first()).toBeVisible();
      await shot(page, `${scheme}-ring-groups-detail`);
      await page.getByRole("button", { name: "Edit" }).nth(3).click();
      await page.getByRole("combobox").click();
      await expect(page.getByRole("option", { name: /^6001 Support/ })).toBeDisabled();
      await expect(page.getByText("Support already sends its unanswered calls here")).toBeVisible();
      await page.keyboard.press("Escape");
      // Saving it unchanged sends back what the server sent, without the
      // read-only label (found in Demo B).
      const saved = page.waitForResponse((r) => r.url().includes("/api/v1/ring-groups/") && r.request().method() === "PATCH");
      await page.getByRole("button", { name: "Save" }).click();
      expect((await saved).status()).toBe(200);
      // "Saved. Undo" shows over the open group, and Undo can be pressed
      // there (found in Demo B: it was hidden behind the panel).
      await page.getByRole("status").getByRole("button", { name: "Undo" }).click({ timeout: 5_000 });
      await expect(page.getByRole("status").getByText("Put back.")).toBeVisible();
    });

    test("incoming, outgoing, simulator, connections", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/incoming");
      await expect(page.getByRole("heading", { name: "Incoming calls" })).toBeVisible();
      await expect(page.getByText("Calls to +97142000102 ring Sales (all at once) Mon–Fri 08:00–17:00.", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-incoming`);

      // "When someone calls": the wizard, its sentence rebuilt as you pick.
      await page.getByRole("button", { name: "Change where +97142000101 goes" }).click();
      await expect(page.getByRole("heading", { name: "When someone calls +97142000101" })).toBeVisible();
      // Its first sentence is asked of the server with what it sent, minus
      // the read-only labels (found in Demo B).
      await expect(page.getByRole("dialog").getByText("Calls to +97142000101 ring Reception (1110)", { exact: false })).toBeVisible();
      await expect(page.getByText(/readOnly/)).toHaveCount(0);
      await page.getByText("Follow office hours").click();
      await expect(page.getByRole("dialog").getByText("Calls to +97142000101 ring Reception (1110) Mon–Fri 08:00–17:00.", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-incoming-wizard-hours`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByText("Holidays go somewhere else").click();
      await page.locator("#wiz-holidays-to").click();
      await page.getByRole("option", { name: /^Front desk/ }).click();
      await expect(page.getByRole("dialog").getByText("On holidays, the call goes to Front desk.", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-incoming-wizard-after`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Save" }).click();
      await expect(page.getByText("Try it: what happens at")).toBeVisible();
      await shot(page, `${scheme}-incoming-wizard-check`);
      await page.keyboard.press("Escape");

      await page.goto("/admin/office-hours");
      await expect(page.getByRole("heading", { name: "Office hours" })).toBeVisible();
      await expect(page.getByText("Now: open (closes at", { exact: false })).toBeVisible();
      await expect(page.getByText("National Day")).toBeVisible();
      await shot(page, `${scheme}-office-hours`);

      await page.goto("/admin/outgoing");
      await expect(page.getByRole("heading", { name: "Which line first" })).toBeVisible();
      await expect(page.getByRole("switch", { name: /Mobiles/ })).toBeChecked();
      await shot(page, `${scheme}-outgoing`);
      await page.getByRole("switch", { name: /Abroad/ }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await shot(page, `${scheme}-outgoing-abroad-confirm`);
      await page.keyboard.press("Escape");

      await page.goto("/admin/simulator");
      await expect(page.getByRole("heading", { name: "Call simulator" })).toBeVisible();
      await page.getByLabel("Number", { exact: true }).fill("050 123 4567");
      await page.getByRole("button", { name: "Check", exact: true }).click();
      await expect(page.getByText("Goes out on")).toBeVisible();
      await shot(page, `${scheme}-simulator-allowed`);
      await page.getByLabel("Number", { exact: true }).fill("0044 20 7946 0958");
      await page.getByRole("button", { name: "Check", exact: true }).click();
      await expect(page.getByRole("button", { name: "Change what phones can call" })).toBeVisible();
      await shot(page, `${scheme}-simulator-refused`);
      // A call in, on Friday evening: outside office hours.
      await page.getByText("Someone calls in").click();
      await page.getByLabel("Your number").click();
      await page.getByRole("option", { name: "+97142000102" }).click();
      await page.getByText("On", { exact: true }).click();
      await page.getByLabel("Day").fill("2026-10-02");
      await page.getByRole("button", { name: "Check", exact: true }).click();
      await expect(page.getByText("outside office hours", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-simulator-after-hours`);

      await page.goto("/admin/connections");
      await expect(page.getByRole("heading", { name: "Connections" })).toBeVisible();
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByLabel("Name").fill("Provider VPN");
      await page.getByLabel("Or paste it").fill("[Interface]\nPrivateKey = x\n");
      await page.getByRole("button", { name: "Add", exact: true }).click();
      await expect(page.getByText('"Provider VPN" is added.')).toBeVisible();
      await shot(page, `${scheme}-connections-added`);
    });

    test("office hours at home", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true, home: true });
      await page.goto("/admin/office-hours");
      await expect(page.getByText("At home there are no office hours", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-office-hours-home`);
    });

    test("system: alerts, activity, settings", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/system/alerts");
      await expect(page.getByText("Mohammed's phone")).toBeVisible();
      await shot(page, `${scheme}-system-alerts`);
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await shot(page, `${scheme}-system-alerts-add-where`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Topic").fill("linx-office-7h2k9");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Add and test" }).click();
      await expect(page.getByText("ntfy took it. Did it arrive?")).toBeVisible();
      await shot(page, `${scheme}-system-alerts-added`);
      await page.getByRole("button", { name: "It arrived" }).click();

      await page.goto("/admin/system/activity");
      await expect(page.getByText('Added person "Chen Wei"')).toBeVisible();
      await expect(page.getByText("Refused: entered a sign-in code (wrong code)")).toBeVisible();
      await shot(page, `${scheme}-system-activity`);
      await page.getByRole("button", { name: /Added person "Chen Wei"/ }).click();
      await expect(page.getByText("user.create")).toBeVisible();
      await shot(page, `${scheme}-system-activity-detail`);
      await page.keyboard.press("Escape");

      await page.goto("/admin/system/routing-changes");
      await expect(page.getByText("Put back the version from")).toBeVisible();
      await shot(page, `${scheme}-system-routing-changes`);
      await page.getByRole("button", { name: "See the change" }).first().click();
      await expect(page.getByText("Calls ring Sara Haddad, then Reception, 15 seconds each.", { exact: false })).toBeVisible();
      await shot(page, `${scheme}-system-routing-changes-see`);
      await page.keyboard.press("Escape");
      await page.getByRole("button", { name: "Put this back" }).first().click();
      await expect(page.getByRole("dialog").getByText("+97142000102", { exact: true })).toBeVisible();
      await shot(page, `${scheme}-system-routing-changes-put-back`);
      await page.getByRole("button", { name: "Put it back" }).click();
      await expect(page.getByRole("dialog")).toBeHidden();

      await page.goto("/admin/system/settings");
      await expect(page.getByRole("heading", { name: "Company sign-in" })).toBeVisible();
      await shot(page, `${scheme}-system-settings`);
      // What's in effect is always said; a different choice waits for Save
      // (owner, Phase 1E demo).
      await expect(page.getByText("Right now: admins can sign in from anywhere.")).toBeVisible();
      await page.getByRole("radio", { name: /Only my home or office network/ }).click();
      await expect(page.getByText("Not saved yet.")).toBeVisible();
      await page.getByRole("button", { name: "Change anyway" }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await page.getByLabel("Password").fill("correct horse battery");
      await page.getByLabel("Code from your authenticator app").pressSequentially("123456");
      await expect(page.getByText("Right now: admins can sign in only from your home or office network.")).toBeVisible();
      await expect(page.getByText("Not saved yet.")).toHaveCount(0);
      await page.getByRole("button", { name: "+ Add a provider" }).click();
      await page.getByRole("radio", { name: /Microsoft/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByText("Redirect address")).toBeVisible();
      await shot(page, `${scheme}-system-settings-add-provider`);
    });

    test("system settings: Email", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupCompleted: true });
      await page.goto("/admin/system/settings");
      const card = page.getByRole("heading", { name: "Email", exact: true });
      await card.scrollIntoViewIfNeeded();
      await expect(page.getByText("Not set up.")).toBeVisible();
      await page.getByRole("button", { name: "Set up email" }).click();
      // Gmail, already chosen when email was never set up, fills in its
      // server too: Save is ready once the address and password are in.
      await page.getByRole("radio", { name: /Gmail or Google Workspace/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Send from this address").fill("pbx@example.com");
      await page.getByLabel("App password").fill("abcd efgh ijkl mnop");
      await expect(page.getByRole("button", { name: "Save and send a test" })).toBeEnabled();
      await page.getByRole("button", { name: "Back" }).click();
      // Microsoft 365's warning comes with the choice.
      await page.getByRole("radio", { name: /Microsoft 365/ }).click();
      await expect(page.getByText("Microsoft is turning off password sign-in for sending mail.")).toBeVisible();
      await page.getByRole("radio", { name: /Gmail or Google Workspace/ }).click();
      await shot(page, `${scheme}-system-settings-email-who`);
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByText("make an app password at myaccount.google.com")).toBeVisible();
      // A preset fills in the server; "Something else" asks for it.
      await expect(page.getByLabel("Mail server")).toHaveCount(0);
      await page.getByLabel("Send from this address").fill("pbx@example.com");
      await page.getByLabel("App password").fill("abcd efgh ijkl mnop");
      await page.getByLabel('"From" name').fill("Linx at Example Co");
      await shot(page, `${scheme}-system-settings-email-sign-in`);
      await page.getByRole("button", { name: "Save and send a test" }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await page.getByLabel("Password", { exact: true }).fill("correct horse battery");
      await page.getByLabel("Code from your authenticator app").pressSequentially("123456");
      await expect(page.getByText("Connected to smtp.gmail.com")).toBeVisible();
      await expect(page.getByText("Certificate checked")).toBeVisible();
      await expect(page.getByText("Did it arrive?")).toBeVisible();
      await shot(page, `${scheme}-system-settings-email-test`);
      await page.getByRole("button", { name: "No, show me what to check" }).click();
      await expect(page.getByText("Look in the spam or junk folder.")).toBeVisible();
      await page.getByRole("button", { name: "It arrived now" }).click();
      await expect(page.getByText("Email is on.")).toBeVisible();
      await page.getByRole("button", { name: "Done" }).click();
      await expect(page.getByText("Linx at Example Co <pbx@example.com>")).toBeVisible();
      await page.getByRole("button", { name: "Send a test email" }).scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-settings-email`);
    });

    test("system settings: Calls to the app", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupCompleted: true });
      await page.goto("/admin/system/settings");
      const card = page.getByRole("heading", { name: "Calls to the app" });
      await card.scrollIntoViewIfNeeded();
      // What the gateway has done is in plain words, not a graph.
      await expect(page.getByText("96 phones, 0.8s on average to ring (slowest 1.9s)")).toBeVisible();
      await shot(page, `${scheme}-system-settings-app-push`);
      await page.getByRole("button", { name: "Change" }).last().click();
      await expect(page.getByText("so a sleeping phone rings")).toBeVisible();
      await page.getByLabel("Team id").fill("ABCDE12345");
      await page.getByLabel("Key id").fill("KEY1234567");
      await shot(page, `${scheme}-system-settings-app-push-key`);
      await page.getByRole("button", { name: "Save" }).click();
      // The Apple key is a system admin's, after "confirm it's you".
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await page.getByLabel("Password", { exact: true }).fill("correct horse battery");
      await page.getByLabel("Code from your authenticator app").pressSequentially("123456");
      await expect(page.getByText("KEY1234567 · team ABCDE12345")).toBeVisible();
    });

    test("system alerts: an email channel", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/system/alerts");
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      // Greyed, with the reason, until email is set up.
      await expect(page.getByRole("radio", { name: /^Email/ })).toBeDisabled();
      await expect(page.getByText("Set up email first (System → Settings).")).toBeVisible();
    });

    test("system alerts: an email channel once email is on", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true, email: "on" });
      await page.goto("/admin/system/alerts");
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await page.getByRole("radio", { name: /^Email/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Send to").fill("mohammed@example.com, ops@example.com");
      await shot(page, `${scheme}-system-alerts-add-email`);
    });

    test("system settings: Email isn't sending", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupCompleted: true, email: "failing" });
      await page.goto("/admin/system/settings");
      const card = page.getByRole("heading", { name: "Email", exact: true });
      await card.scrollIntoViewIfNeeded();
      await expect(page.getByText(/3 waiting: The mail server didn't accept the user name and password/)).toBeVisible();
      await page.getByRole("button", { name: "Send a test email" }).click();
      await expect(page.getByText("Signed in")).toHaveCount(0);
      await expect(page.getByText("Certificate checked")).toBeVisible();
      await page.getByRole("button", { name: "Send a test email" }).scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-settings-email-failing`);
    });

    test("system settings: an admin sees Email read-only", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true, email: "on" });
      await page.goto("/admin/system/settings");
      await expect(page.getByText("Linx at Example Co <pbx@example.com>")).toBeVisible();
      await expect(page.getByRole("button", { name: "Send a test email" })).toBeDisabled();
      await expect(page.getByRole("button", { name: "Turn off" }).first()).toBeDisabled();
    });

    test("system settings: Help answers", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, setupCompleted: true });
      await page.goto("/admin/system/settings");
      const card = page.getByRole("heading", { name: "Help answers" });
      await card.scrollIntoViewIfNeeded();
      await expect(page.getByText("Right now: off: Help shows search results only.")).toBeVisible();
      // Ollama asks for its address; Anthropic's is fixed.
      await page.getByRole("radio", { name: /Ollama/ }).click();
      await expect(page.getByLabel("Address", { exact: true })).toBeVisible();
      await page.getByRole("radio", { name: /Anthropic/ }).click();
      await expect(page.getByLabel("Address", { exact: true })).toHaveCount(0);
      await page.getByRole("switch", { name: "Written answers in Help" }).click();
      await page.getByLabel("API key", { exact: true }).fill("sk-ant-example");
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await page.getByLabel("Password").fill("correct horse battery");
      await page.getByLabel("Code from your authenticator app").pressSequentially("123456");
      await expect(page.getByText("Right now: on, written by Anthropic (Claude).")).toBeVisible();
      await page.getByRole("button", { name: "Test" }).click();
      await expect(page.getByText("2. Pick the person and the phone's model.")).toBeVisible();
      await card.scrollIntoViewIfNeeded();
      await shot(page, `${scheme}-system-settings-help-answers`);
    });

    test("expert: webhooks and API keys", async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true });
      await page.goto("/admin/webhooks");
      await expect(page.getByText("https://crm.example.com/hooks/linx")).toBeVisible();
      await shot(page, `${scheme}-webhooks`);
      await page.getByRole("row", { name: /crm\.example\.com/ }).click();
      await expect(page.getByRole("heading", { name: "Recent deliveries" })).toBeVisible();
      await shot(page, `${scheme}-webhooks-detail`);
      await page.keyboard.press("Escape");
      await page.getByRole("button", { name: "+ Add" }).click();
      await page.getByRole("button", { name: /Guide me/ }).click();
      await page.getByLabel("Address", { exact: true }).fill("https://erp.example.com/linx");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Add and test" }).click();
      await expect(page.getByText("It answered 200 in 183 ms.")).toBeVisible();
      await shot(page, `${scheme}-webhooks-added`);
      await page.getByRole("button", { name: "Done" }).click();

      await page.goto("/admin/api-keys");
      await expect(page.getByRole("cell", { name: "CRM" })).toBeVisible();
      await shot(page, `${scheme}-api-keys`);
      await page.getByRole("button", { name: "+ Create" }).click();
      await page.getByLabel("What it's for").fill("Directory sync");
      await page.getByRole("radio", { name: /Manage people and extensions/ }).click();
      await shot(page, `${scheme}-api-keys-create`);
      await page.getByRole("button", { name: "Create", exact: true }).click();
      await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
      await page.getByLabel("Password").fill("correct horse battery");
      await page.getByLabel("Code from your authenticator app").pressSequentially("123456");
      await expect(page.getByText("The key, shown once:")).toBeVisible();
      await shot(page, `${scheme}-api-keys-created`);
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
      await shot(page, `${scheme}-incoming-call`);
      await page.getByRole("button", { name: "Decline" }).click();

      await page.goto("/settings");
      await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
      await shot(page, `${scheme}-settings`);
    });
  });
}

test.describe("setup wizard", () => {
  test("place", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 1 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Where will you use Linx?" })).toBeVisible();
    await shot(page, "setup-wizard-place");
    await page.getByRole("button", { name: "Business" }).click();
    await expect(page.getByRole("button", { name: "Next" })).toBeEnabled();
    // A Sunday-to-Thursday business sets its week here; it's saved as its
    // Office hours.
    await page.getByRole("button", { name: "Sunday" }).click();
    await page.getByRole("button", { name: "Friday" }).click();
    await expect(page.getByRole("button", { name: "Friday" })).toHaveAttribute("aria-pressed", "false");
    await shot(page, "setup-wizard-place-hours");
    const saved = page.waitForRequest((r) => r.url().endsWith("/api/v1/schedules/s1") && r.method() === "PATCH");
    await page.getByRole("button", { name: "Next" }).click();
    const body = (await saved).postDataJSON() as { spans: { weekday: number; opens: string; closes: string }[] };
    expect(body.spans.map((x) => x.weekday).sort()).toEqual([0, 1, 2, 3, 4]);
    expect(body.spans.every((x) => x.opens === "08:00" && x.closes === "17:00")).toBe(true);
  });

  test("start: fresh or restore", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 1 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "How do you want to start?" })).toBeVisible();
    await shot(page, "setup-wizard-start");
    await page.getByRole("button", { name: /Restore from a backup/ }).click();
    await expect(page.getByRole("heading", { name: "Restore from a backup" })).toBeVisible();
    const restore = page.getByRole("button", { name: "Restore", exact: true });
    await expect(restore).toBeDisabled();
    await page.getByLabel("The backup's password").fill("correct horse battery staple");
    await page.getByRole("checkbox", { name: "I understand, replace everything" }).click();
    await expect(restore).toBeEnabled();
    await shot(page, "setup-wizard-restore");
    await page.getByRole("radio", { name: /A backup place set up on this server/ }).click();
    await page.getByRole("radio", { name: "An older one" }).click();
    await expect(restore).toBeDisabled();
    await page.getByLabel("Its name").fill("office-nas");
    await page.getByLabel("Backup ID").fill("4f2a9c1e");
    await shot(page, "setup-wizard-restore-destination");
    await restore.click();
    await expect(page.getByRole("heading", { name: "Starting the restore" })).toBeVisible();
    await shot(page, "setup-wizard-restore-waiting");
    await expect(page.getByRole("heading", { name: "Restored" })).toBeVisible({ timeout: 10_000 });
    await shot(page, "setup-wizard-restore-done");
  });

  test("restore: failed, and under way after a reload", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 1, restore: "failed" });
    await page.goto("/setup");
    await expect(page.getByRole("alert")).toContainText("wrong password");
    await expect(page.getByRole("textbox", { name: "Folder" })).toHaveValue("/var/backups/linx");
    await shot(page, "setup-wizard-restore-failed");
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 1, restore: "running" });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Restoring your backup" })).toBeVisible();
    await shot(page, "setup-wizard-restore-running");
  });

  test("restore from a backup file", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 1 });
    await page.goto("/setup");
    await page.getByRole("button", { name: /Restore from a backup/ }).click();
    await page.getByRole("radio", { name: /A backup file on my computer/ }).click();
    await page.getByLabel("Backup file", { exact: true }).setInputFiles({ name: "linx-backup-2026-09-27-0300.tar", mimeType: "application/x-tar", buffer: Buffer.alloc(4096) });
    await expect(page.getByText("Size: 4 KB")).toBeVisible();
    await page.getByLabel("The backup's password").fill("example-backup-password-for-screenshots");
    await page.getByRole("checkbox", { name: "I understand, replace everything" }).click();
    await shot(page, "setup-wizard-restore-file");
    await page.getByRole("button", { name: "Restore", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Starting the restore" })).toBeVisible();
  });

  test("numbers", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 3 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "How should extension numbers look?" })).toBeVisible();
    await expect(page.getByText("100–599")).toBeVisible();
    await shot(page, "setup-wizard-numbers");
    await page.getByText("Change the ranges").click();
    await shot(page, "setup-wizard-numbers-ranges");
    // Typing a new range the way people do: empty a box, type the number
    // (the second box once emptied the first; owner, Phase 1E demo).
    const from = page.getByLabel("People from");
    const to = page.getByLabel("People to");
    await from.fill("");
    await from.pressSequentially("200");
    await to.fill("");
    await to.pressSequentially("499");
    await expect(from).toHaveValue("200");
    await expect(to).toHaveValue("499");
    await expect(page.getByText("200–499")).toBeVisible();
    await page.getByText("Use the recommended ranges").click();
    await expect(from).toHaveValue("100");
  });

  test("people: a range changed after a first look is followed", async ({ page }) => {
    // The owner's steps in the Phase 1E demo: Numbers (100-599) → People →
    // Back → people 200-499 → People again: suggestions come from 200.
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 3, followRanges: true });
    await page.goto("/setup");
    await page.getByRole("button", { name: "Next" }).click();
    await page.getByRole("button", { name: "+ Add another row" }).click();
    await expect(page.getByLabel("Extension").last()).toHaveValue("100");
    await page.getByRole("button", { name: "Back" }).click();
    await page.getByText("Change the ranges").click();
    await page.getByLabel("People from").fill("200");
    await page.getByLabel("People to").fill("499");
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByLabel("Extension").last()).toHaveValue("200");
    await expect(page.getByText("isn't in the people range", { exact: false })).toHaveCount(0);
  });

  test("people", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 4 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Extensions: who and what will use Linx?" })).toBeVisible();
    await page.getByRole("button", { name: "+ Add another row" }).click();
    await shot(page, "setup-wizard-people");
    // Checked as it's typed; Next doesn't skip a row that wasn't created
    // (install demo, 2026-09-29).
    await page.getByLabel("Name").last().fill("Sara Haddad");
    await page.getByLabel("Email", { exact: true }).last().fill("sara@");
    await page.getByLabel("Extension").last().fill("99999");
    await expect(page.getByText("That doesn't look like an email address.", { exact: false })).toBeVisible();
    // One button: Next becomes "Create 1 extension", off until the row is right.
    await expect(page.getByRole("button", { name: "Create 1 extension" })).toBeDisabled();
    await page.getByRole("button", { name: "Skip for now" }).click();
    await expect(page.getByText("Create the extensions above first", { exact: false })).toBeVisible();
    await shot(page, "setup-wizard-people-problems");
    await page.getByLabel("Email", { exact: true }).last().fill("sara@example.com");
    await page.getByLabel("Extension").last().fill("102");
    await expect(page.getByRole("button", { name: "Create 1 extension" })).toBeEnabled();
    // A door phone: a name and a number, no email (an extension, no person).
    await page.getByRole("button", { name: "+ Add another row" }).click();
    await page.getByLabel("Name").last().fill("Front door");
    await expect(page.getByText("A person needs an email", { exact: false })).toBeVisible();
    await page.getByLabel("Type").last().click();
    await page.getByRole("option", { name: "Phone" }).click();
    await page.getByLabel("Extension").last().fill("150");
    await expect(page.getByRole("button", { name: "Create 2 extensions" })).toBeEnabled();
    await shot(page, "setup-wizard-people-door-phone");
  });

  test("line and calls", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 5 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Connect a phone line now?" })).toBeVisible();
    await shot(page, "setup-wizard-line");
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("heading", { name: "What can your phones call?" })).toBeVisible();
    await shot(page, "setup-wizard-calls");
  });

  test("done", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 7, setupCompleted: true });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Linx is ready" })).toBeVisible();
    await shot(page, "setup-wizard-done");
  });
});

test.describe("phone width", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  // Every signed-in page at phone width, as an admin, so no page scrolls
  // sideways (shot checks it).
  for (const [path, name, ready] of [
    ["/", "dialer", "Dialer"], ["/team", "team", "Team"], ["/settings", "settings", "Settings"],
    ["/admin", "admin-home", "Getting started"], ["/admin/people", "people", "People"],
    ["/admin/extensions", "extensions", "Extensions"], ["/admin/system/status", "system-status", "Linx services"],
    ["/admin/system/backups", "system-backups", "Backups"], ["/admin/system/server", "system-server", "Server settings"],
    ["/admin/lines", "lines", "Phone lines"], ["/admin/incoming", "incoming", "Incoming calls"],
    ["/voicemail", "voicemail", "Voicemail"], ["/calls", "call-history", "Call history"], ["/admin/calls", "admin-calls", "Calls"],
    ["/admin/ring-groups", "ring-groups", "Ring groups"], ["/admin/office-hours", "office-hours", "Office hours"],
    ["/admin/outgoing", "outgoing", "Outgoing calls"], ["/admin/simulator", "simulator", "Call simulator"],
    ["/admin/connections", "connections", "Connections"],
    ["/admin/system/alerts", "system-alerts", "Where alerts go"], ["/admin/system/activity", "system-activity", "System"],
    ["/admin/system/routing-changes", "system-routing-changes", "System"],
    ["/admin/system/settings", "system-settings", "Admins can sign in from"],
    ["/admin/webhooks", "webhooks", "Webhooks"], ["/admin/api-keys", "api-keys", "API keys"],
    ["/help", "help", "Help"], ["/help/extensions", "help-guide", "Extensions"],
    ["/repair", "repair", "Fix this server's address"],
  ] as const) {
    test(`no sideways scrolling: ${name}`, async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 5, backups: true, download: "ready", serverSettings: "home", repair: "sign-in", moved: true });
      await page.goto(path);
      await expect(page.getByRole("heading", { name: ready, exact: true }).first()).toBeVisible();
      await shot(page, `phone-${name}`);
    });
  }

  test("the emailed phone setup page", async ({ page }) => {
    await fakeServer(page);
    const soon = Math.floor((Date.now() + 9 * 60_000) / 1000);
    await page.goto(`/set-up-phone#${fakeToken(soon)}`);
    await expect(page.getByRole("heading", { name: "Set up this phone" })).toBeVisible();
    await shot(page, "phone-set-up-phone");
  });

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

test.describe("system backups", () => {
  test("first visit: nothing yet", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true });
    await page.goto("/admin/system/backups");
    await expect(page.getByText("No backups yet")).toBeVisible();
    await expect(page.getByText("Off: nothing is backed up")).toBeVisible();
    await shot(page, "system-backups-empty");
    await page.getByRole("radio", { name: /Every week/ }).click();
    await expect(page.getByLabel("On", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Save schedule" }).click();
    await expect(page.getByText("Saved.")).toBeVisible();
    await page.getByRole("button", { name: "Back up now" }).click();
    await expect(page.getByText("Starts within a minute")).toBeVisible();
    await page.getByRole("button", { name: "Make a backup file" }).click();
    await expect(page.getByText(/Waiting for the server to start/)).toBeVisible();
    await shot(page, "system-backups-waiting");
  });

  test("backup file: being made, failed, someone else's", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, backups: true, download: "preparing" });
    await page.goto("/admin/system/backups");
    await expect(page.getByText(/Backing up now and making the file/)).toBeVisible();
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await fakeServer(page, { signedIn: true, admin: true, backups: true, download: "failed" });
    await page.goto("/admin/system/backups");
    await expect(page.getByText(/couldn't be made/)).toBeVisible();
    await shot(page, "system-backups-download-failed");
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await fakeServer(page, { signedIn: true, admin: true, backups: true, download: "others" });
    await page.goto("/admin/system/backups");
    await expect(page.getByText("Another admin has a backup file ready")).toBeVisible();
  });
});

test.describe("my account: authenticator app", () => {
  test("replace it, with new recovery codes", async ({ page }) => {
    await fakeServer(page, { signedIn: true });
    await page.goto("/account");
    await expect(page.getByRole("heading", { name: "Authenticator app" })).toBeVisible();
    await expect(page.getByText(/8 recovery codes left/)).toBeVisible();
    await page.getByRole("button", { name: "Replace" }).click();
    await expect(page.getByAltText("QR code for your authenticator app")).toBeVisible();
    await expect(page.getByText("Your current one keeps working")).toBeVisible();
    await shot(page, "account-replace-authenticator");
    await page.getByLabel("6-digit code from the app").pressSequentially("000000");
    await expect(page.getByText("That code isn't right")).toBeVisible();
    await page.getByLabel("6-digit code from the app").pressSequentially("123456");
    await expect(page.getByRole("list", { name: "Recovery codes" })).toContainText("k7qm-2xpd");
    await expect(page.getByText("Your old authenticator and old recovery codes no longer work")).toBeVisible();
    await shot(page, "account-replace-authenticator-codes");
    await expect(page.getByRole("button", { name: "Done" })).toBeDisabled();
    await page.getByText("I've saved these codes").click();
    await page.getByRole("button", { name: "Done" }).click();
    await expect(page.getByRole("dialog")).toBeHidden();
  });
});

test.describe("system: server settings", () => {
  test("closed until sudo linx setup opens it", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true });
    await page.goto("/admin/system/server");
    await expect(page.getByRole("button", { name: "Server settings" })).toHaveAttribute("aria-current", "page");
    await expect(page.getByText("sudo linx setup", { exact: true })).toBeVisible();
    await shot(page, "system-server-closed");
  });

  test("at home: size, Portainer, then the change's steps", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
    await page.goto("/admin/system/server");
    await expect(page.getByText("Porkbun: Linx updates them for you ✓")).toBeVisible();
    await expect(page.getByRole("button", { name: "Apply" })).toBeDisabled();
    await page.getByRole("radio", { name: /Standard/ }).click();
    await page.getByLabel("Portainer").click();
    await shot(page, "system-server-settings");
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("dialog", { name: "Apply these changes?" })).toContainText("Calls in progress may drop.");
    await page.getByRole("dialog").getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("list", { name: "Change steps" }).getByRole("listitem")).toHaveCount(3);
    await expect(page.getByText("Qx7-m2Pd-9vRk")).toBeVisible();
    await shot(page, "system-server-changing");
  });

  test("rented: no Portainer, DNS kept right automatically", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
    await page.goto("/admin/system/server");
    await expect(page.getByText("No key: the certificate renews through port 443")).toBeVisible();
    await expect(page.getByLabel("Portainer")).toHaveCount(0);
    await expect(page.getByText("Portainer is offered only on a server at home")).toBeVisible();
    // The company the name servers show, named on the records card's button.
    await page.getByRole("button", { name: "Let Linx update these at Porkbun for you" }).click();
    await expect(page.getByLabel("DNS company")).toContainText("Porkbun");
    await expect(page.getByText("(from its name servers)")).toBeVisible();
    await expect(page.getByRole("button", { name: "Check the key" })).toBeDisabled();
    await page.getByLabel("API key", { exact: true }).fill("pk1_0123456789abcdef");
    await page.getByLabel("Secret key", { exact: true }).fill("bad-key");
    await page.getByRole("button", { name: "Check the key" }).click();
    await expect(page.getByRole("alert")).toContainText("Porkbun: Invalid API key");
    await page.getByLabel("Secret key", { exact: true }).fill("sk1_0123456789abcdef");
    await page.getByRole("button", { name: "Check the key" }).click();
    await expect(page.getByText("This key can change example.com's records.")).toBeVisible();
    await page.getByRole("heading", { name: "Let Linx update the records for you" }).scrollIntoViewIfNeeded();
    await shot(page, "system-server-dns-key");
    // Another company: only what it asks for.
    await page.getByLabel("DNS company").click();
    await page.getByRole("option", { name: "OVH" }).click();
    await expect(page.getByLabel("Region")).toBeVisible();
    await expect(page.getByLabel("Consumer key", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Secret key", { exact: true })).toHaveCount(0);
    await page.getByLabel("DNS company").click();
    await page.getByRole("option", { name: "Not in the list" }).click();
    await expect(page.getByText("Linx can't change records there by itself.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Check the key" })).toHaveCount(0);
  });

  test("at home: stop keeping the records right", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Stop", exact: true }).click();
    await expect(page.getByText("Linx stops changing the records")).toBeVisible();
    await page.getByRole("button", { name: "Apply" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("list", { name: "Change steps" }).getByRole("listitem").first()).toContainText("Save your settings");
  });

  test("at home: a new domain shows what it needs first", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Change", exact: true }).nth(1).click();
    await expect(page.getByRole("button", { name: "Check", exact: true })).toBeDisabled();
    await page.getByLabel("New domain").fill("203.0.113.9");
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("That's an address, not a domain");
    await page.getByLabel("New domain").fill("pbx.example.org");
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Before you apply" })).toBeVisible();
    await expect(page.getByText("Passkeys only work at the address")).toBeVisible();
    await expect(page.getByRole("heading", { name: "Your front door needs to do three things" })).toBeVisible();
    await expect(page.getByText("Linx only accepts it from 192.168.1.30, your front door.")).toBeVisible();
    await expect(page.getByText("linx-pbx-example-org-web:")).toBeVisible();
    await expect(page.getByRole("button", { name: "Apply" })).toBeDisabled();
    await page.getByLabel("I've done these steps").click();
    await shot(page, "system-server-move");
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("dialog", { name: "Move Linx to https://pbx.example.org?" })).toBeVisible();
    await page.getByRole("dialog").getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("list", { name: "Change steps" }).getByRole("listitem").first()).toContainText("Save your settings");
  });

  test("at home: another public port", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Change", exact: true }).first().click();
    await page.getByRole("button", { name: "Advanced" }).click();
    await page.getByRole("radio", { name: /public port/ }).click();
    // The owner's warning comes first (docs/SIMPLER.md §2.5).
    await expect(page.getByText("Linx is designed and tuned to work best on port 443")).toBeVisible();
    await expect(page.getByRole("button", { name: "Check", exact: true })).toBeVisible();
    await shot(page, "system-server-public-port-warning");
    await page.getByRole("button", { name: "Use another port" }).click();
    await page.getByLabel("Public web port").fill("5061");
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByText("5061 is used by Linx itself: choose another, like 8443.")).toBeVisible();
    await page.getByLabel("Public web port").fill("8443");
    await page.getByRole("radio", { name: /^3478/ }).click();
    await expect(page.getByText("192.168.1.212 port 3478")).toBeVisible();
    await expect(page.getByText("https://example.com:8443", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByText("Passkeys keep working: they belong to example.com, whatever the port.")).toBeVisible();
    await expect(page.getByText("On your router, forward TCP 8443 to 192.168.1.212 port 8443.")).toBeVisible();
    await shot(page, "system-server-public-port");
    await page.getByLabel("I've done these steps").click();
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("dialog", { name: "Move Linx to https://example.com:8443?" })).toBeVisible();
  });

  test("a server that stops answering: the page says so", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
    await page.goto("/admin/system/server");
    await expect(page.getByRole("button", { name: "Change", exact: true }).first()).toBeVisible();
    // A port a move closed can drop requests rather than refuse them: the
    // page mustn't wait for ever (Demo A).
    await page.route("**/api/v1/server-settings", () => new Promise(() => {}));
    await expect(page.getByText("Linx is restarting with the new settings")).toBeVisible({ timeout: 25_000 });
  });

  test("at home: show the steps for the front door in use", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
    await page.goto("/admin/system/server");
    // No pretend change needed to see them (owner, Demo A).
    await page.getByRole("button", { name: "Show the steps" }).click();
    await expect(page.getByText("How to do this in")).toBeVisible();
    await expect(page.getByRole("tab", { name: "Pangolin" })).toBeVisible();
    await shot(page, "system-server-show-steps");
    await page.getByRole("button", { name: "Hide the steps" }).click();
    await expect(page.getByText("How to do this in")).toHaveCount(0);
  });

  test("rented, no key: another public port asks for one", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Change", exact: true }).first().click();
    await page.getByRole("button", { name: "Advanced" }).click();
    await page.getByRole("radio", { name: /public port/ }).click();
    await page.getByRole("button", { name: "Use another port" }).click();
    await expect(page.getByText("Linx will open TCP 8443 and UDP 443 on this server's firewall")).toBeVisible();
    // Certificates can't be checked on port 443: the DNS key form opens.
    await expect(page.getByRole("heading", { name: "Let Linx update the records for you" })).toBeVisible();
    await shot(page, "system-server-public-port-rented");
    // The key's Check answers for the key, and the move's Check still
    // works after it (Demo A: the tick never came, and Check stayed grey).
    await page.getByLabel("API key", { exact: true }).fill("pk1_0123456789abcdef");
    await page.getByLabel("Secret key", { exact: true }).fill("sk1_0123456789abcdef");
    await page.getByRole("button", { name: "Check the key" }).click();
    await expect(page.getByText("This key can change example.com's records.")).toBeVisible();
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Before you apply" })).toBeVisible();
    await expect(page.getByText("This key can change example.com's records.")).toBeVisible();
  });

  test("rented, no token: a new domain's DNS records to add first", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Change", exact: true }).first().click();
    await expect(page.getByRole("radio", { name: "Another program passes Linx through" })).toBeVisible();
    // A proxy that unlocks the traffic is home only (decision 2026-09-30);
    // another public port is offered on rented servers too (ADR-064).
    await page.getByRole("button", { name: "Advanced" }).click();
    await expect(page.getByRole("radio", { name: /public port/ })).toBeVisible();
    await expect(page.getByRole("radio", { name: /unlocks the traffic/ })).toHaveCount(0);
    await page.getByRole("radio", { name: "Nothing else uses port 443 — Linx takes it" }).click();
    await page.getByRole("button", { name: "Change", exact: true }).first().click();
    await page.getByLabel("New domain").fill("pbx.example.org");
    // The front door's editor is open too: its Check stays off until it changes.
    await expect(page.getByRole("button", { name: "Check", exact: true }).first()).toBeDisabled();
    await page.getByRole("button", { name: "Check", exact: true }).last().click();
    await expect(page.getByText("DNS records to add")).toBeVisible();
    await expect(page.getByText("turn.pbx.example.org", { exact: true })).toBeVisible();
    await shot(page, "system-server-move-records");
  });
});

test.describe("moved to a new place", () => {
  test("after a restore elsewhere: tick, hide, show", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 5, moved: true });
    await page.goto("/admin");
    const card = page.getByRole("region", { name: "Moved to a new place?" });
    await expect(card).toContainText("before:");
    await expect(card).toContainText("home 192.168.1.0/24, pbx.old.com");
    await expect(card).toContainText("0 of 7 done");
    await expect(card.getByLabel("Add your backup places again")).toBeDisabled();
    await shot(page, "admin-home-moved");
    await card.getByLabel("Turn the old server off").click();
    await expect(card).toContainText("1 of 7 done");
    await card.getByRole("button", { name: "Hide for now" }).click();
    await expect(page.getByText("Moved to a new place? 6 left")).toBeVisible();
    await page.getByRole("button", { name: "Show", exact: true }).click();
    await expect(card).toContainText("1 of 7 done");
  });

  test("the sign-in page says passkeys stay behind", async ({ page }) => {
    await fakeServer(page, { moved: true });
    await page.goto("/");
    await expect(page.getByText("Passkeys from the old address don't work here")).toBeVisible();
    await shot(page, "sign-in-moved");
  });
});

test.describe("repair page (port 6464)", () => {
  test("sign in with a password and authenticator, never a passkey", async ({ page }) => {
    await fakeServer(page, { repair: "sign-in", serverSettings: "home" });
    await page.goto("/repair");
    await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Sign in with a passkey" })).toHaveCount(0);
    await expect(page.getByText("sudo linx setup --new-link --no-sign-in")).toBeVisible();
    await expect(page.getByText("didn't answer from the server")).toBeVisible();
    await shot(page, "repair-sign-in");
  });

  test("signed in as a system admin: the Server settings", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, repair: "sign-in", serverSettings: "home" });
    await page.goto("/repair");
    await expect(page.getByRole("heading", { name: "Fix this server's address" })).toBeVisible();
    await expect(page.getByText("Porkbun: Linx updates them for you ✓")).toBeVisible();
    await shot(page, "repair-settings");
  });

  test("a link that skips the sign-in", async ({ page }) => {
    await fakeServer(page, { repair: "no-sign-in", serverSettings: "rented" });
    await page.goto("/repair");
    await expect(page.getByRole("heading", { name: "Fix this server's address" })).toBeVisible();
    await page.getByRole("button", { name: "Change", exact: true }).nth(1).click();
    await page.getByLabel("New domain").fill("pbx.example.org");
    await page.getByRole("button", { name: "Check", exact: true }).click();
    await expect(page.getByText("DNS records to add")).toBeVisible();
    await page.getByRole("button", { name: "Apply" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("list", { name: "Change steps" }).getByRole("listitem").first()).toContainText("Check the DNS records");
    await shot(page, "repair-no-sign-in");
  });

  test("only a system admin sees the tab", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true });
    await page.goto("/admin/system/status");
    await expect(page.getByRole("button", { name: "Backups" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Server settings" })).toHaveCount(0);
  });
});

test.describe("system alerts", () => {
  test("a Gotify on the home network: allowed after confirm-it's-you", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true });
    await page.goto("/admin/system/alerts");
    await page.getByRole("button", { name: "+ Add" }).click();
    await page.getByRole("button", { name: /Quick add/ }).click();
    await page.getByLabel("Where").selectOption("gotify");
    await page.getByLabel("Server").fill("http://192.168.1.40:8080");
    await page.getByLabel("App token").fill("AbCdEf123");
    await page.getByRole("button", { name: "Add and test" }).click();
    await expect(page.getByRole("button", { name: "Allow 192.168.1.40 and add" })).toBeVisible();
    await shot(page, "system-alerts-private-address");
    await page.getByRole("button", { name: "Allow 192.168.1.40 and add" }).click();
    await expect(page.getByRole("heading", { name: "Confirm it's you" })).toBeVisible();
    await page.getByLabel("Password").fill("correct horse battery");
    await page.getByLabel("Code from your authenticator app").pressSequentially("123456");
    await expect(page.getByText("Gotify took it. Did it arrive?")).toBeVisible();
  });
});

test.describe("check it: the phone's page", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });

  test("reached, and call audio works", async ({ page }) => {
    // A relay address, as a real call relay would give.
    await page.addInitScript(() => {
      class FakePC {
        onicecandidate: ((e: { candidate: { type: string; candidate: string } | null }) => void) | null = null;
        createDataChannel() { return {}; }
        async createOffer() { return { type: "offer", sdp: "" }; }
        async setLocalDescription() {
          setTimeout(() => this.onicecandidate?.({ candidate: { type: "relay", candidate: "candidate:1 1 udp 1 203.0.113.5 50000 typ relay" } }), 200);
        }
        close() {}
      }
      (window as unknown as { RTCPeerConnection: unknown }).RTCPeerConnection = FakePC;
    });
    await fakeServer(page, {});
    await page.goto("/reach/7K2QHM4XRB");
    await expect(page.getByRole("heading", { name: "You reached Linx at example.com" })).toBeVisible();
    await expect(page.getByText("Linx saw you coming from 5.194.33.12.")).toBeVisible();
    await expect(page.getByText("Calls from here will have audio.")).toBeVisible();
    await shot(page, "reach-phone");
  });

  test("a used link", async ({ page }) => {
    await fakeServer(page, {});
    await page.goto("/reach/USEDCODE22");
    await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
    await expect(page.getByText("Links work once, for 10 minutes", { exact: false })).toBeVisible();
    await shot(page, "reach-phone-used");
  });
});

test.describe("system status", () => {
  test("check it: from this server, then a phone link", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, reach: "waiting" });
    await page.goto("/admin/system/status");
    const card = page.locator("section", { has: page.getByRole("heading", { name: "Reachable from outside" }) });
    await card.getByRole("button", { name: "Check it" }).click();
    await expect(card.getByText("example.com answers with Linx's certificate")).toBeVisible();
    await expect(card.getByText("Calls from outside will have no audio.", { exact: false })).toBeVisible();
    await expect(card.getByRole("link", { name: "Show the steps" })).toBeVisible();
    await expect(card.getByText("https://example.com/reach/7K2QHM4XRB")).toBeVisible();
    await expect(card.getByRole("img", { name: "The link as a picture to scan" })).toBeVisible();
    await expect(card.getByText("Waiting for your phone…", { exact: false })).toBeVisible();
    await card.scrollIntoViewIfNeeded();
    await shot(page, "system-status-check-it");
  });

  test("check it: a name points elsewhere, and its records", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, reach: "wrong-dns" });
    await page.goto("/admin/system/status");
    const card = page.locator("section", { has: page.getByRole("heading", { name: "Reachable from outside" }) });
    await card.getByRole("button", { name: "Check it" }).click();
    await card.getByRole("button", { name: "Show the records" }).click();
    const records = card.getByRole("region", { name: "Records for example.com" });
    await expect(records.getByText("Shows 94.200.1.9")).toBeVisible();
    await expect(records.getByText("Not found yet")).toBeVisible();
    await expect(records.getByText("sip.example.com")).toBeVisible();
    await expect(card.getByRole("button", { name: "Show the records" })).toHaveCount(0);
    await records.scrollIntoViewIfNeeded();
    await shot(page, "system-status-check-it-records");
  });

  test("check it: the phone came", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, reach: "reached" });
    await page.goto("/admin/system/status");
    const card = page.locator("section", { has: page.getByRole("heading", { name: "Reachable from outside" }) });
    await card.getByRole("button", { name: "Check it" }).click();
    await expect(card.getByText("Your phone reached Linx from 5.194.33.12. People outside can open Linx.")).toBeVisible();
    await expect(card.getByText("Calls from outside will have audio.")).toBeVisible();
    await card.scrollIntoViewIfNeeded();
    await shot(page, "system-status-check-it-phone");
  });

  test("check it: Wi-Fi was still on", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, reach: "wifi" });
    await page.goto("/admin/system/status");
    const card = page.locator("section", { has: page.getByRole("heading", { name: "Reachable from outside" }) });
    await card.getByRole("button", { name: "Check it" }).click();
    await expect(card.getByText("Your phone came from your own network (94.200.1.10)", { exact: false })).toBeVisible();
    await expect(card.getByRole("button", { name: "Try again with a new link" })).toBeVisible();
  });

  test("check it needs an admin", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: false });
    await page.goto("/admin/system/status");
    await expect(page.getByRole("button", { name: "Check it" })).toHaveCount(0);
  });

  test("restart asks first and says what it does", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, phoneSystemDown: true });
    await page.goto("/admin/system/status");
    await page.getByRole("button", { name: /Phone system/ }).click();
    await page.getByRole("button", { name: "Restart", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Restart the phone system?" })).toContainText("Calls in progress end.");
    await shot(page, "system-status-restart");
    await page.getByRole("dialog").getByRole("button", { name: "Restart", exact: true }).click();
    await expect(page.getByText("Restarted.")).toBeVisible();
  });

  test("the database can't be restarted from here", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true });
    await page.goto("/admin/system/status");
    await page.getByRole("button", { name: /Database/ }).click();
    await expect(page.getByRole("button", { name: "Restart", exact: true })).toBeDisabled();
  });

  test("the server helper isn't running", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, helperMissing: true });
    await page.goto("/admin/system/status");
    await expect(page.getByText("The server helper isn't running")).toBeVisible();
    await expect(page.getByText("sudo linx setup")).toBeVisible();
    await shot(page, "system-status-no-helper");
  });
});

// The web install's plain page (docs/ui/INSTALL_SCREENS.md §2 and §7), in
// both colour schemes and at phone width.
for (const [label, opts] of [["light", { colorScheme: "light", timezoneId: "Asia/Dubai" }], ["dark", { colorScheme: "dark", timezoneId: "Asia/Dubai" }],
  ["phone", { colorScheme: "light", timezoneId: "Asia/Dubai", viewport: { width: 390, height: 844 } }]] as const) {
  test.describe(`install ${label}`, () => {
    test.use(opts);

    test("rented server, start to finish", async ({ page }) => {
      await fakeInstall(page, "rented");
      await page.goto("/install");
      await expect(page.getByRole("heading", { name: "Let's set up Linx" })).toBeVisible();
      await expect(page.getByText("This page uses a temporary certificate, so your browser can't tell it's really your server.", { exact: false })).toBeVisible();
      await expect(page.getByRole("timer")).toContainText(/This link closes in 3:4[67]:\d\d\./);
      await expect(page.getByRole("timer")).toContainText("sudo linx setup --new-link");
      await shot(page, `install-claim-${label}`);
      await page.getByRole("button", { name: "Start" }).click();

      await expect(page.getByRole("heading", { name: "Where is this server?" })).toBeVisible();
      await expect(page.getByRole("radio", { name: /Rented server/ })).toBeChecked();
      await shot(page, `install-where-${label}`);
      await page.getByRole("button", { name: "Next" }).click();

      await expect(page.getByRole("heading", { name: "How do people reach this server from the internet?" })).toBeVisible();
      await page.getByRole("radio", { name: /Nothing else uses port 443/ }).click();
      await page.getByRole("button", { name: "Something else already uses port 443 here" }).click();
      // Needs a home network; unlocking proxies aren't offered on a rented server.
      await expect(page.getByRole("radio", { name: /Another program passes Linx through/ })).toBeDisabled();
      await expect(page.getByRole("radio", { name: /unlock the traffic/ })).toHaveCount(0);
      await shot(page, `install-front-door-rented-${label}`);
      // Another public port (advanced, ADR-064): the owner's warning first.
      await page.getByRole("button", { name: "Advanced" }).click();
      await page.getByRole("radio", { name: /use another public port/ }).click();
      await expect(page.getByText("Linx is designed and tuned to work best on port 443")).toBeVisible();
      await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
      await shot(page, `install-public-port-warning-${label}`);
      await page.getByRole("button", { name: "Use another port" }).click();
      await page.getByLabel("Public web port").fill("6666");
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByText("6666 is blocked by browsers: choose another, like 8443.")).toBeVisible();
      await page.getByLabel("Public web port").fill("8443");
      await expect(page.getByText("Linx will open TCP 8443 and UDP 443 on this server's firewall")).toBeVisible();
      await shot(page, `install-public-port-${label}`);
      await page.getByRole("radio", { name: /Nothing else uses port 443/ }).click();
      await page.getByRole("button", { name: "Next" }).click();

      await expect(page.getByRole("heading", { name: "What's your domain?" })).toBeVisible();
      await page.getByLabel("Domain").fill("203.0.113.5");
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("alert")).toHaveText("That's an address, not a domain. It should look like example.com.");
      await page.getByLabel("Domain").fill("co.uk");
      await shot(page, `install-domain-${label}`);
      await page.getByRole("button", { name: "Next" }).click();

      await expect(page.getByRole("heading", { name: "Who's setting this up?" })).toBeVisible();
      await page.getByLabel("Your name").fill("Mohammed AlMudharreb");
      await page.getByLabel("Email you'll sign in with").fill("mohammed@example.com");
      // The certificate email follows until changed; it may differ.
      await expect(page.getByLabel("Email for certificate notices")).toHaveValue("mohammed@example.com");
      await page.getByLabel("Email for certificate notices").fill("it@example.com");
      await expect(page.getByText("This is the system admin account, the highest authority over Linx.")).toBeVisible();
      // The browser's zone, and the rented server's own UTC clock named.
      await expect(page.getByLabel("Your time zone")).toContainText("Asia/Dubai");
      await expect(page.getByText("This server's own clock is set to Etc/UTC")).toBeVisible();
      await page.getByRole("button", { name: "Check and get a certificate" }).click();
      await expect(page.getByRole("alert")).toContainText("Tick the box to agree");
      await page.getByRole("checkbox").click();
      await shot(page, `install-you-${label}`);
      await page.getByRole("button", { name: "Check and get a certificate" }).click();

      // The host refuses co.uk: back to the domain step, with its words.
      await expect(page.getByRole("heading", { name: "What's your domain?" })).toBeVisible();
      await expect(page.getByRole("alert")).toHaveText("co.uk is shared by everyone. Use your own domain.");
      await page.getByLabel("Domain").fill("example.com");
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByRole("button", { name: "Check and get a certificate" }).click();

      // The certificate page: the record to add, and what DNS says now.
      await expect(page.getByRole("heading", { name: "Getting a certificate for example.com" })).toBeVisible();
      await expect(page.getByText("Add these 2 records at your DNS company")).toBeVisible();
      await expect(page.getByText("turn.example.com")).toBeVisible();
      await expect(page.getByText("Still points at 198.51.100.7.")).toBeVisible();
      await expect(page.getByText("Not found yet.")).toBeVisible();
      await expect(page.getByText("Set up")).toHaveCount(0);
      await expect(page.getByRole("radio", { name: /Not now: I'll add 2 records/ })).toBeChecked();
      await shot(page, `install-waiting-dns-${label}`);

      // The other way: the token now, and Linx adds the records itself.
      await page.getByRole("radio", { name: /With my DNS company's token/ }).click();
      await expect(page.getByText("Add these 2 records at your DNS company")).toHaveCount(0);
      await page.getByLabel("I checked the fingerprint, or I trust this network").click();
      await page.getByLabel("API token", { exact: true }).fill("t".repeat(40));
      await shot(page, `install-token-first-${label}`);
      await page.getByRole("button", { name: "Get the certificate" }).click();
      await expect(page.getByText("Linx points your names at this server")).toBeVisible();
      await expect(page.getByText("Saved on the server.")).toBeVisible();
    });

    test("certificate: the front-door card first", async ({ page }) => {
      await fakeInstall(page, "home", {
        accepted: { front_door: "proxy", proxy_address: "192.168.1.20" },
        cert: fakeCert({
          front_door: "proxy", dns: { state: "missing", names: [{ name: "example.com", state: "missing" }, { name: "turn.example.com", state: "missing" }] },
          add_records: [{ type: "A", name: "example.com", value: "5.36.12.4" }, { type: "A", name: "turn.example.com", value: "5.36.12.4" }],
          setup: doorSetup(),
        }),
      });
      await page.goto("/install");
      await expect(page.getByRole("heading", { name: "Getting a certificate for example.com" })).toBeVisible();
      await expect(page.getByText("Not found yet.")).toHaveCount(2);
      await expect(page.getByText("once you've added your token on the next page")).toBeVisible();
      await expect(page.getByRole("heading", { name: "Your front door needs to do three things" })).toBeVisible();
      // Pangolin's tab first: its Resources page can't pass by name, so it's Traefik's file.
      await expect(page.getByText("Pangolin's Resources page can't do this")).toBeVisible();
      await expect(page.getByText("linx-example-com-web:")).toBeVisible();
      await shot(page, `install-waiting-proxy-${label}`);
      if (label !== "phone") {
        await page.getByRole("tab", { name: "Caddy" }).click();
      } else {
        await page.getByRole("combobox", { name: "How to do this in" }).click();
        await page.getByRole("option", { name: "Caddy" }).click();
      }
      await expect(page.getByText("@linx_web tls sni example.com")).toBeVisible();
      await shot(page, `install-waiting-proxy-caddy-${label}`);
      await page.getByLabel("I've done these steps").click();
      await expect(page.getByLabel("I've done these steps")).toHaveCount(0);
      await page.getByRole("button", { name: "Show the steps again" }).click();
      await expect(page.getByLabel("I've done these steps")).toBeDisabled();
    });

    test("certificate: Let's Encrypt couldn't reach port 443", async ({ page }) => {
      await fakeInstall(page, "rented", {
        cert: fakeCert({
          dns: { state: "ok", names: [{ name: "example.com", state: "ok", seen: ["203.0.113.5"] }, { name: "turn.example.com", state: "ok", seen: ["203.0.113.5"] }] },
          reach: { state: "failed", kind: "connection", detail: "203.0.113.5: Timeout during connect (likely firewall problem)" },
        }),
      });
      await page.goto("/install");
      await expect(page.getByText("Let's Encrypt couldn't reach this server on port 443")).toBeVisible();
      await expect(page.getByText("Check that your server provider's firewall allows TCP port 443")).toBeVisible();
      await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
      await shot(page, `install-waiting-failed-${label}`);
    });

    test("certificate: token instead (home only)", async ({ page }) => {
      await fakeInstall(page, "home", { accepted: { front_door: "home-only" }, cert: fakeCert({ mode: "token", front_door: "home-only", add_records: undefined, dns: {} }) });
      await page.goto("/install");
      await expect(page.getByText("This page's certificate is temporary", { exact: true })).toBeVisible();
      await expect(page.getByRole("radio", { name: /With my DNS company's token/ })).toHaveCount(0);
      await expect(page.getByLabel("API token", { exact: true })).toBeDisabled();
      await page.getByLabel("I checked the fingerprint, or I trust this network").click();
      await page.getByLabel("API token", { exact: true }).fill("short");
      await shot(page, `install-token-fallback-${label}`);
      await page.getByRole("button", { name: "Get the certificate" }).click();
      await expect(page.getByRole("alert")).toContainText("doesn't look like a DNS provider token");
      await expect(page.getByLabel("API token", { exact: true })).toHaveValue("");
      await page.getByLabel("API token", { exact: true }).fill("t".repeat(40));
      await page.getByRole("button", { name: "Get the certificate" }).click();
      await expect(page.getByText("Saved on the server.")).toBeVisible();
    });

    test("certificate: waiting, with the time so far", async ({ page }) => {
      const at = new Date(Date.now() - 72_000).toISOString();
      await fakeInstall(page, "home", { accepted: { front_door: "home-only" }, cert: fakeCert({ mode: "token", front_door: "home-only", add_records: undefined, dns: {},
        token_saved: true, records: { state: "ok", at }, certificate: { state: "running", at } }) });
      await page.goto("/install");
      await expect(page.getByText("usually two or three minutes")).toBeVisible();
      await expect(page.getByText(/\(1:1\d so far\)/)).toBeVisible();
      await shot(page, `install-certificate-waiting-${label}`);
    });

    test("certificate ready, browser can't open it yet", async ({ page }) => {
      await fakeInstall(page, "rented", {
        cert: fakeCert({ dns: { state: "ok" }, reach: { state: "ok" }, certificate: { state: "ok" }, secure_url: "https://example.com" }),
      });
      await page.route("https://example.com/**", (route) => route.abort("namenotresolved"));
      await page.goto("/install");
      await expect(page.getByText("Your browser can't open")).toBeVisible();
      await expect(page.getByRole("button", { name: "Open it" })).toBeVisible();
      await shot(page, `install-waiting-ready-${label}`);
    });

    test("moves to the secure page", async ({ page, baseURL }) => {
      await fakeInstall(page, "rented", {
        cert: fakeCert({ dns: { state: "ok" }, reach: { state: "ok" }, certificate: { state: "ok" }, secure_url: "https://example.com" }),
      });
      await fakeSecureInstall(page, baseURL!);
      await page.goto("/install");
      await expect(page.getByRole("heading", { name: "You're on the secure page now" })).toBeVisible();
      expect(page.url()).toBe("https://example.com/install");
      await expect(page.getByText("This page uses a temporary certificate", { exact: false })).toHaveCount(0);
      await expect(page.getByRole("timer")).toContainText(/This setup page closes in (4:00:00|3:59:5\d)\./);
      await shot(page, `install-secure-arrive-${label}`);
    });

    test("secure page: token, extras, installing", async ({ page, baseURL }) => {
      await fakeSecureInstall(page, baseURL!);
      await page.goto("https://example.com/install");
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByRole("heading", { name: "Let Linx look after your DNS" })).toBeVisible();
      await expect(page.getByText("sip.example.com")).toHaveCount(0);
      await page.getByLabel("API token", { exact: true }).fill("short-token");
      await page.getByRole("button", { name: "Check and save" }).click();
      await expect(page.getByRole("alert")).toContainText("can't see example.com at Cloudflare");
      await shot(page, `install-dns-token-${label}`);
      await page.getByRole("button", { name: "Skip" }).click();
      await expect(page.getByRole("heading", { name: "A few extras" })).toBeVisible();
      await expect(page.getByRole("radio", { name: /Standard/ })).toBeChecked();
      await expect(page.getByRole("switch", { name: "Portainer" })).toHaveCount(0);
      await expect(page.getByText("Portainer is offered only on a server at home")).toBeVisible();
      await shot(page, `install-extras-${label}`);
      await page.getByRole("button", { name: "Install" }).click();
      await expect(page.getByRole("heading", { name: "Installing Linx" })).toBeVisible();
      await expect(page.getByRole("list", { name: "Install steps" }).getByRole("listitem")).toHaveCount(10);
      await expect(page.getByText("K7QM-2XPD-9RTA-LW4E-HB6N-C3VY")).toBeVisible();
      await shot(page, `install-progress-${label}`);
    });

    test("secure page at home: the token is needed, Portainer offered", async ({ page, baseURL }) => {
      await fakeSecureInstall(page, baseURL!, { where: "home" });
      await page.goto("https://example.com/install");
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.getByText("adds sip.example.com for your desk phones")).toBeVisible();
      await expect(page.getByRole("button", { name: "Skip" })).toHaveCount(0);
      await expect(page.getByText("At home your desk phones need sip.example.com")).toBeVisible();
      await page.getByLabel("API token", { exact: true }).fill("t".repeat(40));
      await page.getByRole("button", { name: "Check and save" }).click();
      await expect(page.getByRole("heading", { name: "A few extras" })).toBeVisible();
      await page.getByLabel("Portainer").click();
      await expect(page.getByLabel("Portainer")).toBeChecked();
      await page.getByRole("button", { name: "Back" }).click();
      await expect(page.getByText("Cloudflare key added")).toBeVisible();
    });

    test("the install stops", async ({ page, baseURL }) => {
      await fakeSecureInstall(page, baseURL!, { installed: true, failAt: 3 });
      await page.goto("https://example.com/install");
      await expect(page.getByRole("heading", { name: "The install stopped" })).toBeVisible();
      await expect(page.getByText("no space left on device")).toBeVisible();
      await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
      await shot(page, `install-progress-failed-${label}`);
    });

    test("moves to the first sign-in once Linx answers", async ({ page, baseURL }) => {
      const fake = await fakeSecureInstall(page, baseURL!, { installed: true });
      await page.goto("https://example.com/install");
      await expect(page.getByRole("heading", { name: "Installing Linx" })).toBeVisible();
      let linxUp = false;
      await page.route("https://example.com/api/v1/sign-in-options", (route) =>
        linxUp ? route.fulfill({ json: { password: true, providers: [] } }) : route.fulfill({ status: 404, contentType: "text/html", body: "" }));
      await page.route("https://example.com/api/v1/setup-links/*", (route) => route.fulfill({ json: { email: "mohammed@example.com", name: "Mohammed" } }));
      fake.switchOver();
      await expect(page.getByText("The installer's first page (port 6464) is now closed for good")).toBeVisible();
      linxUp = true;
      await expect(page.getByText("Tick “I've written these down” to go to your first sign-in")).toBeVisible({ timeout: 15_000 });
      await page.getByLabel("I've written these down").click();
      await page.waitForURL("https://example.com/setup/" + "s".repeat(43));
    });

    // Found on the VPS demo: an update after the switch came without the
    // things to write down (the server had wiped them), and the page moved
    // on to the sign-in without the tick.
    test("waits for the tick even when the update loses what to write down", async ({ page, baseURL }) => {
      const fake = await fakeSecureInstall(page, baseURL!, { installed: true });
      await page.goto("https://example.com/install");
      await expect(page.getByText("K7QM-2XPD-9RTA-LW4E-HB6N-C3VY")).toBeVisible();
      await page.route("https://example.com/api/v1/sign-in-options", (route) => route.fulfill({ json: { password: true, providers: [] } }));
      await page.route("https://example.com/api/v1/setup-links/*", (route) => route.fulfill({ json: { email: "mohammed@example.com", name: "Mohammed" } }));
      fake.switchOver({ keepWiped: true });
      await expect(page.getByText("Tick “I've written these down” to go to your first sign-in")).toBeVisible({ timeout: 15_000 });
      await expect(page.getByText("K7QM-2XPD-9RTA-LW4E-HB6N-C3VY")).toBeVisible();
      expect(page.url()).toBe("https://example.com/install");
      await page.getByLabel("I've written these down").click();
      await page.waitForURL("https://example.com/setup/" + "s".repeat(43));
    });

    test("a used handoff", async ({ page, baseURL }) => {
      await fakeSecureInstall(page, baseURL!, { usedHandoff: true });
      await page.goto("https://example.com/install/continue#" + "h".repeat(43));
      await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
      await expect(page.getByText("work once, for two minutes")).toBeVisible();
      expect(page.url()).toBe("https://example.com/install/continue");
    });

    test("at home, behind another program", async ({ page }) => {
      await fakeInstall(page, "home");
      await page.goto("/install");
      await page.getByRole("button", { name: "Start" }).click();
      await expect(page.getByRole("radio", { name: /At home or at the office/ })).toBeChecked();
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("heading", { name: "What's in front of Linx on the internet?" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
      // Advanced: a proxy that unlocks the traffic asks first.
      await page.getByRole("button", { name: "Advanced" }).click();
      await page.getByRole("radio", { name: /My proxy must unlock the traffic itself/ }).click();
      await expect(page.getByRole("alert")).toContainText("Not recommended");
      await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
      await shot(page, `install-front-door-unlock-${label}`);
      await page.getByRole("button", { name: "Show me how" }).click();
      await expect(page.getByRole("radio", { name: /Another program passes Linx through/ })).toBeChecked();
      await page.getByLabel("Address of the machine it runs on").fill("8.8.8.8");
      await page.getByRole("button", { name: /Call audio port: 443/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("alert")).toHaveText("8.8.8.8 isn't a home-network address.");
      await page.getByLabel("Address of the machine it runs on").fill("192.168.1.20");
      await shot(page, `install-front-door-home-${label}`);
      await page.getByRole("button", { name: "Next" }).click();
      await page.getByLabel("Domain").fill("pbx.example.com");
      await expect(page.getByText("sip.pbx.example.com")).toBeVisible();
      // A reload comes back to the same step (the draft kept on the server).
      await page.waitForTimeout(600);
      await page.reload();
      await expect(page.getByRole("heading", { name: "What's your domain?" })).toBeVisible();
      await expect(page.getByLabel("Domain")).toHaveValue("pbx.example.com");
    });

    test("link about to close", async ({ page }) => {
      await fakeInstall(page, "rented", { expiresIn: 242 });
      await page.goto("/install");
      await page.getByRole("button", { name: "Start" }).click();
      await expect(page.getByRole("timer")).toContainText(/This link closes in 4:0\d\./);
      await shot(page, `install-closing-${label}`);
    });

    test("link runs out while open", async ({ page }) => {
      await fakeInstall(page, "rented", { expiresIn: 2 });
      await page.goto("/install");
      await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible({ timeout: 5000 });
      await expect(page.getByText("Answers you already gave are kept")).toBeVisible();
    });

    test("link can't be used", async ({ page }) => {
      await fakeInstall(page, "rented", { closed: true });
      await page.goto("/install");
      await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
      await shot(page, `install-link-unusable-${label}`);
    });
  });
}

// Safari (WebKit) must be able to reload a signed-in page: the tab lock for
// the phone line used to block it, on every page (owner, Phase 1E demo,
// iPhone and iPad).
test.describe("safari", () => {
  test("a signed-in page reloads", async ({ playwright, baseURL }) => {
    const browser = await playwright.webkit.launch();
    try {
      const page = await (await browser.newContext({ baseURL })).newPage();
      await fakeServer(page, { signedIn: true });
      await page.goto("/");
      await expect(page.getByTestId("account-menu")).toContainText("Available");
      await page.reload({ waitUntil: "load", timeout: 10_000 });
      await expect(page.getByTestId("account-menu")).toContainText("Available");
    } finally {
      await browser.close();
    }
  });
});
