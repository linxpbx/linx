---
title: Your iPhone and iPad
audience: everyone
section: everyday
keywords: [iphone, ipad, app, mobile, phone app, qr code, setup code, set up phone, lost phone, expired, ios, accounts, multiple accounts, second account, two servers]
screens: [/set-up-phone]
---
# Your iPhone and iPad

The Linx app turns your iPhone or iPad into your extension: it rings for your number, and calls you make from it show your number, not the phone's.

The Linx app is coming soon to the App Store. Until it's there, only phones invited to test it can install it; everything below works the same for them.

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

- **Calls** — everything you've made, taken and missed, newest first, with a number on the tab for what you've missed since you last looked. Tap the phone beside a row to ring them back, or tap the row itself for that call on its own: what happened, when, how long, which group rang and who answered. **All** / **Missed** switches between them. Search by number and Linx looks through every call it still keeps, not just the ones on the screen; search by name and it looks through the ones shown. Opening the tab clears the number, on this phone and on everything else you're signed in on, because missed calls belong to you and not to one phone.
- **Team** — everyone with an extension, and what they're doing this second: Available, Away, Do not disturb, Ringing, On a call (with how long), or Offline. It changes as it happens; nothing needs refreshing. Search by name or extension, tap the green phone to call or the blue camera for a video call, or tap the row for their card. Swipe a row to the right to **Favourite** someone: starred people sit in their own section at the top, on this phone only. The button at the top right is **your own** status — **Do not disturb** stops your extension ringing anywhere and sends callers to your voicemail.
- **Keypad** — type a number or an extension and tap the green phone button for a call, or the blue camera button for a video call; **Test my sound** calls Linx's echo test, which plays your own voice back so you can hear whether the microphone and the speaker are working. Tap either button with nothing typed and the last number you called comes back, ready to ring again.
- **More** — **Voicemail** and **Settings**.

## On an iPad, and on an iPhone that opens out

The app is not the phone app stretched. On a big screen the four tabs move to a sidebar down the side, and each one becomes a *list with what you picked open beside it*, the way Mail does:

- **Calls** — the list on the left, and the call you tap on the right: who it was, what happened, when, how long it lasted, which group rang and who answered, with a button to *ring them back*.
- **Team** — the list of people, and the person you tap on the right: their extension, what they're doing this second, **Call**, **Video call**, and **Add to favourites**.
- **Keypad** — your starred people stand beside the keypad under **Favourites**, with the last number you rang above them. Tapping one puts their extension on the keypad; the phone beside it rings them. (Star people in **Team**, by swiping a row to the right.)
- **More** — **Voicemail** and **Settings** open beside the list instead of covering it.

*A call doesn't take the whole screen either.* On an iPad, or an iPhone opened out, the call stands in its own column beside whatever you were doing, so the team list stays where it was. On a phone it covers the screen as it always has.

*On a phone that folds*, the app asks the phone where its fold is and divides along it, so no button ever lands in the crease. Opened out, it is a small iPad; folded shut, it is a phone.

## Voicemail

**More** → **Voicemail**: your own messages and any ring group's you're in, newest first, with a dot beside the ones nobody has heard. Tap play to listen — that marks it heard for everyone who shares the box — swipe a message to delete it (also for everyone who shares the box), or tap the phone to ring the caller back. Nothing is kept on the phone: a message is fetched when you press play and forgotten when it stops.

## Making and taking calls

While you're in a call: **Mute**, **Keypad** (for menus, extensions and PINs), **Speaker** and **Video**. **Hang up** ends it. The top of the screen says **Encrypted**; tap it (or the ⓘ beside it) for how the call is getting through — straight to the other phone (direct) or through your Linx server (relayed, which is normal on mobile data and on networks that block everything but web traffic). The **…** at the top right holds **Add person** and **Call details**.

*Where the sound comes out.* With nothing else connected, **Speaker** is a plain switch between the earpiece and the loudspeaker. Connect AirPods, a headset or a car — before the call or in the middle of it — and the sound moves there, and the button becomes the iPhone's own picker wearing the name of whatever has the sound, so you can move it between them without hanging up. Turning your camera on never moves the sound.

*When you can't hear anything.* Tap **Encrypted** to open **Call details**: whether a route for the sound was found at all, whether it goes straight there or through your Linx server's relay, how much sound is coming in and going out, and what the relay said if it wouldn't take this phone. A call that has been up for a few seconds with nothing arriving says so on the screen. **Copy these details** puts the lot on the clipboard to send to whoever looks after your Linx server.

A call that comes in while the app is open rings on the phone and shows **Answer** and **Decline**.

## Video calls

The blue **Video call** button (a camera) rings someone as a video call: your camera is on from the start, their phone or browser shows it is a video call while it rings, and answering turns their camera on. Between two people using Linx the picture goes straight from one to the other, so turning a camera on or off happens at once and never disturbs the call. Either of you can tap **Stop video** at any time and the call carries on as an ordinary call. Calling a desk phone or an outside number, which can't show a picture, simply rings as an ordinary call, and **Video** stays greyed out. You can also add a picture to a call that's already up: tap **Video**. Their camera only comes on when they answer a video call or tap **Video** themselves, never because somebody else pressed a button.

*The screen stays on* for as long as there is a picture in the call — it never dims or locks itself halfway through, and only the power button turns it off. A call with no picture leaves the screen to iOS as usual, and goes dark at your ear.

In a video call the picture fills the whole screen, on an iPhone and an iPad alike. The caller's name, the buttons and the **…** go away by themselves after a few seconds; *tap the picture to bring them back*. You see them on the big screen and yourself in the corner — *tap the small picture to swap them over*, so your own camera takes the big screen and theirs moves to the corner, and tap it again to put them back. If only your camera is on, *your own picture takes the big screen* — a call with one camera on is a perfectly ordinary call. **Stop video** puts the camera away and the call carries straight on as a phone call; The camera button on your own picture switches between the front and back cameras. Turning the camera on over mobile data takes a second or two, and the button says so while it works. The picture follows your connection, up to 540p, or up to 720p with **HD** on: tap the **HD** badge at the top right (solid when on, an outline when off). HD is off at the start of every call because 720p uses nearly twice the data, and it only changes the picture *you* send. On a slower connection the picture gets smaller by itself and recovers as the connection does. If the connection gets too poor for any picture at all, Linx turns your camera off by itself and tells you: a call you can hear is worth more than one you can see. **Call details** (tap **Encrypted**) says the size your picture is really going out at. On an iPad, the front camera uses **Center Stage**: it keeps you in the middle of the picture as you move. Switch it off in Control Center → **Video Effects** if you prefer a still frame.

*Between two Linx users* (the app or the web app on either side), the voice goes straight from one device to the other once the call is answered, in higher quality than an ordinary phone line, the way the picture already does. If that direct way stops working, the call carries on through Linx within a second or two, without you doing anything. Calls to desk phones and outside numbers aren't affected. **Call details** shows which way the sound is going.

*Switching to another app during a video call* carries the call on in a small window in a corner of the screen, both pictures still moving: they're big in it and you're small in the corner. Drag it anywhere, pinch it bigger, or swipe it to the edge of the screen to tuck it away (your camera pauses while it's tucked away). Tap the window for its two buttons: one goes back to the call, the cross closes the window and the call carries on as sound only.

The screen lays itself out for whatever you're holding: upright or turned on its side, and on an iPhone that opens out, the picture on one side of the fold and the buttons on the other.

*A call to a locked or sleeping phone* rings like any other call on an iPhone: your own ringtone, the caller on the lock screen, and Answer without unlocking. You can take it from a car, from headphones or from a watch, and it shows afterwards in the Phone app's Recents with everything else. It works with the app closed, and even after you've swiped it away. The first time you set a phone up, Linx asks to send notifications — say yes, so a missed call and a new voicemail reach you too. (An admin has to turn this on once for the whole company: see below.)

In mainland China Apple doesn't allow calls on the lock screen, so Linx rings inside the app there instead: a call arrives as a notification you tap, and the app rings its own screen. The app says so on **This phone** when that's where it is.

Every call is encrypted both ways, and the app has no password of any kind on it — it asks Linx for a one-time phone line each time it starts.


## Group calls

In a call with someone else using Linx, tap **Add person** at the top of the call and pick someone from the Team list. Their phones ring with an invitation to a group call (a sleeping iPhone is woken for it, like any call), and when they answer, everyone sees and hears everyone, in a grid that highlights whoever is speaking. Tap **Add person** again for more, up to 8 people. **Mute**, **Start video** and **Stop video** work as in any call; **End** leaves the group call, and the others carry on. Desk phones and outside numbers can't join a group call yet.

## Calls when the app isn't open (admins)

For a call to ring an iPhone or iPad whose app is closed or asleep, Linx has to ask Apple to wake it. That needs a key from an Apple developer account, which the person who publishes the app holds — on a self-hosted Linx, that's whoever set this server up.

System → **Settings** → **Calls to the app** → **Set it up**: the team id, the key id, and the `.p8` file Apple lets you download once, pasted in. The key is sealed on this server and never shown again, and only a system admin can change it.

Once it's on, a call for someone whose app is asleep waits a few seconds — the caller hears ringing — while the phone wakes up and joins; then every phone of theirs rings together, as always. The card shows how many phones have been woken and how long they took. Apple is told the call's number and nothing else: no names, and nothing about what is said.

A missed call and a new voicemail also arrive as ordinary notifications, on the phones whose owner allowed them. The app's icon shows a number too: missed calls you haven't looked at plus voicemail you haven't heard, the same two numbers as the **Calls** and **Voicemail** tabs. It goes down when you open them, here or on anything else you're signed in on.

Leave it off and nothing is sent to Apple at all; the app then rings only while it is open.

## "Set it up again"

A phone stays set up as long as it's in touch with Linx — it checks in by itself while you use it. Nothing else interrupts it. It asks to be set up again only when:

- it hasn't been in touch for **6 months** (a phone left in a drawer, or lost and switched off);
- the person's **password changed**;
- an admin stopped it.

Then the list says **Set it up again**: make a new code and set it up as above. Nothing else about the phone changes.

## Losing your phone

My account → **My phones** → **I've lost it**. It stops at once and for good, and any call on it drops. An admin can do the same from People → the person → **Stop it**.

Even if someone has the phone, they can't use it to be you: the key is inside the phone and can't be copied, and the phone stops by itself after 6 months with no contact. Stopping it also makes Linx forget how to ring it, so nothing more is sent to that phone at all.

## When a call went wrong (admins)

After every call it makes or takes, the app sends Linx a short record of what it saw: how the call was set up, the route it took through the network, the sound in and out, and anything that failed. Never the sound itself, and no passwords. People → the person → **Call records** beside the phone shows the last seven days, newest first. Open one to read it, or **Copy** it to send to whoever looks after your Linx.

It's the same as the **Copy these details** text from the call screen, kept for you, so nobody has to remember to copy it during a call that's going wrong.

If the app ever crashes, iOS makes a report of it and hands it to the app the next time it opens (sometimes up to a day later). The app sends it to Linx, and it appears in the same list as **The app crashed**, in red. It holds the crashed part of the app's code and nothing of yours.

## More than one Linx on one phone

One iPhone or iPad can be set up on *several Linx systems at once* — your work system and another one, or two companies. Up to four.

*They are all live together.* Every account rings. There is nothing to switch into, and no account misses a call because you were "in" another one.

**More** → **Settings** → **Accounts** is the list. **Add account** asks for a setup code from the other Linx, exactly as the first one did: My account → My phones → Add phone, over there. It then asks what to **call** the account on this phone — "Work", "Site office" — which is the name you'll see when it rings. Leave it blank and it's called after its Linx (the server's address). Swipe an account later to rename it or to remove it.

Once there is more than one account:

- a call that rings says which account it came through, on the lock screen as well — "Ahmed Khan — Work";
- **Calls** and **Voicemail** are still one list each, newest first, with a small label on each row saying which account it belongs to. Ringing somebody back always goes out on the account their call came in on;
- **Team** has a section for each account, because the same extension number on two systems is two different people;
- the keypad asks **Call with** which account, and remembers the last one you used;
- the call screen names the account under the caller.

Removing an account stops this phone ringing for it and forgets its key. Your other accounts are untouched. If you've **lost the phone**, stop it in Linx on a computer instead (below) — and do it on each system the phone was set up on, since each one only knows its own.

With one account none of this appears at all: no labels, no sections, no chooser.

## Settings on the phone

**More** → **Settings**: your status, how this phone's line is doing, which extension it answers, which **Person** it signs in as and which **Linx server** it was set up on, when it would have to be set up again, whether video calls start on the front camera, how calls reach this phone, **Accounts** (above), and **Sign out of this phone**. Two more:

- **Appearance** — Light, Dark, or **Match this device**, the same choice the web app has. It is kept on this phone.
- **Show Linx calls in the Phone app** — on, a Linx call sits in the iPhone's own Recents beside your ordinary calls and you can ring back from there; off, Linx keeps its calls to itself and only the **Calls** tab has them. Calls already made stay where they are.

## The app can't run Linx

The app is for calls, your team and your voicemail. Adding people, phone lines and settings stays in the web app on a computer, even if you're an admin.
