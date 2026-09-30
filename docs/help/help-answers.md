---
title: Written answers in Help
audience: admin
section: admin
keywords: [ai, artificial intelligence, chatbot, claude, anthropic, ollama, openai, llm, language model, answers, api key, help answers]
screens: [/admin/system/settings]
---
# Written answers in Help

Help's search finds the parts of the guides that match a question. You can also let Help **write an answer**: a short reply in plain words, made only from those parts of the guides, with links to the guides it used. It's off until you turn it on.

![Help answers](screen:system-settings-help-answers)

## What leaves your server

Only the question and the parts of the guides that match it, sent to the provider you pick. Nothing about your server, your people or your calls. The answer can't see or change anything in Linx: it only reads the guides.

## Turning it on

1. Open **System**, then **Settings**, and find **Help answers**.
2. Pick a provider:
   - **Anthropic (Claude)**: the simplest. Make an API key at console.anthropic.com, under API keys. The model is filled in for you: Claude Haiku, fast and cheap.
   - **Ollama**: a model running on your own computer or network, so nothing leaves it. Its **Address** must start with https://, so put it behind your proxy first. If it's on your home network, Linx offers to allow that address.
   - **Another service (OpenAI-compatible)**: any service that speaks the OpenAI chat API. Give its **Address**, **Model** and **API key**.
3. Turn on the switch at the top of the card, then press **Save** and confirm it's you.
4. Press **Test**: Linx asks one question and shows the answer.

The **API key** is locked away on the server and never shown again. If you change the provider or its address, paste the key again: a key only ever goes to the address it was given for.

## Limits

Each person can ask 10 questions a minute and 200 a day, and the whole server 1,000 a day, so nobody can run up a bill. Change the daily numbers on the same card. The card shows how many were asked today. When a limit is reached, Help still shows the search results.

## If an answer doesn't come

Help shows why (for example, the provider didn't accept the key, or didn't answer within 30 seconds) and the search results under it. Press **Test** on the card to try it yourself: there you also see what the provider itself said, which people asking don't (it can name your account with the provider, or an address on your network).
