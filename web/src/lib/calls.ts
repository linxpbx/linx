// Call history in words (docs/ui/SCREENS_PHASE1F.md §13, ADR-070), for
// the Call history tab, Admin → Calls and the Dialer's Recent list.
import type { components } from "@/api/schema";

export type CallRecord = components["schemas"]["CallRecord"];
type Party = components["schemas"]["CallHistoryParty"];

/** "Aisha (103)", "Ahmed Ali (+971501234567)", "+971501234567", "A withheld number". */
export function partyLabel(p: Party): string {
  if (p.number === "*43") return "Echo test";
  if (p.name && p.number && p.name !== p.number) return `${p.name} (${p.number})`;
  return p.number || p.name || "A withheld number";
}

/** Who the call was with, from the person's own point of view. */
export function otherParty(c: CallRecord): string {
  return c.placed_by_me ? partyLabel(c.to) : partyLabel(c.from);
}

/** The number to call back ("" when there's none: withheld, or the echo test). */
export function callBackNumber(c: CallRecord): string {
  const n = c.placed_by_me ? c.to.number : c.from.number;
  return n === "*43" ? "" : n;
}

/** "45 s", "3 min" */
export function talkWords(seconds: number): string {
  if (seconds < 60) return `${seconds} s`;
  return `${Math.round(seconds / 60)} min`;
}

/** "3:02" */
export function talkClock(seconds: number): string {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

const ENDED: Record<string, string> = {
  not_available: "Nobody was available",
  not_in_use: "Number not in use",
  closed: "Closed",
  not_permitted: "Not allowed from this phone",
  no_lines: "No outside line free",
  limit_reached: "Too many outside calls",
  echo_test: "Echo test",
  busy: "Busy",
  failed: "Couldn't connect",
};

/** What happened, in a few words: "Missed", "No answer", "3 min", "Left a voicemail". */
export function resultWords(c: CallRecord, mine: boolean): string {
  const left = c.voicemail?.id ? "Left a voicemail" : "Reached voicemail";
  switch (c.result) {
    case "answered":
      return talkWords(c.talk_seconds);
    case "missed":
      return mine && c.placed_by_me ? "No answer" : "Missed";
    case "voicemail":
      if (mine && c.placed_by_me) return left;
      return c.rang_unanswered || mine ? `Missed · ${left.toLowerCase()}` : left;
    default:
      return ENDED[c.result] ?? c.result;
  }
}

/** The short result for Admin → Calls' column: "Answered", "Missed · VM". */
export function resultShort(c: CallRecord): string {
  switch (c.result) {
    case "answered":
      return "Answered";
    case "missed":
      return "Missed";
    case "voicemail":
      return c.rang_unanswered ? "Missed · VM" : "Voicemail";
    default:
      return ENDED[c.result] ?? c.result;
  }
}

/** Whether to show it as missed (in my history: I missed it). */
export function isMissed(c: CallRecord): boolean {
  return c.missed && !c.placed_by_me;
}

/** "TODAY", "YESTERDAY", "MONDAY", "3 SEP" heading for the day a call started. */
export function dayHeading(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((day(now) - day(d)) / 86_400_000);
  if (days === 0) return "Today";
  if (days === 1) return "Yesterday";
  if (days > 1 && days < 7) return d.toLocaleDateString(undefined, { weekday: "long" });
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: days > 300 ? "numeric" : undefined });
}

/** "10:42" */
export function timeOf(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
}

/** Calls grouped by the day they started, newest first. */
export function byDay(calls: CallRecord[], now = new Date()): { day: string; calls: CallRecord[] }[] {
  const out: { day: string; calls: CallRecord[] }[] = [];
  for (const c of calls) {
    const day = dayHeading(c.started_at, now);
    const last = out[out.length - 1];
    if (last && last.day === day) last.calls.push(c);
    else out.push({ day, calls: [c] });
  }
  return out;
}
