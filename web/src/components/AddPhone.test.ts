// The words the Add phone box and the phone lists use (docs/PHASE2.md §4).
import { describe, expect, it } from "vitest";
import { countdownWords, lastSeenWords } from "./AddPhone";

const now = Date.UTC(2026, 9, 3, 12, 0, 0);
const at = (ms: number) => new Date(now + ms).toISOString();

describe("countdownWords", () => {
  it("counts a setup code down, then says it has expired", () => {
    expect(countdownWords(at(10 * 60_000), now)).toBe("10 minutes left");
    expect(countdownWords(at(100_000), now)).toBe("2 minutes left");
    expect(countdownWords(at(45_000), now)).toBe("45 seconds left");
    expect(countdownWords(at(1_000), now)).toBe("1 second left");
    expect(countdownWords(at(0), now)).toBe("Expired");
    expect(countdownWords(at(-60_000), now)).toBe("Expired");
  });
});

describe("lastSeenWords", () => {
  it("says when a phone was last in touch", () => {
    expect(lastSeenWords(undefined, now)).toBe("never");
    expect(lastSeenWords(at(-30_000), now)).toBe("just now");
    expect(lastSeenWords(at(-20 * 60_000), now)).toBe("20 minutes ago");
    expect(lastSeenWords(at(-5 * 3600_000), now)).toBe("5 hours ago");
    // Older than a day and a half: the date itself.
    expect(lastSeenWords(at(-9 * 24 * 3600_000), now)).toMatch(/September/);
  });
});
