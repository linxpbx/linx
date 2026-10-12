# Linx — video calls that work: one to one, and adding people

*Owner's go-ahead 2026-10-12 ("Do one to one and add people as well"), after build 26's video call between their iPad and iPhone was poor: one-way picture, no sound from the iPad, slow to start, never smooth. ADR-087 and ADR-088 record the decisions; this file is the design and the build order.*

## 1. In plain words
- **Calls still ring exactly as they do now:** push, the lock screen, CallKit, Answer, the browser's ring. Desk phones and outside numbers are untouched.
- **Between two Linx users** (the app or the browser, either side), the **picture goes straight from one device to the other**, the way FaceTime and WhatsApp do one-to-one calls. The sound keeps the route it has today, through the phone system, so the voice never depends on the picture.
- **Video on and off mid-call** is a camera switched on or off on that direct link: a second, no message through the phone system, no prompt that comes and goes.
- **The browser does video** from now on, so app ↔ browser video calls work.
- **Add person** in a Linx-to-Linx call turns it into a **group call** on Linx's own video server (LiveKit, ADR-002): everyone in it moves there together, the new person is rung like any call, and each person leaves on their own. Up to **8 people**.
- **Desk phones and outside numbers join group calls in Phase 3** (LiveKit's phone bridge), as planned.

## 2. Why the picture leaves Asterisk (ADR-087)
The 2026-10-12 22:29 call (both phones' Call records, Asterisk's log): the network was fine (iPad direct, 3 ms; iPhone relayed over UDP, 29 ms). What went wrong was the path. **Asterisk bridges video as a plain relay**: it doesn't pass on the messages two WebRTC ends use to agree on quality (loss and bandwidth feedback, "send a fresh picture"), so each phone guessed — choppy, HD made no difference. And adding a camera was a **re-INVITE through Asterisk to each side in turn**: the iPhone's answer and its camera offer arrived together 4.5 s late, Asterisk renegotiated the iPad's side, and the iPad never showed the iPhone's picture. Two months of fixes have been fixes to that path.

A direct WebRTC link between the two devices has none of this: one connection, end-to-end congestion control and keyframe requests, and the picture can be turned on and off locally.

## 3. One to one: the direct picture
### 3.1 The call link (server)
`GET /api/v1/call-link?call=<SIP Call-ID>` — a websocket each Linx device opens **once its call is answered**. It takes what `GET /api/v1/team/live` takes: a browser session (with the `Origin` check) or an app's device token.
- **Who is in the call is Asterisk's word, not the device's.** The control plane asks ARI for the channel whose SIP Call-ID (`protocol_id`) is the one given **and** whose endpoint is one of the asking person's own devices; anything else is refused. The other side is that channel's `BRIDGEPEER`, and its endpoint names its device. A device can only ever be linked to the person it is actually talking to.
- **Both ends connected = paired.** Each is told `{"type":"peer","video":true,"polite":…}`; if the other side is not a Linx device (a desk phone, a trunk) it is told `video:false` and the video button stays off, as today.
- **Then it only relays:** `{"type":"signal",…}` from one end is passed to the other unchanged (offers, answers, ICE candidates). At most 64 KB a message and 50 a second. When either end goes, the other is told `peer-gone`. Channels are re-checked every 15 s (the `/sip` relay's rule), so a link never outlives its call.
- In-memory, one hub in the control plane; nothing stored.

### 3.2 The direct link (app and browser)
A second `RTCPeerConnection` beside the call's own, **for the picture only**, made when the link pairs and kept for the whole call, so turning video on is adding a track, not a new call. *Perfect negotiation* (the WebRTC pattern for two sides that may both offer: `polite` decides who gives way). ICE through Linx's own TURN with the credentials the call already has.
- **coturn** may now relay to its own relay address as well as Asterisk's (two relayed devices reach each other through it), and to LiveKit's (§4). Every other peer stays refused; TURN's own permissions stop anyone sending to an allocation that didn't ask for them.
- **Quality**: H.264 first on the app (hardware), WebRTC's own congestion control, a cap per the route (ADR-081's ceilings), and the camera off by itself when there isn't room for voice. ADR-081's ladder stays as the cap; WebRTC does the adapting.
- **Lip sync**: sound and picture take different paths, so they are not tied together; both are short on the same network. Measured and recorded in `docs/RESOURCES.md`; if it's noticeable, the sound moves onto the direct link too (§8).

### 3.3 Ringing as a video call (replaces ADR-086 (1))
The INVITE carries **no video at all**; the app and the browser send `X-Linx-Video: yes` on a video call. The dialplan carries it to every leg it rings and to the wake push (`Video:` on `LinxWake`), so CallKit and the browser show "Video call" while it rings, as now. Migration 0052 takes `h264,vp8` and the video stream off the app's and the browser's endpoints (ADR-086 (3)'s bundling stays): Asterisk carries sound only. An older app that still offers a picture gets the stream refused and the call goes on as sound.

## 4. Adding people: group calls on LiveKit (ADR-088)
### 4.1 What happens
1. In a Linx-to-Linx call, either person presses **Add person** and picks someone from the Team list.
2. The control plane makes a **room**, gives both people a LiveKit token over the call link (`{"type":"move",…}`), and **rings the new person** through Asterisk (ARI originate into a small `linx-room-invite` context that rings their devices with the wake push, as any call), with `X-Linx-Room` on the INVITE.
3. Both join the room, their pictures and sound go there, and they **end their SIP call** (BYE) — the app keeps its CallKit call; only what carries it changed. Each person now leaves on their own.
4. The new person answers; their device sees `X-Linx-Room`, asks for its token over the call link (the server knows that channel carries that room), joins, and ends its SIP leg.
5. Anyone in the room can **Add person** again, up to 8. The room closes when the last person leaves.

### 4.2 Who may join a room
A token is minted only for a device whose own live channel carries that room (the invite) or that was in the call that became the room. Tokens are HS256 with LiveKit's key (a Docker secret the installer generates), one room, publish and subscribe, five-minute life (LiveKit keeps a joined participant in). Room names are random. **Presence**: the control plane counts room members as "on a call" in the Team list.

### 4.3 LiveKit on the server
- `linx-livekit`: the pinned upstream image (Apache-2.0, Go), non-root, read-only, `mem_limit` sized from measurement. It runs all the time: starting it only for a room would need Docker control the control plane must never have (security rule), so it is kept small instead (measured idle and in a 3-person call, `docs/RESOURCES.md`).
- **Signalling** through the control plane at `/livekit/` (a websocket proxy to `linx-livekit:7880` on `linx-private`), so **no new hostname, port or front-door change**.
- **Media**: one UDP port (`7882`, ICE/UDP mux) published on the LAN address only, like Asterisk's audio, and reached through coturn on `linx-media` from anywhere else. LiveKit's own TURN is off; clients use Linx's.
- **The phones**: the browser uses `livekit-client` (Apache-2.0), loaded only when a group call starts. The app speaks LiveKit's signalling itself on the WebRTC it already has (one video engine, one audio session under CallKit), with a small protobuf reader/writer for the messages it uses.

## 5. Security (STRIDE rows go into `docs/THREAT_MODEL.md` with the build)
- The call link trusts Asterisk's channels, not what a device says; relayed messages are size- and rate-limited and never interpreted by the server.
- A direct link exposes a device's addresses to the other person in the call — the same as any WebRTC call app; relay-only is still possible and is what the app falls back to.
- coturn's peer list grows by two addresses, both inside the server.
- LiveKit's API is reachable only on `linx-private`; tokens are minted only by the control plane; media is DTLS-SRTP. Group calls are **not end-to-end encrypted** (as calls through Asterisk aren't); E2EE stays Phase 3's meetings.

## 6. Resources
Nothing new for a 1:1 call: the picture no longer passes through Asterisk at all, and directly connected devices cost the server nothing. LiveKit is the one new service; its idle and busy cost, and the data a minute of a group call uses, are measured and written into `docs/RESOURCES.md` before release.

## 7. Build order (one release at the end: one server update, one app build)
1. **Server, 1:1:** the video header through the dialplan and the push, migration 0052, the call link and its ARI checks, coturn's peers. Call suite: pairing proved on the real Asterisk.
2. **Browser, 1:1:** the direct picture, the **Video call** button working, mid-call video on/off. Browser suite: two browsers on a video call.
3. **App, 1:1:** the direct picture replacing video through SIP; the old SIP-video code removed.
4. **Server, groups:** `linx-livekit`, its secret and config, the `/livekit/` proxy, tokens, Add person (originate, the invite context), presence.
5. **Browser, groups:** the group call screen.
6. **App, groups:** LiveKit signalling, the group call screen (iPhone, iPad).
7. **Finish:** help guides, `docs/RESOURCES.md`, `docs/THREAT_MODEL.md`, `docs/HISTORY.md`, the release.

## 8. Left open
- Moving the sound onto the direct link too (lip sync, one less hop) if measurement says so.
- Desk phones and outside numbers in a group call: Phase 3, `livekit-sip`.
- The 4.5 s between the iPhone sending its answer and Asterisk receiving it on the 22:29 call: looked at in step 3.
