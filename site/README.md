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

## TestFlight requests (`/beta`)

People ask to test the iPhone/iPad app at **linxpbx.com/beta**. The request is
emailed to the owner with one **Review** link (the request travels inside it,
signed; nothing is stored), whose page has **Approve** and **Reject**.
Approve adds the person to the TestFlight group through the App Store Connect
API, and Apple emails them the invitation. Code: `worker/index.ts`.

Until everything below is set, the page says requests aren't open yet.

**Apple (App Store Connect):**

1. **TestFlight → External Testing → +**: a group, e.g. *Public testers*. Add
   the build, fill in **Test Information** (what to test, feedback email,
   `https://linxpbx.com/privacy` as the privacy policy, and sign-in details
   for a demo server), and submit it for **Beta App Review** (the first
   build of each version; usually a day). The group's id is the last part
   of its address in App Store Connect.
2. **Users and Access → Integrations → App Store Connect API → +**: a key
   with the **App Manager** role, just for this (not the upload key).
   Download the `.p8` (once only) and note the **Key ID** and **Issuer ID**.

**Cloudflare (linxpbx.com):**

3. **Email → Email Routing**: turn it on, and add the owner's email as a
   **destination address** (Cloudflare sends a confirmation to click).
4. **Turnstile → Add widget**: hostname `linxpbx.com`, mode *Managed*. Note
   the **site key** and **secret key**.
5. **Workers & Pages → linx → Settings → Variables and Secrets**, add:

   | Name | Type | Value |
   |---|---|---|
   | `OWNER_EMAIL` | Text | the destination address from step 3 |
   | `MAIL_FROM` | Text | `testflight@linxpbx.com` |
   | `TURNSTILE_SITE_KEY` | Text | from step 4 |
   | `TURNSTILE_SECRET` | Secret | from step 4 |
   | `REVIEW_SECRET` | Secret | 64 random hex characters (`openssl rand -hex 32`) |
   | `ASC_KEY_ID` | Text | from step 2 |
   | `ASC_ISSUER_ID` | Text | from step 2 |
   | `ASC_PRIVATE_KEY` | Secret | the whole `.p8` file's contents |
   | `ASC_BETA_GROUP_ID` | Text | from step 1 |

Changing `REVIEW_SECRET` makes every review link already sent stop working.
Local testing: put the same names in `site/.dev.vars` (git-ignored; Turnstile
has test keys that always pass) and run `npx wrangler dev`.
