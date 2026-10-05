---
title: Your iPhone and iPad
audience: everyone
section: everyday
keywords: [iphone, ipad, app, mobile, phone app, qr code, setup code, set up phone, lost phone, expired, ios]
screens: [/set-up-phone]
---
# Your iPhone and iPad

The Linx app turns your iPhone or iPad into your extension: it rings for your number, and calls you make from it show your number, not the phone's.

Setting one up takes a code that works **once** and lasts 10 minutes. No password is in it: the phone makes its own security key as it finishes, and that key never leaves the phone.

## Setting up your own phone

My account → **My phones** → **Add phone**. Choose iPhone or iPad, give it a name, then pick a way:

![My phones](screen:account-my-phones)

- **Show a QR code** — open Linx on the phone and scan what's on the screen.
- **Email a link to me** — open the email on the phone and tap the link; it shows the same code.
- **Set it up by hand** — type the 8 characters into the app.

![Setting up a phone](screen:add-phone-code)

Linx says **Waiting for the phone…** until it finishes, then the phone appears in the list. If the 10 minutes run out, close the box and make another code.

## Setting one up for someone else

Admins: People → open the person → **Phones** → **+ Add phone**. The same three ways; **Email a link** sends it to that person's email address.

![A person's phones](screen:person-phones)

They need an extension first, or their phone would have nothing to answer for.

## On the phone itself

Open Linx on the iPhone or iPad. It shows **Set up your extension**, and there are three ways in:

- Point the camera at the QR code on the computer's screen — that's it.
- **Paste setup link** — if the code came by email, open the email on this phone, hold the link until Copy appears, then tap this and paste.
- **Set it up by hand** — type the Linx address (like pbx.your-company.com) and the 8 characters.

The phone then makes its own security key, gets its own certificate from Linx, and signs itself in. There is nothing to remember and no password to type on the phone, now or later: it keeps itself signed in on its own.

To take Linx off a phone you still have: **Sign out of this phone** in the app. For a phone you've lost, stop it in Linx instead (below) — that works even if the phone is switched off.

## What's in the app

Four tabs along the bottom:

- **Calls** — everything you've made, taken and missed, newest first, with a number on the tab for what you've missed since you last looked. Tap the phone beside a row to ring them back. **All** / **Missed** switches between them. Search by number and Linx looks through every call it still keeps, not just the ones on the screen; search by name and it looks through the ones shown. Opening the tab clears the number, on this phone and on everything else you're signed in on, because missed calls belong to you and not to one phone.
- **Team** — everyone with an extension, and what they're doing this second: Available, Away, Do not disturb, Ringing, On a call (with how long), or Offline. It changes as it happens; nothing needs refreshing. Search by name or extension, tap the phone to call or the camera to start a call with video. Swipe a row to the right to **Favourite** someone: starred people sit in their own section at the top, on this phone only. The button at the top right is **your own** status — **Do not disturb** stops your extension ringing anywhere and sends callers to your voicemail.
- **Keypad** — type a number or an extension and tap the green call button; **Test my sound** calls Linx's echo test, which plays your own voice back so you can hear whether the microphone and the speaker are working. Tap the green button with nothing typed and the last number you called comes back, ready to ring again.
- **More** — **Voicemail** and **Settings**.

## Voicemail

**More** → **Voicemail**: your own messages and any ring group's you're in, newest first, with a dot beside the ones nobody has heard. Tap play to listen — that marks it heard for everyone who shares the box — swipe a message to delete it (also for everyone who shares the box), or tap the phone to ring the caller back. Nothing is kept on the phone: a message is fetched when you press play and forgotten when it stops.

## Making and taking calls

While you're in a call: **Mute**, **Keypad** (for menus, extensions and PINs), **Speaker** and **Video**. **Hang up** ends it. The line at the top says **Encrypted**, and whether the sound is going straight to the other phone (**Direct**) or through your Linx server (**Relayed**) — relayed is normal on mobile data and on networks that block everything but web traffic.

*Where the sound comes out.* With nothing else connected, **Speaker** is a plain switch between the earpiece and the loudspeaker. Connect AirPods, a headset or a car — before the call or in the middle of it — and the sound moves there, and the button becomes the iPhone's own picker wearing the name of whatever has the sound, so you can move it between them without hanging up. Turning your camera on never moves the sound.

*When you can't hear anything.* Tap the **Encrypted · …** line to open **Call details**: whether a route for the sound was found at all, whether it goes straight there or through your Linx server's relay, how much sound is coming in and going out, and what the relay said if it wouldn't take this phone. A call that has been up for a few seconds with nothing arriving says so on the screen. **Copy these details** puts the lot on the clipboard to send to whoever looks after your Linx server.

A call that comes in while the app is open rings on the phone and shows **Answer** and **Decline**.

## Video calls

A Linx call always starts as an ordinary call, and the picture is added to it: tap **Video** during a call, or the camera button beside someone in **Team**, which rings them first and turns your camera on when they answer. The other person sees a picture when they turn theirs on too — yours never comes on by itself because somebody else pressed a button.

In a video call you see them on the big screen and yourself in the corner. If only your camera is on, *your own picture takes the big screen* — a call with one camera on is a perfectly ordinary call. **Stop video** puts the camera away and the call carries straight on as a phone call; **Flip camera** swaps the front camera for the back one. Turning the camera on over mobile data takes a second or two, and the button says so while it works. The picture follows your connection: on a fast one it goes up to 720p, on a slower one it drops to whatever that link can carry, and it changes while you talk — down the moment the connection tightens, back up once it has been comfortable for a few seconds. On a call going through your Linx server's relay it stays inside what the relay will carry. If the connection gets too poor for any picture at all, Linx turns your camera off by itself and tells you: a call you can hear is worth more than one you can see. **Call details** (tap the **Encrypted · …** line) says which size you are sending.

The screen lays itself out for whatever you're holding: upright, turned on its side, and on an iPad (or an iPhone that opens out) the picture on one side and the buttons on the other.

*A call to a locked or sleeping phone* rings like any other call on an iPhone: your own ringtone, the caller on the lock screen, and Answer without unlocking. You can take it from a car, from headphones or from a watch, and it shows afterwards in the Phone app's Recents with everything else. It works with the app closed, and even after you've swiped it away. The first time you set a phone up, Linx asks to send notifications — say yes, so a missed call and a new voicemail reach you too. (An admin has to turn this on once for the whole company: see below.)

In mainland China Apple doesn't allow calls on the lock screen, so Linx rings inside the app there instead: a call arrives as a notification you tap, and the app rings its own screen. The app says so on **This phone** when that's where it is.

Every call is encrypted both ways, and the app has no password of any kind on it — it asks Linx for a one-time phone line each time it starts.

## Calls when the app isn't open (admins)

For a call to ring an iPhone or iPad whose app is closed or asleep, Linx has to ask Apple to wake it. That needs a key from an Apple developer account, which the person who publishes the app holds — on a self-hosted Linx, that's whoever set this server up.

System → **Settings** → **Calls to the app** → **Set it up**: the team id, the key id, and the `.p8` file Apple lets you download once, pasted in. The key is sealed on this server and never shown again, and only a system admin can change it.

Once it's on, a call for someone whose app is asleep waits a few seconds — the caller hears ringing — while the phone wakes up and joins; then every phone of theirs rings together, as always. The card shows how many phones have been woken and how long they took. Apple is told the call's number and nothing else: no names, and nothing about what is said.

A missed call and a new voicemail also arrive as ordinary notifications, on the phones whose owner allowed them.

Leave it off and nothing is sent to Apple at all; the app then rings only while it is open.

## "Set it up again"

A phone stays set up as long as it's in touch with Linx — it checks in by itself while you use it. Nothing else interrupts it. It asks to be set up again only when:

- it hasn't been in touch for **6 months** (a phone left in a drawer, or lost and switched off);
- the person's **password changed**;
- an admin stopped it.

Then the list says **Set it up again**: make a new code and set it up as above. Nothing else about the phone changes.

## Losing your phone

My account → **My phones** → **I've lost it**. It stops at once and for good, and any call on it drops. An admin can do the same from People → the person → **Stop it**.

Even if someone has the phone, they can't use it to be you: the key is inside the phone and can't be copied, and the phone stops by itself after 6 months with no contact.

## Settings on the phone

**More** → **Settings**: your status, how this phone's line is doing, which extension it answers, when it would have to be set up again, whether video calls start on the front camera, how calls reach this phone, and **Sign out of this phone**. Two more:

- **Appearance** — Light, Dark, or **Match this device**, the same choice the web app has. It is kept on this phone.
- **Show Linx calls in the Phone app** — on, a Linx call sits in the iPhone's own Recents beside your ordinary calls and you can ring back from there; off, Linx keeps its calls to itself and only the **Calls** tab has them. Calls already made stay where they are.

## The app can't run Linx

The app is for calls, your team and your voicemail. Adding people, phone lines and settings stays in the web app on a computer, even if you're an admin.
