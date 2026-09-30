---
title: Company sign-in (Google, Microsoft and others)
audience: admin
section: admin
keywords: [company sign-in, sso, single sign-on, google, google workspace, gmail, microsoft, microsoft 365, entra, azure ad, authentik, keycloak, openid connect, oidc]
screens: [/admin/system/settings]
---
# Company sign-in (Google, Microsoft and others)

People can sign in with their company account instead of a password: Google, Microsoft 365, Authentik, Keycloak, or anything that speaks OpenID Connect.

## Adding a provider

System → Settings → **Company sign-in** → **+ Add a provider**, then choose yours. Linx shows the steps for it:

1. Make an app at your provider (for Google: console.cloud.google.com → APIs & Services → Credentials).
2. Copy Linx's **Redirect address** into it.
3. Paste its **Client ID** and **Client secret** into Linx (and its **Issuer address**, for some providers).
4. **Add**, and confirm it's you.

![Adding a provider](screen:system-settings-add-provider)

## Using it

- A person's Linx email must be the same as their company account's email, the first time. After that, the link stays.
- Each person can link their account from My account.
- It counts like a correct password: anyone with a passkey or authenticator is still asked for it.

## Showing the button

**Show on the sign-in page**, or keep it hidden (then it works only for linking and confirming).

## People must use company sign-in

Turn it on to refuse passwords for everyone but system admins. System admins can always use a password or passkey, so a broken provider can't lock you out.
