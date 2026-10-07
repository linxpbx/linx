// @ts-check
import { defineConfig } from 'astro/config';
import remarkHelp from './src/lib/remark-help.mjs';

// The Linx product site (https://linxpbx.com). Static output, no client-side
// JavaScript unless a component opts in. Built with `npm run build` into
// `dist/` and deployed on Cloudflare (wrangler.jsonc). Help pages are the
// server's own guides (docs/help), published here as they are; a few
// website-only pages live in src/content/docs.
export default defineConfig({
  site: 'https://linxpbx.com',
  // download.html rather than download/index.html, so /download is served
  // directly instead of being redirected to /download/ on every click.
  build: { format: 'file' },
  markdown: { remarkPlugins: [remarkHelp] },
});
