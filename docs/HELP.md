# Linx — Help pages and search

*Status: design drafted 2026-09-28 from the owner's request (no public website yet), **approved by the owner 2026-09-28**: inside every Linx, smart search plus optional AI answers, signed-in people only except the sign-in guides. ADR-060 records the decisions.*

## 1. In plain words

Every Linx server has **Help** at `https://<domain>/help`, for anyone signed in (the few sign-in guides open to everyone, §8 item 3). It holds the how-to guides for everyone who uses Linx (people, admins, the owner) and a search box that takes plain questions ("how do I add a desk phone?", "why can't my phone register?"). Search always works, on the server itself, private and free. An admin can also turn on **written AI answers**, made only from the guides, with a provider they choose. The guides ship inside each Linx release, so upgrading a server updates its help, and a server's help always matches its own version.

## 2. The guides

- **Where they're written:** `docs/help/*.md` in the repo, one file per guide, in the same plain words as the admin screens (no telecom jargon). Front matter: `title`, `audience` (`public` for the sign-in guides, `everyone`, `admin` or `system_admin`), `section`, `keywords` (other words people use, e.g. "extension" for "phone number"), `screens` (the app routes the guide is about).
- **Not the design docs.** `docs/*.md` stays the design and security record, for developers. Help guides link to nothing in it.
- **First set** (owner: all four areas):
  1. *Installing and moving:* the web install step by step, each front door (Pangolin, nginx or HAProxy, Caddy or Nginx Proxy Manager, Linx takes 443, home only), DNS and the token, certificates, running setup again, Server settings, the repair page, moving to a new server.
  2. *Everyday use:* signing in, passkeys and the authenticator app, lost authenticator, calls in the browser, the Team list and presence, the echo test (`*43`), microphone and speaker settings.
  3. *Admin tasks:* people and invite links, extensions, desk phones and phone apps (with the settings box explained), phone lines (a provider, a Grandstream UCM, WireGuard), incoming numbers, calling permissions, "admins only from my home network", company sign-in.
  4. *Keeping it running:* backups (schedule, places, download) and restore, System status and Restart, alerts and where they go, `linx doctor`, and a "something's wrong" guide per symptom (can't open the address, phone won't register, no audio, line down).
- **Pictures:** the screenshots `make screens` already takes, copied in by the build, so they're refreshed with every release too (light and dark).
- **Who sees what:** the guides and search results are filtered by the person's role: admin guides for admins, system admin guides for system admins.

## 3. Search (always on)

- **Runs in the control plane**, over an index built into the image from `docs/help` (no database table, nothing to keep in sync). `GET /api/v1/help/search?q=…` (any signed-in session, no scope, like `/me`; API keys can't use it) returns the best sections, each with its guide, heading and the lines that matched.
- **How it understands plain questions:** each guide is split into its sections; a question is cut into words, question words and filler are dropped ("how", "do", "I", "can't"), words are reduced to their stem ("registering" → "register"), and the guides' own `keywords` plus a small list of everyday words for Linx terms ("number" → extension, "phone company" → phone line, "log in" → sign in) widen the match. Sections are ranked with BM25, the standard ranking behind most search boxes, with titles and headings counting more. A few hundred lines of Go, no library, a few megabytes of memory.
- **Limits, honestly:** this finds the right sections for questions worded most ways, but it doesn't *understand* them. A question with none of the guides' words finds nothing, and the page then says so and offers the guide list. The AI answers (§4) fill that gap where they're turned on.
- **Help from where you are:** every screen gets a **?** button that opens the guide for that screen (from `screens` in front matter).

## 4. Written AI answers (optional, off by default)

- **Turned on by an admin** at System → Settings → Help answers: provider **Ollama** (a model on your own computer or network), **Anthropic (Claude)** or **OpenAI-compatible**, its address (Ollama and OpenAI-compatible) and API key (sealed in the database with ADR-030's key, never shown again), and the model. Suggested models: Claude Haiku 4.5 (`claude-haiku-4-5`, fast and cheap), or a small local model for Ollama. **Test** sends one question and shows the answer. Changing it needs `settings:write` and "confirm it's you"; audited without the key.
- **How an answer is made:** the question goes to the search in §3 first; the best sections (at most about 3,000 words) go to the model with fixed instructions: answer only from these sections, in plain words, say "the guides don't cover that" otherwise, and name the guides used. The page shows the answer as it's written, with links to those guides under it, and the search results below it either way.
- **What leaves the server:** only the question and the chosen sections, and only to the provider the admin chose; nothing about the server, its people or its calls. Connections go through the SSRF guard (docs/API.md); an Ollama on the home network is added to the outbound allowlist, as for webhooks. The page says "Answers are written by <provider> from Linx's guides" under every answer.
- **Limits:** 10 questions a minute and 200 a day per person, 1,000 a day per server (settable), so a person can't run up a bill. A question that's refused, too long (500 characters) or times out (30 s) falls back to the search results.
- **Not an assistant:** it answers from the guides only; it can't see or change anything on the server. (The MCP assistant that can is a later phase, ROADMAP Phase 6, with its own confirmations.)

## 5. Keeping it up to date by itself

- **Every release carries its own help:** the image's build step builds the search index and the guide pages from `docs/help` at that commit. Upgrading the server is updating its help; nothing is fetched from anywhere.
- **Guides can't fall behind silently:**
  - A test fails if a screen route in the web app has no guide naming it in `screens`, or a guide names a route that no longer exists.
  - A test fails if a guide links to a guide or heading that doesn't exist, or uses a screenshot `make screens` no longer makes.
  - A test fails if a guide quotes a button or heading in `**bold**` that no screen has any more (the text is checked against the built web app), which catches renamed buttons.
  - Working rule (CLAUDE.md): a change people can see updates its guide in the same commit, and each step's "As built" says which guides changed.
- **What's new:** `docs/help/whats-new.md` gets a short entry per release, in plain words; Help shows it first after an upgrade.

## 6. Threat model notes

- Guides hold nothing secret, but are for signed-in people only (owner decision), so the API and the pages need a session. The exception is guides marked `audience: public` (signing in, passkeys, lost authenticator, locked out, set-password links), served without a session and linked from the sign-in page; search without a session looks only at those, and AI answers always need a session.
- **No way from the open sign-in guides to anything else (owner requirement, 2026-09-28).** Nobody can change a guide's address, a search, or anything else in a request to reach a guide or answer they aren't allowed:
  - **The server decides, every time.** What a caller may read is worked out on the server from their session (none, or their role), on every request: the page, the guide, each picture, search results, AI answers. Hiding a link in the web app is never the protection; the web app only shows what the server already allowed.
  - **Two separate indexes, built apart.** The build makes a *public* index holding only `audience: public` guides (their text, headings and pictures) and a *full* one for signed-in people. Requests without a session only ever read the public index: a guide or picture that isn't in it can't be found there, whatever the address says. Search without a session searches only that index, so its results and "matched lines" can't quote any other guide.
  - **Addresses are names, not paths.** A guide is asked for by its name (`/help/lost-authenticator`), checked against `^[a-z0-9-]{1,64}$` and looked up in the index; nothing in an address is ever turned into a file path, so `..`, `%2e%2e`, encoded slashes and the like have nothing to reach. Pictures are served only when a guide the caller may read uses them.
  - **One answer for "not there" and "not for you".** A guide that doesn't exist and one the caller may not read get the same 404, the same body and the same timing, so nobody can list the other guides' names from outside.
  - **Fails closed.** A guide with no `audience`, an unknown one, or a typo fails the build: nothing is ever public by default. Only a fixed list of guides may be `public` (the sign-in ones); a test fails if any other guide is marked public, so opening one needs a deliberate code change and review.
  - **Roles inside, too.** A signed-in person without admin rights gets the same 404 for an admin guide, by the same rule.
  - **Limits.** Requests without a session are rate-limited per address, like sign-in.
  - **Tested:** without a session, every non-public guide by name, by altered names (case, encodings, `..`, trailing slashes, extra path parts, query strings), its pictures and search words found only in it all come back 404 or empty; the same for a person's role and admin guides; the public guides all work.
- The index is built from files in the image: no admin can edit guide text on a server, so a guide can't be changed into instructions that trick people ("paste this token here").
- AI answers: prompt injection through the question can only make the model say odd things to the person asking; it has no tools and sees nothing but the guides. Its answer is shown as text, never as HTML. The API key is sealed and only ever sent to the provider's own address.

## 7. Build order (one step per session)

1. **Guides, first set:** `docs/help/` with its front-matter rules, the four areas in §2, whats-new, and the checks in §5 (routes, links, screenshots, bold text).
2. **Help pages + search:** index built into the image, `GET /api/v1/help/…`, the Help screen (guide list by area, guide page, search box with results), the **?** button on every screen. `make screens` shots.
3. **AI answers:** settings and sealed key, providers (Anthropic, OpenAI-compatible, Ollama), streaming answers, limits, audit, Settings screen, threat model rows.
4. **Review + demo:** security review of steps 2–3 and a short demo on the lab server.

## 8. Owner decisions (2026-09-28)

1. **Where:** inside every Linx, shipped with each release (recommended; chosen).
2. **Search:** smart search always, written AI answers optional with a provider the admin picks, off by default (recommended; chosen).
3. **Who:** signed-in people only (owner's choice; the recommendation was to leave the guides open to everyone and keep AI answers for signed-in people, so someone who can't sign in could still read "lost authenticator"). *Consequence:* someone who can't sign in can't read "lost authenticator" or "locked out"; their admin has to tell them the steps. **Owner decision (2026-09-28): open the sign-in guides too.** Guides with `audience: public` (signing in, passkeys and the authenticator app, lost authenticator, locked out, set-password links) open without signing in, from a **Help signing in** link on the sign-in page; everything else and AI answers need a session.
4. **First guides:** all four areas (the question went unanswered; taken as all).
