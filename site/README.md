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

## Deploy — Cloudflare (Workers static assets, git-driven)

The site deploys from this repo on every push, so updating content is just a
commit. `wrangler.jsonc` tells Cloudflare to serve the built `dist/` folder;
no server code runs. One-time setup in the Cloudflare dashboard:

1. **Workers & Pages → Create → Import a repository**, pick `linxpbx/linx`.
2. Fill in:
   - **Project name:** `linx`
   - **Build command:** `npm run build`
   - **Deploy command:** `npx wrangler deploy`
   - **Preview command:** `npx wrangler versions upload`
   - **Advanced settings → Path:** `/site`  ← the site lives in this folder
   - **API token:** leave "Create new token" (Cloudflare makes it); no variables needed.
3. **Deploy.** Then the project's **Settings → Domains & Routes → Add →
   Custom domain**: `linxpbx.com`, then again for `www.linxpbx.com`.

After that, every `git push` to `master` rebuilds and redeploys automatically —
no tokens on any developer machine. Check locally with
`npm run build && npx wrangler deploy --dry-run`.
