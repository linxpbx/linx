# Linx for iOS / iPadOS

The Xcode project arrives in **Phase 2** (see `docs/ROADMAP.md`).

Already here: `Linx/Resources/Colors.xcassets` is generated from `design/tokens.json` by `make tokens`. Don't edit it by hand.

## Tools on this Mac (set up 2026-10-03)

| | |
|---|---|
| **Xcode 27.0** (`/Applications/Xcode.app`) | The active toolchain (`xcode-select -p`). Builds releases and whatever CI builds. |
| **Xcode 27.1 beta** (`/Applications/Xcode-27.1-beta.app`, 27A9269) | The iOS 27.1 SDK and the **iPhone Duo** simulator. Use it for the app: `DEVELOPER_DIR=/Applications/Xcode-27.1-beta.app/Contents/Developer`. |
| **Xcode's own MCP server** (`xcode` in `.mcp.json`) | `xcrun mcpbridge` — a bridge to the **running** Xcode's tool service: new project/target from Apple's templates, build and run, build log, per-file diagnostics, SwiftUI preview snapshots, test runs, tap/swipe/type on a simulator or device, String Catalog and build settings. It needs Xcode **open** with the project; the wrapper in `.mcp.json` attaches to Xcode 27.1 beta when that one is running, otherwise to the `xcode-select` Xcode. |
| **XcodeBuildMCP** (in the user's own config) | Headless build, simulator boot, screenshots — no Xcode window needed. Use it for `make ios-screens` and anything scripted. |

`xcrun mcpbridge run-agent claude` is the other direction: it starts Claude Code from Xcode with Xcode's configuration and skills (`xcrun agent skills export` writes them out).
