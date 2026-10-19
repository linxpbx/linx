import { describe, expect, it } from "vitest";

import { cancelled, noPasskeyUsed } from "./passkey";

describe("a passkey attempt that ends without one being used", () => {
  // Browsers answer NotAllowedError both when someone closes the prompt and
  // when the attempt genuinely fails, and never say which. These two tests
  // are the pair: cancelled() cannot tell them apart, so anything the person
  // started themselves has to answer with a line that fits either.
  it("cannot be told apart from the person closing the prompt", () => {
    expect(cancelled(new DOMException("", "NotAllowedError"))).toBe(true);
    expect(cancelled(new DOMException("", "AbortError"))).toBe(true);
  });

  it("is not confused with a fault the browser does name", () => {
    expect(cancelled(new DOMException("", "SecurityError"))).toBe(false);
    expect(cancelled(new DOMException("", "InvalidStateError"))).toBe(false);
    expect(cancelled(new Error("network"))).toBe(false);
  });

  // The old behaviour was to say nothing at all here, which left a real
  // failure looking like a dead button (the owner's "confirm it's you",
  // 2026-10-19).
  it("always says something, and names the way out", () => {
    const said = noPasskeyUsed("Try again, or confirm with your password below.");
    expect(said).not.toBe("");
    expect(said).toContain("No passkey was used");
    expect(said).toContain("password");
  });
});
