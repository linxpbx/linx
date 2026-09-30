# Help demo checklist

A short hand-run check that Help (`docs/HELP.md`, steps 1–3, with the step 4 review) does what it promises on a real server behind Pangolin. Tick each box. Allow about 30 minutes.

**Exit** (`docs/HELP.md` §7 step 4): on the lab server, someone not signed in reads only the sign-in guides; a person reads their guides and not the admin ones, whatever address they type; an admin reads everything, with pictures in light and dark; **?** opens the right guide; search finds plain questions; with written answers turned on, an answer appears word by word through Pangolin, from the guides only, with links, within the limits; the API key is never shown again. Automated: `make test`, `make test-docker`, `make screens` and the browser suite are green in CI.

The examples use the home server `home.mym.ae` (behind Pangolin), an admin account, and a person's account (201). Use your own.

## You need
- The home server from the Phase 1E demo, with its admin and at least one person.
- **An Anthropic API key** for the written-answers part (console.anthropic.com → API keys). A few answers cost well under one US cent. Skip section 6 if you'd rather not.
- A laptop, and the iPhone on mobile data.

## 1. On your computer
```
cd ~/Projects/linx
git pull
make lint          # ends with "lint: ok"
make test          # nothing listed means all passed
```
- [x] CI is green for this commit (the images are published).

## 2. Update the home server
Build `linx` for the server and run setup again, keeping your answers, as for the Phase 1E fixes:
```
GOOS=linux GOARCH=amd64 make build
scp bin/linx linx@home.mym.ae:
ssh linx@home.mym.ae 'sudo ./linx setup'      # pick the terminal, keep every answer
```
- [x] Setup ends with "Linx is running".
- [x] `sudo linx doctor` shows the database at `schema version 29`.

## 3. Not signed in
In a private browser window, open `https://home.mym.ae`.
- [x] Under the sign-in box, **Help signing in** opens Help with only the sign-in guides (5 of them) and **Back to sign in**.
- [x] Type `https://home.mym.ae/help/activity` (an admin guide) in the address bar: "Guide not found", exactly as for `https://home.mym.ae/help/nothing-here`.
- [x] Search `api key`: nothing from the admin guides.

## 4. As a person (201)
Sign in as the person, on the iPhone on mobile data.
- [x] **Help** in the menu lists the everyday guides, and no admin ones.
- [x] `https://home.mym.ae/help/activity` gives "Guide not found" here too.
- [x] On **My account**, the **?** (Help for this page) opens the My account guide.
- [x] Search `how do I change my password`: the first result is the right part of My account.
- [x] No **Write an answer** button yet (answers are off).

## 5. As an admin
Sign in as the admin on the laptop.
- [x] Help lists the admin and system guides too.
- [x] Open a guide with a picture (for example Written answers in Help): the picture shows, and switches with the laptop's light/dark setting.
- [x] **?** on System → Settings opens that page's guide.

## 6. Written answers
- [x] System → Settings → **Help answers**: provider **Anthropic (Claude)**, paste the API key, turn it on, **Save**, confirm it's you.
- [x] Reload the page: the key field shows it's set, but never the key.
- [x] **Test** shows an answer about adding a desk phone, with the guides it used.
- [x] As the person (iPhone): **Write an answer** for `how do I change my password`. The answer appears **word by word** (not all at once after a wait: that would be Pangolin holding it back), in plain words, with **From:** links and "Answers are written by Anthropic (Claude) from Linx's guides."
- [x] Ask `what's the weather in Dubai?`: it says the guides don't cover that.
- [x] Ask `how do I add a phone line?` (an admin task): the answer doesn't give the admin steps or link an admin guide.
- [x] Admin: set **Questions a day, each person** to 1, **Save**. The person asks twice: the second says they've asked the most for one day, and the search results still show. Set it back to 200.
- [x] Admin: change the provider to **Another service (OpenAI-compatible)** with any https address and **Save**: it asks to paste the API key again. Cancel.
- [x] System → **Activity**: the `help_answers.update` entries show what changed and `api_key: changed`, never the key.
- [x] Turn written answers off (or keep them on, your choice). Off: the person's **Write an answer** button is gone after a reload.

## Results
- 2026-09-30, home.mym.ae behind Pangolin (admin on the laptop, 201 on the iPhone on mobile data, Anthropic key): **every box passed; approved by the owner.** Setup applied with the saved answers (`--config /etc/linx/setup.yaml`), doctor at schema version 29, only the two known warnings (CA backup on the server, no API key).
- Fix: this checklist quoted "There's no such page in Help"; the screen says "Guide not found" (wording corrected; the admin guide and a made-up one give the same page and the same 404).
- Owner request during the demo: a Light / Dark / follow-the-device switch on every page (work queue).
