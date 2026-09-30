import { describe, expect, it } from "vitest";
import { guideForScreen, guideInPath, guidePath, type HelpGuideSummary } from "./help";

const g = (name: string, screens: string[]): HelpGuideSummary => ({ name, title: name, section: "admin", screens });

describe("help", () => {
  it("reads a guide's name from its address, and nothing else", () => {
    expect(guideInPath("/help/extensions")).toBe("extensions");
    for (const p of ["/help", "/help/", "/help/Extensions", "/help/a/b", "/help/../admin", "/helpx/a"]) {
      expect(guideInPath(p)).toBeUndefined();
    }
    expect(guidePath("extensions", "adding-one")).toBe("/help/extensions#adding-one");
  });

  it("finds the guide about a screen, preferring the one that names it first", () => {
    const list = [g("desk-phones", ["/admin/lines", "/admin/extensions"]), g("extensions", ["/admin/extensions"]), g("status", ["/admin/system", "/admin/system/status"])];
    expect(guideForScreen(list, "/admin/extensions")?.name).toBe("extensions");
    expect(guideForScreen(list, "/admin/lines")?.name).toBe("desk-phones");
    expect(guideForScreen(list, "/admin/system/status")?.name).toBe("status");
    expect(guideForScreen(list, "/nowhere")).toBeUndefined();
  });
});
