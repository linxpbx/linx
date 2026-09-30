---
title: Lost your authenticator or passkey
audience: public
section: everyday
keywords: [lost phone, new phone, broken phone, lost passkey, authenticator, reset 2fa, recovery code, can't get code]
screens: []
---
# Lost your authenticator or passkey

## You still have a recovery code

Sign in with your password, then choose **Use a recovery code instead** and type one of the codes you saved. Each works once.

Once you're in, go to My account and replace your authenticator app or add a new passkey. You get new recovery codes too.

## You have no recovery codes left

Ask your admin to reset it. In People they can choose **Reset authenticator**: it removes your authenticator app and passkeys and signs you out everywhere. Your password stays. Sign in with it, then set up a new second step.

- An admin's reset is done by a system admin.
- If you're the only system admin, reset it on the server:

```
sudo linx user reset-2fa you@example.com
```

A password reset never removes the second step: that would make it protect nothing.

## You've lost your password too

Your admin can send you a new set-password link ([Your invite or set-password link](set-password-link)). From the server: `sudo linx user setup-link you@example.com`.
