# Linx product site (linxpbx.com)

The public marketing and help site for Linx. Built with [Astro](https://astro.build)
(static output, near-zero JavaScript), styled from the Linx brand tokens
(`design/tokens.json`). Help/support pages are Markdown files under
`src/content/docs/` — add or edit one and push to update the live site.

## Develop

```sh
cd site
npm install        # first time (pinned versions in package.json)
npm run dev        # http://localhost:4321
npm run build      # static site into dist/
npm run preview    # serve the built dist/ locally
```

## Structure

```
site/
  src/
    pages/            index.astro (landing), download.astro, docs/
    layouts/          Base.astro, DocsLayout.astro
    components/       Logo, Header, Footer, InstallCommand
    content/docs/     one Markdown file per help page  ← content to keep updated
    styles/brand.css  colours, type, radii from design/tokens.json
  public/             favicon.svg and other static assets
```

### Adding a help page

Create `src/content/docs/<name>.md` with frontmatter and push:

```md
---
title: Ring groups
description: Ring a whole team at once.
section: Guides      # groups it in the sidebar
order: 10            # sorts within the section
---

Your content…
```

It appears at `/docs/<name>` and in the sidebar automatically.

## Deploy — Cloudflare Pages (git-driven)

The site deploys from this repo on every push, so updating content is just a
commit. One-time setup in the Cloudflare dashboard:

1. **Workers & Pages → Create → Pages → Connect to Git**, pick `linxpbx/linx`.
2. Build settings:
   - **Production branch:** `master`
   - **Framework preset:** Astro
   - **Build command:** `npm run build`
   - **Build output directory:** `dist`
   - **Root directory:** `site`
3. Deploy. Then **Custom domains** → add `linxpbx.com` and `www.linxpbx.com`
   (Cloudflare adds the DNS records if the domain is on Cloudflare).

After that, every `git push` to `master` that touches `site/` rebuilds and
redeploys automatically — no tokens on any developer machine. A one-off manual
deploy is possible with `npx wrangler pages deploy dist` if ever needed.
