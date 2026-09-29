// Screenshots of every Phase 1C screen against a stand-in server
// (e2e/fakes.ts), for comparison with docs/ui (the front-end rule in
// CLAUDE.md). `npm run screens` writes them to e2e/screenshots/.
import { expect, test, type Page } from "@playwright/test";
import { fakeCert, fakeInstall, fakeSecureInstall, fakeServer } from "./fakes";

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

const shot = async (page: Page, name: string) => {
  await noSidewaysScroll(page, name);
  await page.screenshot({ path: `e2e/screenshots/${name}.png`, animations: "disabled", caret: "hide", timeout: 15_000 });
};

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

    test("my account", async ({ page }) => {
      await fakeServer(page, { signedIn: true, company: true });
      await page.goto("/account?company=linked");
      await expect(page.getByRole("list", { name: "Your passkeys" })).toContainText("Mohammed's iPhone");
      await expect(page.getByRole("list", { name: "Company accounts" })).toContainText("Google: mohammed@example.com");
      await expect(page.getByRole("button", { name: "Link Microsoft" })).toBeVisible();
      await expect(page.getByRole("status")).toHaveText("Linked.");
      await shot(page, `${scheme}-account`);
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

test.describe("setup wizard", () => {
  test("place", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 1 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Where will you use Linx?" })).toBeVisible();
    await shot(page, "setup-wizard-place");
    await page.getByRole("button", { name: "Business" }).click();
    await expect(page.getByRole("button", { name: "Next" })).toBeEnabled();
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
    await page.getByLabel("Email").last().fill("sara@");
    await page.getByLabel("Extension").last().fill("99999");
    await expect(page.getByText("That doesn't look like an email address.", { exact: false })).toBeVisible();
    // One button: Next becomes "Create 1 extension", off until the row is right.
    await expect(page.getByRole("button", { name: "Create 1 extension" })).toBeDisabled();
    await page.getByRole("button", { name: "Skip for now" }).click();
    await expect(page.getByText("Create the extensions above first", { exact: false })).toBeVisible();
    await shot(page, "setup-wizard-people-problems");
    await page.getByLabel("Email").last().fill("sara@example.com");
    await page.getByLabel("Extension").last().fill("102");
    await expect(page.getByRole("button", { name: "Create 1 extension" })).toBeEnabled();
    // A door phone: a name and a number, no email (an extension, no person).
    await page.getByRole("button", { name: "+ Add another row" }).click();
    await page.getByLabel("Name").last().fill("Front door");
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
    ["/repair", "repair", "Fix this server's address"],
  ] as const) {
    test(`no sideways scrolling: ${name}`, async ({ page }) => {
      await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 5, backups: true, download: "ready", serverSettings: "home", repair: "sign-in", moved: true });
      await page.goto(path);
      await expect(page.getByRole("heading", { name: ready, exact: true }).first()).toBeVisible();
      await shot(page, `phone-${name}`);
    });
  }

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
    await expect(page.getByText("Cloudflare token added ✓")).toBeVisible();
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

  test("rented: no Portainer, a token to add", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
    await page.goto("/admin/system/server");
    await expect(page.getByText("No token: the certificate renews through port 443")).toBeVisible();
    await expect(page.getByLabel("Portainer")).toHaveCount(0);
    await page.getByRole("button", { name: "Add a token" }).click();
    await page.getByLabel("New Cloudflare token").fill("short");
    await page.getByRole("button", { name: "Apply" }).click();
    // Checked before anything is asked.
    await expect(page.getByRole("alert")).toContainText("can't see example.com at Cloudflare");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("at home: a new domain shows what it needs first", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "home" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Change" }).nth(1).click();
    await expect(page.getByRole("button", { name: "Check" })).toBeDisabled();
    await page.getByLabel("New domain").fill("203.0.113.9");
    await page.getByRole("button", { name: "Check" }).click();
    await expect(page.getByRole("alert")).toContainText("That's an address, not a domain");
    await page.getByLabel("New domain").fill("pbx.example.org");
    await page.getByRole("button", { name: "Check" }).click();
    await expect(page.getByRole("heading", { name: "Before you apply" })).toBeVisible();
    await expect(page.getByText("Passkeys only work at the address")).toBeVisible();
    await page.getByRole("button", { name: "Show the block for Pangolin" }).click();
    await expect(page.getByRole("button", { name: "Apply" })).toBeDisabled();
    await page.getByLabel("I've done these steps").click();
    await shot(page, "system-server-move");
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("dialog", { name: "Move Linx to https://pbx.example.org?" })).toBeVisible();
    await page.getByRole("dialog").getByRole("button", { name: "Apply" }).click();
    await expect(page.getByRole("list", { name: "Change steps" }).getByRole("listitem").first()).toContainText("Save your settings");
  });

  test("rented, no token: a new domain's DNS records to add first", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, serverSettings: "rented" });
    await page.goto("/admin/system/server");
    await page.getByRole("button", { name: "Change" }).first().click();
    await expect(page.getByRole("radio", { name: "Pangolin" })).toHaveCount(0);
    await page.getByRole("button", { name: "Change" }).first().click();
    await page.getByLabel("New domain").fill("pbx.example.org");
    // The front door's editor is open too: its Check stays off until it changes.
    await expect(page.getByRole("button", { name: "Check" }).first()).toBeDisabled();
    await page.getByRole("button", { name: "Check" }).last().click();
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
    await expect(page.getByText("Cloudflare token added ✓")).toBeVisible();
    await shot(page, "repair-settings");
  });

  test("a link that skips the sign-in", async ({ page }) => {
    await fakeServer(page, { repair: "no-sign-in", serverSettings: "rented" });
    await page.goto("/repair");
    await expect(page.getByRole("heading", { name: "Fix this server's address" })).toBeVisible();
    await page.getByRole("button", { name: "Change" }).nth(1).click();
    await page.getByLabel("New domain").fill("pbx.example.org");
    await page.getByRole("button", { name: "Check" }).click();
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

test.describe("system status", () => {
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
      await page.getByRole("radio", { name: /Directly/ }).click();
      await page.getByRole("button", { name: "Something else already uses port 443 here" }).click();
      await expect(page.getByRole("radio", { name: /Caddy/ })).toBeDisabled();
      await shot(page, `install-front-door-rented-${label}`);
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
      await page.getByLabel("Your email").fill("mohammed@example.com");
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
      await page.getByLabel("Token", { exact: true }).fill("t".repeat(40));
      await shot(page, `install-token-first-${label}`);
      await page.getByRole("button", { name: "Get the certificate" }).click();
      await expect(page.getByText("Linx points your names at this server")).toBeVisible();
      await expect(page.getByText("Saved on the server.")).toBeVisible();
    });

    test("certificate: Pangolin's block first", async ({ page }) => {
      await fakeInstall(page, "home", {
        accepted: { front_door: "pangolin", proxy_address: "192.168.1.20" },
        cert: fakeCert({
          front_door: "pangolin", dns: { state: "missing", names: [{ name: "example.com", state: "missing" }, { name: "turn.example.com", state: "missing" }] },
          add_records: [{ type: "A", name: "example.com", value: "5.36.12.4" }, { type: "A", name: "turn.example.com", value: "5.36.12.4" }],
          setup: {
            steps: ["On the Pangolin machine (192.168.1.20), add the block below to the end of config/traefik/dynamic_config.yml.",
              "On your router, keep TCP port 443 going to Pangolin (192.168.1.20), and send UDP port 443 to this server (192.168.1.212)."],
            files: [{ title: "the block for Pangolin", path: "config/traefik/dynamic_config.yml", text: "tcp:\n  routers:\n    linx-web:\n      entryPoints: [websecure]\n      rule: \"HostSNI(`example.com`) || HostSNI(`api.example.com`)\"\n" }],
          },
        }),
      });
      await page.goto("/install");
      await expect(page.getByRole("heading", { name: "Getting a certificate for example.com" })).toBeVisible();
      await expect(page.getByText("Not found yet.")).toHaveCount(2);
      await expect(page.getByText("once you've added your token on the next page")).toBeVisible();
      await page.getByRole("button", { name: "Show the block for Pangolin" }).click();
      await expect(page.getByText("linx-web:")).toBeVisible();
      await shot(page, `install-waiting-pangolin-${label}`);
      await page.getByLabel("I've done this").click();
      await expect(page.getByLabel("I've done this")).toBeDisabled();
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
      await expect(page.getByLabel("Token")).toBeDisabled();
      await page.getByLabel("I checked the fingerprint, or I trust this network").click();
      await page.getByLabel("Token").fill("short");
      await shot(page, `install-token-fallback-${label}`);
      await page.getByRole("button", { name: "Get the certificate" }).click();
      await expect(page.getByRole("alert")).toContainText("doesn't look like a DNS provider token");
      await expect(page.getByLabel("Token")).toHaveValue("");
      await page.getByLabel("Token").fill("t".repeat(40));
      await page.getByRole("button", { name: "Get the certificate" }).click();
      await expect(page.getByText("Saved on the server.")).toBeVisible();
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
      await page.getByLabel("Token").fill("short-token");
      await page.getByRole("button", { name: "Check and save" }).click();
      await expect(page.getByRole("alert")).toContainText("can't see example.com at Cloudflare");
      await shot(page, `install-dns-token-${label}`);
      await page.getByRole("button", { name: "Skip" }).click();
      await expect(page.getByRole("heading", { name: "A few extras" })).toBeVisible();
      await expect(page.getByRole("radio", { name: /Standard/ })).toBeChecked();
      await expect(page.getByText("Portainer")).toHaveCount(0);
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
      await page.getByLabel("Token").fill("t".repeat(40));
      await page.getByRole("button", { name: "Check and save" }).click();
      await expect(page.getByRole("heading", { name: "A few extras" })).toBeVisible();
      await page.getByLabel("Portainer").click();
      await expect(page.getByLabel("Portainer")).toBeChecked();
      await page.getByRole("button", { name: "Back" }).click();
      await expect(page.getByText("Cloudflare token added")).toBeVisible();
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

    test("a used handoff", async ({ page, baseURL }) => {
      await fakeSecureInstall(page, baseURL!, { usedHandoff: true });
      await page.goto("https://example.com/install/continue#" + "h".repeat(43));
      await expect(page.getByRole("heading", { name: "This link can't be used" })).toBeVisible();
      await expect(page.getByText("work once, for two minutes")).toBeVisible();
      expect(page.url()).toBe("https://example.com/install/continue");
    });

    test("at home, behind Pangolin", async ({ page }) => {
      await fakeInstall(page, "home");
      await page.goto("/install");
      await page.getByRole("button", { name: "Start" }).click();
      await expect(page.getByRole("radio", { name: /At home or at the office/ })).toBeChecked();
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("heading", { name: "What's in front of Linx on the internet?" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();
      await page.getByRole("radio", { name: /Pangolin/ }).click();
      await page.getByLabel("Address of the machine Pangolin runs on").fill("8.8.8.8");
      await page.getByRole("button", { name: /Call audio port: 443/ }).click();
      await page.getByRole("button", { name: "Next" }).click();
      await expect(page.getByRole("alert")).toHaveText("8.8.8.8 isn't a home-network address.");
      await page.getByLabel("Address of the machine Pangolin runs on").fill("192.168.1.20");
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
