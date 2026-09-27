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
      await fakeServer(page, { signedIn: true, admin: true, setupStep: 4 });
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
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 0 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Where will you use Linx?" })).toBeVisible();
    await shot(page, "setup-wizard-place");
    await page.getByRole("button", { name: "Business" }).click();
    await expect(page.getByRole("button", { name: "Next" })).toBeEnabled();
  });

  test("start: fresh or restore", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 0 });
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
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 0, restore: "failed" });
    await page.goto("/setup");
    await expect(page.getByRole("alert")).toContainText("wrong password");
    await expect(page.getByRole("textbox", { name: "Folder" })).toHaveValue("/var/backups/linx");
    await shot(page, "setup-wizard-restore-failed");
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 0, restore: "running" });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Restoring your backup" })).toBeVisible();
    await shot(page, "setup-wizard-restore-running");
  });

  test("restore from a backup file", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, systemAdmin: true, setupStep: 0 });
    await page.goto("/setup");
    await page.getByRole("button", { name: /Restore from a backup/ }).click();
    await page.getByRole("radio", { name: /A backup file on my computer/ }).click();
    await page.getByLabel("Backup file", { exact: true }).setInputFiles({ name: "linx-backup-2026-09-27-0300.tar", mimeType: "application/x-tar", buffer: Buffer.alloc(4096) });
    await expect(page.getByText("Size: 4 KB")).toBeVisible();
    await page.getByLabel("The backup's password").fill("q7Xk2pLm9RtV4wZs8NcB1yHd6FgJ3aEu0oTiPrKe5Ws");
    await page.getByRole("checkbox", { name: "I understand, replace everything" }).click();
    await shot(page, "setup-wizard-restore-file");
    await page.getByRole("button", { name: "Restore", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Starting the restore" })).toBeVisible();
  });

  test("numbers", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 2 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "How should extension numbers look?" })).toBeVisible();
    await expect(page.getByText("100–599")).toBeVisible();
    await shot(page, "setup-wizard-numbers");
    await page.getByText("Change the ranges").click();
    await shot(page, "setup-wizard-numbers-ranges");
  });

  test("people", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 3 });
    await page.goto("/setup");
    await expect(page.getByRole("heading", { name: "Who will use Linx?" })).toBeVisible();
    await page.getByRole("button", { name: "+ Add another row" }).click();
    await shot(page, "setup-wizard-people");
  });

  test("line and calls", async ({ page }) => {
    await fakeServer(page, { signedIn: true, admin: true, setupStep: 4 });
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
