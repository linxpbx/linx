# Linx — Design tokens and screen index

The source of truth will be `design/tokens.json` (Phase 0b). It generates CSS variables for web and a Swift asset catalog for iOS. This file is the human-readable spec that `tokens.json` is built from.

## Brand (ADR-016)
The mockups used a placeholder teal. **Linx Cobalt replaces it everywhere** that teal appears: primary buttons, the selected nav item, focus rings, links, avatar tint and the active-speaker border.

| Token | Light | Dark | Use |
|---|---|---|---|
| `color.brand` | `#1F5FD6` Cobalt | `#7FB0FF` Signal (text/links), `#1F5FD6` (filled buttons) | primary actions, selection |
| `color.bg` | `#F4F3EF` Ivory | `#17191E` Ink | app background |
| `color.surface` | `#FFFFFF` | `#22252C` | cards, tables, panels |
| `color.surface-dark` | `#17191E` Ink | `#17191E` | sidebar, in-call screen, meeting stage |
| `color.text` | `#17191E` | `#F4F3EF` | body text |
| `color.text-muted` | `#5B5F68` | `#A6AAB3` | secondary text |
| `color.border` | `#E3E1DA` | `#343842` | dividers, outlines |
| `color.call` | `#1E8E4E` | `#1E8E4E` | answer button, "available" |
| `color.end` | `#C53030` | `#C53030` | hang-up, destructive, alerts |

All text/background pairs must meet WCAG 2.2 AA. This is verified by a contrast test in CI (Phase 0b).

## Presence status colours (brief-mandated, identical on all platforms)
| Status | Token | Light | Dark |
|---|---|---|---|
| Available | `status.available` | `#1E8E4E` green | `#1E8E4E` |
| On a call / busy | `status.busy` | `#C53030` red | `#E05252` |
| Ringing | `status.ringing` | `#C27A12` amber | `#C27A12` |
| Away | `status.away` | `#9C7F0A` yellow | `#B8960C` |
| Do not disturb | `status.dnd` | `#7A3FC4` purple | `#9D6FE3` |
| Reachable via push | `status.push` | `#5A7A9A` grey-blue | `#5A7A9A` |
| Offline | `status.offline` | `#8A8E96` grey | `#8A8E96` |
| In a meeting | `status.meeting` | `#1F5FD6` cobalt | `#5B8FEA` |

Some dark-mode shades (and the light-mode away yellow) were lightened or darkened so that every status dot reaches the WCAG 3:1 minimum for non-text elements. This is enforced by `internal/tokens` tests.

Status is never shown by colour alone. It always has a text label, and the dot has an accessible name.

## Typography
| Role | Web | iOS |
|---|---|---|
| Display / headings | Instrument Sans (600–700) | SF Pro (system) with Dynamic Type; Instrument Sans only for branding moments |
| Body | IBM Plex Sans | SF Pro (system) |
| Numbers (dial string, timers, extensions) | IBM Plex Mono | SF Mono |
| Arabic | IBM Plex Sans Arabic | SF Arabic (system) |

iOS uses system fonts per HIG and the brief. Web self-hosts the IBM Plex and Instrument Sans files (no Google Fonts call on the guest page, which keeps it under 300 KB).

## Spacing, radii
- Spacing scale: 4, 8, 12, 16, 20, 24, 32, 40
- Radii: `sm` 8 (inputs, chips), `md` 12 (cards, buttons), `lg` 16 (panels, call card), `full` (dial keys, avatars, pills)

## Logo
The X mark and the "linx" wordmark come from `A · Meeting point@1x.png`. It's a cobalt app icon; on dark backgrounds the wordmark is white with a Signal-blue X. **A vector (SVG) source is needed from the owner before release.** Until then, a traced SVG placeholder is used.

## Screen index (approved mockups → build phase)
| Mockup file | Screen | Phase |
|---|---|---|
| `iOS · Keypad@1x.png` | iOS dialer: status pill, network pill, 5-tab bar | 2 |
| `iOS · Team & presence@1x.png` | iOS Team: search, filter chips, favourites, department sections | 2/4 |
| `iOS · Active call@1x.png` | iOS in-call (dark): quality pill, low-data toggle, 3×3 controls | 2 |
| `iOS · QR setup@1x.png` | iOS enrollment step 1 of 3 | 2 |
| `Web · Console & presence@1x.png` | Web shell: sidebar, Team table, right panel (call, park slots, queue) | 1/4 |
| `Web · Meeting & invites@1x.png` | Meeting grid, E2EE badge + code, invite panel, lobby | 3 |
| `Web · Guest join page@1x.png` | Guest join card | 3 |
| `Colour, type and taglines@1x.png` | Brand palette and type | — |
| `A · Meeting point@1x.png`, `Screenshot …9.44.21 AM.png` | Logo / app icon | — |
