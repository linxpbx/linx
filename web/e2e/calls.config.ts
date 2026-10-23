import { defineConfig } from "@playwright/test";

// The browser call suite (e2e/calls.spec.ts) against the real stack. Run by
// internal/browsertest (make test-browser) inside Playwright's container on
// the stack's network; not meant to be run by hand.
export default defineConfig({
  testDir: ".",
  testMatch: "calls.spec.ts",
  timeout: 420_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  // One retry in CI. "browsers call each other, one with UDP blocked" misses
  // its window for the sound to move onto the direct link now and then — it
  // failed on the public-port door on run 39886994277 and on nginx on
  // 39957708680, passing on the same door either side of each — and a rerun
  // of the whole job to settle it costs twenty minutes of everybody's time.
  // A retry costs forty seconds, a test that is really broken still fails
  // twice and turns the run red, and Playwright says "flaky" in the log, so
  // the underlying timing question stays visible instead of being buried.
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  outputDir: "/tmp/linx-calls-results",
});
