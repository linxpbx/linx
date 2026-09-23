/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    // Keep builds CSP-friendly: no inlined scripts or data: module URLs.
    assetsInlineLimit: 0,
  },
  test: {
    environment: "jsdom",
    globals: true,
  },
});
