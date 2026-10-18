# Linx — the sound of a Linx-to-Linx call goes device to device

*Owner, 2026-10-18: "I will do the small step first … the main reason is improve voice quality to match the direct video call." The small step of the two offered that day: the sound joins the picture on the direct link; Asterisk stays the switchboard. ADR-092 records the decision. **Design, awaiting the owner's approval.***

## 1. In plain words
- **Nothing about ringing changes.** Calls ring, wake a phone, show on the lock screen, go to voicemail and appear in history exactly as now. Asterisk still runs the call.
- **Between two Linx devices** (the app or the browser, either side), once the call is answered the **sound moves onto the same direct link the picture already uses**. The two devices then talk to each other, not through Asterisk.
- **Asterisk's sound route stays connected, but quiet**, as a safety net. If the direct sound stops arriving, both devices switch back to it within about a second and the call carries on.
- **Desk phones, outside numbers, voicemail, `*43` and group calls are untouched.** They have no direct link, so they keep the route they have today.

## 2. Why it should sound better
Today the voice goes phone → Asterisk → phone, as two separate connections joined in the middle. Asterisk passes the sound on but **not the reports each end uses to adapt**: how much is being lost, how late it arrives, how much room the network has. Each phone adapts to its half of the route only. On the direct link:
- **The reports go end to end.** Opus's loss protection (FEC) and its bitrate follow the real route between the two phones, the way they already do for the picture.
- **One congestion control for sound and picture.** On the shared link the sound is marked high priority, so when the network tightens the picture gives way first. Today the two compete without knowing about each other.
- **Lips match.** Sound and picture travel together in one stream, so WebRTC keeps them in step. Today they take different routes.
- **One hop fewer** on a direct route. On mobile data both still go through Linx's relay (`MobileData`), which forwards packets without touching the reports.

This is the expectation, not a promise: §6 says how it is measured before it reaches the owner.

## 3. How it works
### 3.1 The direct link carries a microphone too
`DirectPicture` (app) and `web/src/phone/picture.ts` (browser) already make one direct connection for every answered Linx-to-Linx call, voice or video, and keep it for the whole call (`SIPUserAgent.startPicture` skips only `*43`). They gain an **audio transceiver** with the call's own microphone track, so no second microphone is opened and the audio session CallKit owns is not touched. The microphone and the camera share one stream id, which is what lets WebRTC keep them in sync. The audio sender gets `networkPriority = high`. The Opus settings are the call's existing ones (`SDPTweaks` / `web/src/phone/sdp.ts`: 24 kbit/s cap, DTX, in-band FEC).

### 3.2 The switch-over
1. **The call starts on Asterisk's route**, exactly as now, so the first words are heard at once and nothing waits for the direct link.
2. When the direct link is connected **and sound from the other side has arrived on it** (inbound audio packets counted in the stats, two readings), the device switches: the **direct sound is played** and Asterisk's incoming sound is muted (the remote track disabled). It then **sends** on the direct link only: Asterisk's outgoing track is disabled, so that leg carries DTX silence (a few packets a second).
3. **Each side switches on its own evidence**, so a one-way direct route never leaves anybody unheard: a side plays whichever route is actually bringing sound, and keeps sending on Asterisk's route until the other side's reports show its direct sound is arriving there.
4. **Falling back:** if direct sound stops arriving for **1 s** while the call is up, the device unmutes Asterisk's route both ways at once. It moves back to direct when the direct sound has been steady for 5 s again. A link closed by the server, a lost call link or a dropped direct connection is the same fallback.
5. **Mute** silences both routes. **Keypad tones** still go on Asterisk's route (RFC 4733), the way Asterisk expects them.
6. **Group calls:** moving into one already quiets Asterisk's route (`joinGroup`); the direct link is stopped there as now, and LiveKit carries everything.

### 3.3 What doesn't change
- **The server.** The call link already relays whatever the two ends say (`internal/calllink`). The offers simply include an audio section. No migration, no new endpoint.
- **Asterisk's leg stays up for the whole call**, so the call is still a call for CallKit, the Team list, history, presence, transfer and the hang-up. No RTP timeout is configured on the app's endpoints, so a quiet leg isn't dropped (the bench checks this on a long call).
- **Older apps and browsers** that don't add audio to the link just keep Asterisk's sound. A side whose offer has no audio is never switched.

## 4. Security
The direct sound is DTLS-SRTP between the two devices, encrypted end to end, which Asterisk's route isn't (Asterisk decrypts and re-encrypts). Who may link with whom is still decided by Asterisk's channels (ADR-087). Nothing new is exposed: no port, no endpoint, no new data stored. One STRIDE row in `docs/THREAT_MODEL.md`: the call link can now carry an audio offer, under the same 64 KB / 50-a-second limits.

## 5. Resources
- **Server:** less work. Once switched, Asterisk forwards only DTX silence for these calls, and on a direct route nothing passes through the server at all. On mobile data the relay (coturn) forwards the sound instead of Asterisk, the same bytes, without decoding.
- **Phone:** one more audio stream encoded while switching over, then one. The quiet Asterisk leg is a few small packets a second.
- **Data:** the same Opus settings. The quiet leg adds well under 0.1 MB a minute, and the end-to-end reports should cut the FEC overhead on good links. Measured and recorded in `docs/RESOURCES.md`.

## 6. How it is proved (the phone bench, before anything reaches the owner)
- **All 18 existing scenarios pass**, and both phones' records say **which route the sound was on** and when it switched.
- **Sound quality is compared, old against new, scenario by scenario**: lost packets, concealed audio (WebRTC's `concealedSamples`), jitter-buffer delay. The new way must be no worse anywhere and better on the poor links, or it doesn't ship.
- **New scenario: the direct link fails mid-call** (the direct route blocked for 5 s at the relay). Sound must carry on via Asterisk with a gap under 1.5 s, and come back to direct.
- **New scenario: a one-way direct route.** Each side must hear the other the whole call.
- **Browser suite:** a browser-to-browser and an app-like call switch to direct sound and back.
- **Call details** shows **Sound: direct** or **Sound: through Linx**, so the owner's own calls can be read the same way.

## 7. Build order (one app build, one server update for the browser)
1. App: the audio transceiver, the switch-over, the fallback, Call details, unit tests.
2. Browser: the same in `picture.ts` / `line.ts`, and the browser suite.
3. Bench: the route in the records, the old-vs-new comparison, the two new scenarios.
4. `docs/RESOURCES.md`, `docs/THREAT_MODEL.md`, ADR-092 marked as built, the help guide's line about how calls travel.
5. CI green, the full bench, then the owner's go for the server update and the build.

## 8. Left open
- **Recording (Phase 4)** will need the sound where the recorder is. A recorded call would either stay on Asterisk's route or be recorded by the devices. Decided with Phase 4.
- **The full redesign** (Linx running Linx-to-Linx calls without Asterisk) stays parked. This step is the part of it that matters for quality.
