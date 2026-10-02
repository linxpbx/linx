import { describe, expect, it } from "vitest";
import { byDay, callBackNumber, otherParty, resultShort, resultWords, talkWords, type CallRecord } from "./calls";

const base: CallRecord = {
  id: "1", direction: "internal", started_at: "2026-10-02T10:42:00Z", ended_at: "2026-10-02T10:45:00Z", talk_seconds: 0,
  result: "answered", missed: false, rang_unanswered: false, placed_by_me: false,
  from: { number: "103", name: "Aisha", extension_id: "e3" }, to: { number: "101", name: "Sara", extension_id: "e1" }, steps: [],
};

describe("call history words", () => {
  it("names the other party from my side", () => {
    expect(otherParty(base)).toBe("Aisha (103)");
    expect(otherParty({ ...base, placed_by_me: true, to: { number: "+97145551234", name: "" } })).toBe("+97145551234");
    expect(otherParty({ ...base, from: { number: "", name: "" } })).toBe("A withheld number");
    expect(callBackNumber({ ...base, placed_by_me: true, to: { number: "*43", name: "" } })).toBe("");
    expect(callBackNumber(base)).toBe("103");
  });

  it("says what happened", () => {
    expect(resultWords({ ...base, talk_seconds: 185 }, true)).toBe("3 min");
    expect(resultWords({ ...base, talk_seconds: 45 }, true)).toBe("45 s");
    expect(resultWords({ ...base, result: "missed", missed: true }, true)).toBe("Missed");
    expect(resultWords({ ...base, result: "missed", placed_by_me: true }, true)).toBe("No answer");
    const vm: CallRecord = { ...base, result: "voicemail", rang_unanswered: true, missed: true, voicemail: { box: "Sara (101)", id: "v1", duration_ms: 42000 } };
    expect(resultWords(vm, true)).toBe("Missed · left a voicemail");
    expect(resultWords({ ...vm, placed_by_me: true }, true)).toBe("Left a voicemail");
    expect(resultShort(vm)).toBe("Missed · VM");
    expect(resultShort({ ...base, result: "not_in_use" })).toBe("Number not in use");
    expect(talkWords(60)).toBe("1 min");
  });

  it("groups by day", () => {
    const now = new Date("2026-10-02T18:00:00");
    const days = byDay([
      { ...base, id: "a", started_at: new Date("2026-10-02T10:00:00").toISOString() },
      { ...base, id: "b", started_at: new Date("2026-10-02T09:00:00").toISOString() },
      { ...base, id: "c", started_at: new Date("2026-10-01T17:00:00").toISOString() },
    ], now);
    expect(days.map((d) => [d.day, d.calls.length])).toEqual([["Today", 2], ["Yesterday", 1]]);
  });
});
