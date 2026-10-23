---
title: Call history
audience: everyone
section: everyday
keywords: [call quality, bad call, call problems, choppy, frozen video, no sound, call history, calls, recent calls, missed call, missed calls, call log, call records, cdr, who called, call back, download calls, csv, export calls, report]
screens: [/calls, /admin/calls, /admin/calls/quality]
---
# Call history

**Call history** in the sidebar lists your calls, newest first, by day: the ones you made, the ones you got, and the ones you missed. A number on it counts the calls you missed since you last opened it.

![Call history](screen:call-history)

## Your calls

- Each call's icon shows whether you got it or made it. A missed call is in red: *"Missed"*, or *"Missed · left a voicemail"* when the caller left a message.
- A call to a [ring group](ring-groups) you're in says so: *"050 123 4567 · rang Sales"*. If someone else in the group answered, it isn't missed.
- A call you made that nobody answered says *"No answer"*.
- **Call** rings the number back from your browser.
- Press a call to see its way through Linx, step by step: *"Rang Sales (all at once): Sara (101), Omar (102) · nobody answered in 25 s"*, then *"Went to the voicemail for Sales · left a message (0:42)"*. If a message was left and you may hear it, **Listen** plays it here.
- **Missed** shows only the calls you missed. **Search number** finds calls with a number (any part of it, with or without the leading 0).
- **Remove from my list** takes a call out of *your* list — in the browser, the bin beside **Call**; in the iPhone and iPad app, swipe the row and press **Delete**. **Undo** puts it back for ten seconds afterwards. It goes from every phone and browser you sign in on, because it is your list, not that phone's.
  - Linx still keeps the call itself: an admin's **Calls** screen and the CSV download still show it, and it is deleted only when the **Keep call history** setting says so. Removing a call from your list is tidying, not a way to delete a record.

Your calls come from all your phones and browsers, not just this one. The Dialer's **Recent** list shows your last 5.

Calls are kept for 1 year, then deleted (an admin can change that).

## For admins: everyone's calls

**Calls** in the admin sidebar lists every call on the server. Reporters see it too.

![Calls](screen:admin-calls)

- Choose a person (**Anyone**), a number, **Missed only**, and when (**Today**, **Last 7 days**, **Last 30 days**, **Everything kept**).
- Each call shows who called, the number or person called (and the ring group it rang), what happened, and how long people talked.
- Calls people have removed from their own lists are still here. Nobody can delete a call from the company's record from a phone or a browser.
- Press a call for its details: every step, the phone line it came in or went out on, who answered, and the voicemail left. Admins can play the voicemail; listening to someone else's is written in the activity log.
- **Download (CSV)** saves the calls you've chosen as a spreadsheet file (one line per call, up to 100,000), with times in the server's time zone.
- **System → Settings**: **Keep call history** sets how long calls are kept (30 days to 2 years; 1 year unless you change it) and shows the space call history uses. Older calls are deleted within the hour.

Call history is written by the phone engine itself as each call ends, so calls are kept even while the rest of Linx restarts. It's in Linx's database, so [backups](backups) include it. Each call takes about half a kilobyte.

## For admins: how calls went

**Call quality**, next to **Call history** on the admin **Calls** page, shows how each phone's part of every call in the last seven days went, so you hear about a bad call even when nobody tells you.

- Linx judges each one itself, from the sound the phone engine carried: *Good*, *Had problems* or *Bad*, with the reason in words, such as *"the way from this phone lost 9% of its sound"*, *"its sound came in uneven bursts"* or *"a long delay each way"*. A phone that was woken for a call but never connected is listed as *Bad* too.
- Beside it is what the Linx app on that iPhone or iPad said of the same call: its own *Good*, *Had problems* or *Bad*, for example *"their picture stopped 8 times, 21 s in all"*. Desk phones don't send one.
- **Only calls that went wrong** is on to begin with; turn it off to see every call.
- Press a call to see what Linx measured and the phone's whole report, with **Copy** to send it on.

If calls keep going wrong on one phone (three bad ones in a day), Linx raises an alert, the same way as its other [alerts](alerts). The records are kept seven days.
