# Linx for iOS / iPadOS

The app's skeleton, built in **Phase 2, build-order step 1** (`docs/PHASE2.md` §12). Swift 6,
SwiftUI, one app for iPhone and iPad, minimum **iOS 26.0**, bundle ID `com.linxpbx.app`.

```
ios/
  Linx.xcodeproj/        the project (checked in; no project generator)
  Linx/
    App/                 LinxApp.swift — the app and its first screen
    DesignSystem/        buttons, cards and the page background, all from the tokens
    Features/            one folder per part of the app (Enrollment is the first)
    Generated/           DesignTokens.swift — `make tokens`, never edited by hand
    Resources/           Colors.xcassets — `make tokens`, never edited by hand
  LinxTests/             unit tests (Swift Testing)
  tools/                 the simulator helper and the screenshot harness
  screenshots/           what `make ios-screens` writes (not committed)
```

The two folders `Linx` and `LinxTests` are **synchronised folders**: Xcode builds whatever
files are in them, so adding a Swift file never touches the project file.

## Day to day

| | |
|---|---|
| `make ios-lint` | formatting (`swift-format`, settings in `ios/.swift-format`) |
| `make ios-build` | builds for the simulator, no signing and no Apple account |
| `make ios-test` | the unit tests on a simulator (`IOS_SIM_DEVICE="iPad Pro 11-inch (M5)"` to choose one) |
| `make ios-screens` | every screen, light and dark, into `ios/screenshots/` — compare them with the mockups in `docs/ui/` |

`ios/tools/sim.sh "iPhone 17"` prints a booted simulator's id, creating one if needed; on a
machine with an older Xcode it falls back to the newest iPhone that Xcode has.

**Colours, spacing and radii come only from `design/tokens.json`.** `make tokens` writes
`Linx/Resources/Colors.xcassets` and `Linx/Generated/DesignTokens.swift` (`LinxColor`,
`LinxSpace`, `LinxRadius`); `make lint` fails if either is stale. A unit test checks every
colour set against the tokens in both appearances. Type is the system font with Dynamic
Type — iOS uses no Linx font files (`docs/ui/DESIGN_TOKENS.md`).

## CI

The `ios` job in `.github/workflows/ci.yml` runs the three commands above on a `macos-26`
runner on every push and pull request. No Apple account, no signing, no secrets. The runner's
newest Xcode is 26.x (the iOS 26 SDK, which is the app's minimum); the fold layouts of
build-order step 8 need the iOS 27.1 SDK, which only this Mac has so far.

## Tools on this Mac (set up 2026-10-03)

| | |
|---|---|
| **Xcode 27.0** (`/Applications/Xcode.app`) | The active toolchain (`xcode-select -p`). Builds releases and whatever CI builds. |
| **Xcode 27.1 beta** (`/Applications/Xcode-27.1-beta.app`, 27A9269) | The iOS 27.1 SDK and the **iPhone Duo** simulator. Use it for the app: `DEVELOPER_DIR=/Applications/Xcode-27.1-beta.app/Contents/Developer`. |
| **Xcode's own MCP server** (`xcode` in `.mcp.json`) | `xcrun mcpbridge` — a bridge to the **running** Xcode's tool service: new project/target from Apple's templates, build and run, build log, per-file diagnostics, SwiftUI preview snapshots, test runs, tap/swipe/type on a simulator or device, String Catalog and build settings. It needs Xcode **open** with the project; the wrapper in `.mcp.json` attaches to Xcode 27.1 beta when that one is running, otherwise to the `xcode-select` Xcode. |
| **XcodeBuildMCP** (in the user's own config) | Headless build, simulator boot, screenshots — no Xcode window needed. Use it for `make ios-screens` and anything scripted. |

`xcrun mcpbridge run-agent claude` is the other direction: it starts Claude Code from Xcode with Xcode's configuration and skills (`xcrun agent skills export` writes them out).
