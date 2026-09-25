import { defineConfig } from "@playwright/test";

// The browser call suite (e2e/calls.spec.ts) against the real stack. Run by
// internal/browsertest (make test-browser) inside Playwright's container on
// the stack's network; not meant to be run by hand.
export default defineConfig({
  testDir: ".",
  testMatch: "calls.spec.ts",
  timeout: 240_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  workers: 1,
  outputDir: "/tmp/linx-calls-results",
});
