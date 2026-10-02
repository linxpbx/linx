---
title: Call history
audience: everyone
section: everyday
keywords: [call history, calls, recent calls, missed call, missed calls, call log, call records, cdr, who called, call back, download calls, csv, export calls, report]
screens: [/calls, /admin/calls]
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

Your calls come from all your phones and browsers, not just this one. The Dialer's **Recent** list shows your last 5.

Calls are kept for 1 year, then deleted (an admin can change that).

## For admins: everyone's calls

**Calls** in the admin sidebar lists every call on the server. Reporters see it too.

![Calls](screen:admin-calls)

- Choose a person (**Anyone**), a number, **Missed only**, and when (**Today**, **Last 7 days**, **Last 30 days**, **Everything kept**).
- Each call shows who called, the number or person called (and the ring group it rang), what happened, and how long people talked.
- Press a call for its details: every step, the phone line it came in or went out on, who answered, and the voicemail left. Admins can play the voicemail; listening to someone else's is written in the activity log.
- **Download (CSV)** saves the calls you've chosen as a spreadsheet file (one line per call, up to 100,000), with times in the server's time zone.
- **System → Settings**: **Keep call history** sets how long calls are kept (30 days to 2 years; 1 year unless you change it) and shows the space call history uses. Older calls are deleted within the hour.

Call history is written by the phone engine itself as each call ends, so calls are kept even while the rest of Linx restarts. It's in Linx's database, so [backups](backups) include it. Each call takes about half a kilobyte.
