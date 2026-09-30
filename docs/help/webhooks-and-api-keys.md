---
title: Webhooks and API keys
audience: admin
section: admin
keywords: [webhook, api, api key, integration, crm, automation, oauth, client id, events, developer]
screens: [/admin/webhooks, /admin/api-keys]
---
# Webhooks and API keys

For your own software. Both are expert pages: turn **Simple mode** off to see them ([Admin home and Simple mode](admin-home#simple-mode)).

## Webhooks

A webhook tells another system when something happens in Linx (a call, a new person, a failed backup).

1. **Webhooks** → **+ Add** → **Quick add** (just the address, every event) or **Guide me**.
2. Give its **Address** (it must start with `https://`).
3. Linx sends a test event. Copy **Its signing secret, shown once**: your software uses it to check each event came from Linx.

Open one to see **Recent deliveries**, **Resend** one, narrow down the **Events**, or **Turn off**.

## API keys

An API key lets your own software use Linx. Each key can do only what it's given.

1. **API keys** → **+ Create**.
2. Choose its access: **Read only**, **Manage people and extensions**, **Everything** or **Choose permissions**.
3. Choose when it **Stops working**.
4. Copy the key. It's shown once. **I've saved it**.

**Revoke** a key you don't need any more: anything using it stops working at once.
