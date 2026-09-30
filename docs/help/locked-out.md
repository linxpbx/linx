---
title: Locked out
audience: public
section: everyday
keywords: [locked out, too many tries, wait, blocked, can't sign in, account disabled, unlock]
screens: []
---
# Locked out

## Too many wrong passwords

After five wrong passwords, Linx makes each next try wait: one minute, then longer, up to an hour. During the wait, even the right password is refused. This stops people guessing your password.

What to do:

- Wait, then try once, carefully.
- Sign in with your passkey or company account instead: those aren't held back by the wait.
- Ask your admin to unlock you: in People they choose **Unlock**. From the server: `sudo linx user unlock you@example.com`.

If you didn't make those wrong tries yourself, tell your admin: someone may be guessing your password.

## Your account is turned off

If your admin turned your account off, no way of signing in works. Ask them to turn it back on.

## People must use company sign-in

If your Linx requires company sign-in, a correct password is refused and the page says so. Use the company button (like *Sign in with Google*) instead.

## Other problems

- Lost your phone or passkey: [Lost your authenticator or passkey](lost-authenticator).
- Forgot your password: ask your admin for a set-password link ([Your invite or set-password link](set-password-link)).
