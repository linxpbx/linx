// @ts-check
import { defineConfig } from 'astro/config';

// The Linx product site (https://linxpbx.com). Static output, no client-side
// JavaScript unless a component opts in. Built with `npm run build` into
// `dist/` and deployed on Cloudflare Pages. Support/help pages are Markdown
// files under src/content/docs — add or edit one and push to update the site.
export default defineConfig({
  site: 'https://linxpbx.com',
});
