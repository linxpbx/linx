# Linx for iOS / iPadOS

Linx's phone, for iPhone and iPad (`docs/PHASE2.md` §12; build-order steps 1 to 7 are built).
Swift 6, SwiftUI, one app for both, minimum **iOS 26.0**, bundle ID `com.linxpbx.app`.

```
ios/
  Linx.xcodeproj/        the project (checked in; no project generator)
  Linx/
    App/                 LinxApp.swift (which screen is on) and AppModel (what the app is doing)
    Core/                being set up and signed in: the Secure Enclave key, the certificate
                         request (DER), the proof, the Keychain, and the calls to Linx
      SIP/               the app's own SIP: messages, digest, the websocket, the user agent
      Media/             the sound: WebRTC, the Opus settings, the ring
      Call/              the system's side of a call: CallKit, and China's in-app ringing
      Push/              being woken for a call: PushKit, notifications, Apple's tokens
    DesignSystem/        buttons, cards and the page background, all from the tokens
    Features/            one folder per part of the app:
                           Enrollment  setting the phone up
                           Home        the four tabs, and what they share (HomeModel)
                           Phone       the keypad, a call, a video call, and the layout rules
                           Calls       call history
                           Team        the directory, live, with presence
                           Voicemail   the messages, played out of memory
                           Settings    this phone's own settings
    Generated/           DesignTokens.swift — `make tokens`, never edited by hand
    Resources/           Colors.xcassets — `make tokens`, never edited by hand
  LinxTests/             unit tests (Swift Testing)
  tools/                 the simulator helper, the screenshot harness, the WebRTC fetch
  Vendor/                Google's WebRTC (not in git; `make ios-deps` puts it there)
  screenshots/           what `make ios-screens` writes (not committed)
```

## How the app signs in (build step 4a)

No password, ever, on the phone (`docs/PHASE2.md` §4):

1. A setup code reaches the app three ways — the camera reads the QR code, the person pastes
   the link from the setup email, or types the Linx address and 8 characters (`Core/SetupCode.swift`).
2. The app makes one P-256 key **in the Secure Enclave** (`Core/DeviceKey.swift`), writes a
   certificate request for it by hand (`Core/DER.swift`: Apple has no API for one) and sends it
   with the code to `POST /v1/enroll`. Back comes the phone's certificate.
3. From then on it signs a one-minute, single-use ES256 proof with that key (`Core/Proof.swift`)
   and gets a 15-minute token from `POST /v1/device-token`; the certificate renews itself for the
   same key with a month to spare (`Core/PhoneSession.swift`).
4. With that token it asks `POST /api/v1/me/phone-line` for its SIP login, which stays **in
   memory only** — a new password every time the app starts.

What the Keychain holds: the certificate, Linx's CA, the server's address, who the phone is for,
and the Enclave's own blob — all `ThisDeviceOnly`, so nothing follows a backup onto another
phone. The private key itself can never be read, copied or backed up.

**Simulator:** there is no Secure Enclave on some Macs, so `DeviceKey` falls back to a software
key there (debug builds only — a release build on a phone refuses). The camera is left alone in
the simulator: its "camera" is a test pattern and a capture session on it brings the app down,
so the viewfinder shows what to do instead, which is also what the screenshots show. And an
**unsigned simulator build has no Keychain**, which is why `PhoneStore` takes a `PhoneStorage`
and the tests give it one in memory; the real Keychain is checked on a phone.

The two folders `Linx` and `LinxTests` are **synchronised folders**: Xcode builds whatever
files are in them, so adding a Swift file never touches the project file.

## How a call works (build step 4b)

The app is a phone, not a web page in a wrapper: it speaks SIP to Linx's `/sip` relay and
carries the sound over WebRTC, exactly as the browser does, so Asterisk sees the two the same
way (`internal/db/migrations/0041_ios_devices.sql`).

- **SIP, written here** (`Core/SIP`). Every maintained SIP library is GPL or LGPL, which a
  client bundle may not carry (ADR-006), and the subset Linx's relay allows is small:
  REGISTER, INVITE, ACK, BYE, CANCEL, and answers to what Asterisk sends. `SIPMessage` reads
  and writes the text (bodies kept byte for byte, so an SDP's line endings survive),
  `SIPDigest` answers Asterisk's challenge (MD5, the only scheme pjsip offers; checked against
  RFC 2617's own worked example), `SIPTransport` is one websocket to `GET /sip` with the
  device token in the `Authorization` header and the `sip` subprotocol the relay requires, and
  `SIPUserAgent` is the call itself.
- **The sound** (`Core/Media`). `WebRTCMedia` makes one peer connection per call: DTLS-SRTP,
  rtcp-mux, ICE through Linx's own TURN server when there's no direct route, and **the
  browser's Opus settings** (`SDPTweaks`, the port of `web/src/phone/sdp.ts`: in-band FEC, DTX,
  mono, 24 kbps), so a poor network degrades the same way in both. Candidates are gathered
  before the invitation goes out — Asterisk doesn't take them one at a time — with the same
  "a relay candidate, or three seconds" rule the browser uses. Keypad tones go as RFC 4733 in
  the media, not as SIP INFO. `Ringer` makes the ring in memory (no sound file to ship), the
  same two tones every three seconds the web client plays.
- **The screens** (`Features/Phone`). `PhoneModel` is what they watch: the line's state, the
  one call, the last few calls. `KeypadView` is `docs/ui/iOS · Keypad@1x.png` in Cobalt, and
  `CallView` is `docs/ui/iOS · Active call@1x.png` with the buttons that work today (mute,
  keypad, speaker, hang up); hold, transfer, park, record and video arrive with the steps that
  build them, because App Review refuses placeholders.
- **While the app is in front, and no longer.** The websocket opens when the app comes
  forward and closes when it goes away; a call in progress keeps it, and so does a phone a
  push has just woken whose call is still on its way.

## How a sleeping phone rings (build step 6)

A call for someone whose app is asleep goes: Linx asks Apple to wake it → Apple delivers a
**VoIP push** → the app reports the call to **CallKit** at once → the system rings the phone
with the person's own ringtone, on the lock screen, in a car → the app signs its line in while
the server holds the call ringing → Asterisk's invitation arrives and the call is the one the
system is already showing.

- **Ready before there is a screen** (`App/AppDelegate.swift`). PushKit only delivers a call
  to an app that was already listening, and a push to a closed app launches it with no window,
  so registering happens in the delegate's first moment rather than a view's `task`. SwiftUI
  keeps the lifecycle; `AppModel.shared` is the one model the delegate and the screens share.
- **Report first, everything else after** (`AppModel.woken`, `PhoneModel.woken`). iOS kills an
  app that takes a VoIP push without reporting a call, and stops sending pushes to one that
  keeps doing it, so nothing — no token, no network, no database — happens before the report.
  A phone woken for a call that never comes stops ringing after 20 seconds and closes its line
  again, because a registration left behind would make Linx think the phone is awake.
- **The system owns the call** (`Core/Call`). Every button goes one way round: the app **asks**
  the system (`CXCallController`), the system **tells** the app (`CXProviderDelegate`), and
  only then does the app act — so the lock screen, a car and the app's own screen can never
  disagree. `SystemCalls` is the seam, with `CallKitCalls` the real one and `NoSystemCalls`
  for China and the tests.
- **The sound waits to be handed over.** WebRTC is in **manual audio** mode whenever CallKit is
  in charge: a call's sound starts in `didActivate` and nowhere else, which is what stops the
  dropouts on an answered call.
- **Which call is which.** The push carries the call's id, but Asterisk's invitation doesn't,
  so the two are matched on the caller's number (digits only) and otherwise on which push has
  been waiting longest. The phone has one line: a second caller hears busy and goes to
  voicemail, and their push is reported and then ended at once, because iOS is watching.
- **China** (`Core/Call/CallStyle.swift`, ADR-078). Apple doesn't allow CallKit there, so the
  app sends **no PushKit token** and tells Linx `call_alerts` instead: a ringing call arrives as
  a time-sensitive notification to tap, the app rings on its own screen, and the signed-in
  screen says so once.
- **Which Apple** (`Core/Push/PushEnvironment.swift`). A token from a development build only
  works on Apple's sandbox, so the app reads `aps-environment` out of its own provisioning
  profile and tells Linx; a TestFlight phone and an Xcode build both ring without a setting.
- **What it asks iOS for**: background modes `voip` and `audio` and nothing else
  (`ios/Info.plist`), and the Push Notifications entitlement (`ios/Linx.entitlements`, which
  signing turns into `production` for TestFlight and the App Store).

**Not here yet:** the CallKit icon on the lock screen (a monochrome template, worth doing with
the dark and tinted app icons when the app goes to TestFlight), and the real-device tests of
every ringing case, which are the demo's job (`docs/PHASE2.md` §13).

## How a video call works (build step 7)

A picture is **added to a call that is already up**, and never rung as one (`ADR-079`,
`docs/PHASE2.md` §7). The call rings as an ordinary call — which is what the lock screen, a car
and a headset understand — and then:

1. The person presses **Start video**. `PhoneModel.toggleVideo` → `SIPUserAgent.setVideo(true)`.
2. `WebRTCMedia.startVideo` starts the camera (`Core/Media/Camera.swift`: 640×480, 24 frames,
   the smallest format the camera itself can give), adds the track and makes a new offer.
3. The app sends a **re-INVITE** inside the same dialog. The 200 OK's answer goes straight back
   into the peer connection; a refusal (488), both sides asking at once (491) or a mid-call
   challenge each get their own ACK and a **rollback** (RFC 8829), so the call is exactly as it
   was and the sound never stops.
4. The other side's picture arrives as a track on the peer connection and the screen shows it.

Two rules worth keeping in mind when changing any of this:

- **Nobody else switches on this phone's camera.** An offer that adds video is answered
  `recvonly`: their picture appears, and this phone sends nothing until its own button is pressed.
- **Sound comes first.** The picture is capped at 600 kbit/s (`SDPTweaks.capVideo` writes `b=AS`
  and the sender's own settings carry it), and when WebRTC reports under 150 kbit/s to spare for
  three readings running the camera goes off by itself and the person is told the call carries on.

The layout is decided by the **size of the screen**, never by the device (`CallLayout`): a phone
upright, a phone on its side, and two panels on an iPad or an iPhone Duo opened out — side by
side when the screen is square-ish, so that nothing anyone presses sits on the crease. Build-order
step 8 replaces that near-square rule with the fold's own geometry (`ReservedRegion`,
`ArrangementView`, iOS 27.1) and leaves everything else alone.

## Google's WebRTC

`make ios-deps` fetches the prebuilt **WebRTC M154** XCFramework (BSD, ADR-006), checks it
against the SHA-256 pinned in `tools/webrtc.sh`, and unpacks it into `ios/Vendor` — which is
not in git, because it is about 100 MB unpacked. `make ios-build`, `ios-test` and `ios-screens`
depend on it, so there is nothing extra to remember; CI caches the zip by version.

It is fetched rather than managed by Swift Package Manager because SwiftPM's own binary-artifact
download hangs on this Mac (Xcode and `swift package` both stall after checkout, while `curl`
fetches the same URL in seconds — most likely a firewall that has to be allowed per program).
A script we own also makes the pin plain to read and the download cacheable in CI. Moving back
to a Swift package later is a small change: one `XCRemoteSwiftPackageReference` in the project
and this script deleted.

It costs **12 MB** of the app's 13 MB (`docs/RESOURCES.md`); the App Store ships only the one
slice a phone needs.

## Day to day

| | |
|---|---|
| `make ios-lint` | formatting (`swift-format`, settings in `ios/.swift-format`) |
| `make ios-build` | builds for the simulator, no signing and no Apple account |
| `make ios-deps` | fetches Google's WebRTC (pinned version, checked against its SHA-256) |
| `make ios-test` | the unit tests on a simulator (`IOS_SIM_DEVICE="iPad Pro 11-inch (M5)"` to choose one) |
| `make ios-screens` | every screen, light and dark, into `ios/screenshots/` — compare them with the mockups in `docs/ui/`. The call screens are shot on their side as well (the app is asked to turn, `-LinxOrientation landscape`). `LINX_IOS_DEVICE="iPad Pro 11-inch (M5)"` for the iPad, and `LINX_IOS_DEVICE="iPhone Duo" DEVELOPER_DIR=/Applications/Xcode-27.1-beta.app` for the foldable |

`ios/tools/sim.sh "iPhone 17"` prints a booted simulator's id, creating one if needed; on a
machine with an older Xcode it falls back to the newest iPhone that Xcode has.

## The app icon

`design/app-icon.svg` is the source: the Linx mark in white on Cobalt (the owner chose it on
2026-10-03), both colours from `design/tokens.json`. `ios/tools/icon.sh` renders it into
`Linx/Resources/Assets.xcassets/AppIcon.appiconset/AppIcon-1024.png` **without an alpha
channel**, which the App Store insists on; run it after changing the SVG and commit the PNG with
it. The SVG lives beside the tokens rather than in `Resources/`, so it is never shipped inside
the app. Dark and tinted variants (iOS 18's three-icon set) aren't there yet — one plain icon is
valid, and they are worth a look together when the app goes to TestFlight.

**Colours, spacing and radii come only from `design/tokens.json`.** `make tokens` writes
`Linx/Resources/Colors.xcassets` and `Linx/Generated/DesignTokens.swift` (`LinxColor`,
`LinxSpace`, `LinxRadius`); `make lint` fails if either is stale. A unit test checks every
colour set against the tokens in both appearances. Type is the system font with Dynamic
Type — iOS uses no Linx font files (`docs/ui/DESIGN_TOKENS.md`).

## CI

The `ios` job in `.github/workflows/ci.yml` runs the commands above on a `macos-26` runner on
every push and pull request: formatting, the simulator build, the unit tests, and a **Release
build for a real iPhone** with no signing — that last one catches the mistakes that otherwise
only appear when the app is archived for TestFlight. WebRTC's zip is cached by its pinned
version, so only the first run of a version downloads it. No Apple account, no signing, no
secrets. The runner's newest Xcode is 26.x (the iOS 26 SDK, which is the app's minimum); the
fold layouts of build-order step 8 need the iOS 27.1 SDK, which only this Mac has so far.

## Tools on this Mac (set up 2026-10-03)

| | |
|---|---|
| **Xcode 27.0** (`/Applications/Xcode.app`) | The active toolchain (`xcode-select -p`). Builds releases and whatever CI builds. |
| **Xcode 27.1 beta** (`/Applications/Xcode-27.1-beta.app`, 27A9269) | The iOS 27.1 SDK and the **iPhone Duo** simulator. Use it for the app: `DEVELOPER_DIR=/Applications/Xcode-27.1-beta.app/Contents/Developer`. |
| **Xcode's own MCP server** (`xcode` in `.mcp.json`) | `xcrun mcpbridge` — a bridge to the **running** Xcode's tool service: new project/target from Apple's templates, build and run, build log, per-file diagnostics, SwiftUI preview snapshots, test runs, tap/swipe/type on a simulator or device, String Catalog and build settings. It needs Xcode **open** with the project; the wrapper in `.mcp.json` attaches to Xcode 27.1 beta when that one is running, otherwise to the `xcode-select` Xcode. |
| **XcodeBuildMCP** (in the user's own config) | Headless build, simulator boot, screenshots — no Xcode window needed. Use it for `make ios-screens` and anything scripted. |

`xcrun mcpbridge run-agent claude` is the other direction: it starts Claude Code from Xcode with Xcode's configuration and skills (`xcrun agent skills export` writes them out).
