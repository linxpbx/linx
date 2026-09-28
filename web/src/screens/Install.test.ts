import { describe, expect, it } from "vitest";
import { isSecurePage } from "@/screens/Install";

describe("isSecurePage", () => {
  it("the first page on port 6464 isn't the secure page, even over HTTPS", () => {
    expect(isSecurePage({ protocol: "https:", port: "6464" })).toBe(false);
    expect(isSecurePage({ protocol: "http:", port: "6464" })).toBe(false);
  });
  it("https://<domain> is the secure page", () => {
    expect(isSecurePage({ protocol: "https:", port: "" })).toBe(true);
  });
});
