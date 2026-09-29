import { describe, expect, it } from "vitest";
import { activitySentence } from "./activity";

describe("activitySentence", () => {
  it("reads common actions", () => {
    expect(activitySentence({ action: "user.create", result: "ok", detail: { name: "Sara Haddad" } })).toBe('Added person "Sara Haddad"');
    expect(activitySentence({ action: "extension.update", result: "ok", detail: { number: "110" } })).toBe("Changed extension 110");
    expect(activitySentence({ action: "user.sign_in", result: "ok" })).toBe("Signed in");
    expect(activitySentence({ action: "trunk.delete", result: "ok" })).toBe("Removed phone line");
  });
  it("says what was refused, and why, never what was typed", () => {
    expect(activitySentence({ action: "user.sign_in_code", result: "denied", detail: { reason: "wrong_code" } }))
      .toBe("Refused: entered a sign-in code (wrong code)");
  });
  it("still reads an action it doesn't know", () => {
    expect(activitySentence({ action: "meeting.recording_started", result: "ok" })).toBe("Recording started meeting");
  });
});
