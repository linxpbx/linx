---
title: Email
audience: admin
section: admin
keywords: [email, e-mail, smtp, mail, gmail, google workspace, microsoft 365, outlook, office 365, icloud, fastmail, amazon ses, postmark, brevo, mailgun, app password, invites, password reset, spam, not arriving]
screens: [/admin/system/settings]
---
# Email

Linx can send email: invites, "Forgot your password?" links, voicemail and alerts. It sends through a mail account you already have, like Gmail, Microsoft 365 or a sending service. Linx doesn't run a mail server of its own: mail from a home connection is usually blocked or marked as spam.

![Email](screen:system-settings-email)

Only a **system admin** can set it up, because it holds a mail account's password. Other admins see the card without being able to change it.

## Setting it up

1. Open **System**, then **Settings**, and press **Set up email**.
2. **Who sends it**: pick your mail provider or sending service. If yours isn't in the list, pick **Something else**.
3. **Sign in**: give the address to send from, and the account's password:
   - **Gmail or Google Workspace**, **iCloud Mail** and **Fastmail** need an *app password*, not your normal one. The page says where to make it, with a link.
   - **Microsoft 365**: your Microsoft admin has to allow "Authenticated SMTP" for the mailbox. Microsoft is turning off password sign-in for sending mail, so it may stop working; a sending service is safer there.
   - **Amazon SES**, **Postmark**, **Brevo** and **Mailgun** give you a user name and password for sending (SMTP). They only send from an address or domain you've verified with them.
   - **Something else** also asks for the mail server, the encryption and the port. Your provider's help pages list them.
4. Press **Save and send a test** and confirm it's you. Linx sends a test to your own address and shows each step: connected, encrypted, certificate checked, signed in, sent.
5. **Did it arrive?** Press **Yes**, and email is on. If it didn't, **No, show me what to check** lists the usual reasons (the spam folder first).

**More: server, port, emails an hour** shows the server Linx uses, and the hourly limit.

## Always encrypted

Linx only sends email encrypted: TLS from the start (usually port 465) or STARTTLS (usually port 587), and it checks the mail server's certificate. There is no unencrypted choice, and Linx doesn't use port 25.

The password is locked away on the server and never shown again. If you change the server or the address you send from, type the password again: a password only ever goes to the server it was given for.

A mail server on your own network (a NAS or a mail relay at home) works too: Linx asks first, and **Allow** adds it to the outbound allowlist.

## What the card shows

- **Sending as** and **Through**: the address and the mail server.
- **Last sent** and how many this hour, with the limit.
- **Queue**: **Nothing waiting**, or how many emails are waiting and why.

**Send a test email** tries again any time. **Turn off** stops all email; nothing is lost from the settings.

## Limits

At most *60 emails an hour* by default, so a taken-over account can't turn Linx into a spam sender. Change it under **More** when you set up email. Emails over the limit wait for the next hour.

An email that can't be sent because the mail server is busy is tried 3 times, a minute and then 15 minutes apart. One the mail server refuses (a wrong password, say) isn't tried again.

## When email stops working

If the mail account stops working (the password was changed, the app password removed, the server refuses Linx), Linx sends an **Email isn't sending** alert to your other alert channels, never by email. It clears by itself once an email goes out again. Fix the account, then press **Send a test email**.

Linx keeps an email only until it's sent. Invites and password resets carry links that work like a password, so Linx locks them away while they wait and removes them once sent.
