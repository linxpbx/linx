import { defineConfig } from "@playwright/test";

// Screenshot tests (e2e/screens.spec.ts) run against the built app with a
// stand-in server. The browser call suite has its own config
// (e2e/calls.config.ts) and runs against the real stack in Docker.
export default defineConfig({
  testDir: "e2e",
  testMatch: "screens.spec.ts",
  timeout: 60_000,
  reporter: [["dot"]],
  use: {
    baseURL: "http://localhost:4174",
    viewport: { width: 1440, height: 900 },
    permissions: ["microphone"],
    launchOptions: { args: ["--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream"] },
  },
  webServer: {
    command: "npx vite build --logLevel error && npx vite preview --port 4174 --strictPort",
    url: "http://localhost:4174",
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
