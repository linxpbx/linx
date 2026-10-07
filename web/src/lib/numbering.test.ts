import { describe, expect, it } from "vitest";
import { avoidedRanges, blockedDigit, defaultRanges } from "./numbering";

describe("numbering plan by country (ADR-085)", () => {
  it("keeps the usual plan where the national prefix is 0", () => {
    expect(defaultRanges(3, "0")).toEqual({
      people: { from: 100, to: 599 }, groups: { from: 600, to: 699 }, reserved: { from: 700, to: 899 },
    });
    expect(avoidedRanges(3, "0")).toEqual([{ from: 900, to: 999 }]);
  });

  it("moves past 1 in North America", () => {
    expect(blockedDigit("1")).toBe(1);
    expect(defaultRanges(3, "1")).toEqual({
      people: { from: 200, to: 699 }, groups: { from: 700, to: 799 }, reserved: { from: 800, to: 899 },
    });
    expect(avoidedRanges(3, "1")).toEqual([{ from: 100, to: 199 }, { from: 900, to: 999 }]);
    expect(defaultRanges(4, "1").people).toEqual({ from: 2000, to: 6999 });
  });

  it("stops before 8 in Russia", () => {
    expect(defaultRanges(3, "8")).toEqual({
      people: { from: 100, to: 599 }, groups: { from: 600, to: 699 }, reserved: { from: 700, to: 799 },
    });
    expect(avoidedRanges(3, "8")).toEqual([{ from: 800, to: 899 }, { from: 900, to: 999 }]);
  });

  it("treats no prefix like 0", () => {
    expect(blockedDigit("")).toBeNull();
    expect(defaultRanges(3).people).toEqual({ from: 100, to: 599 });
  });
});

import { withArticle } from "./countries";

describe("country names in sentences", () => {
  it("adds 'the' where English does", () => {
    expect(withArticle("United Arab Emirates")).toBe("the United Arab Emirates");
    expect(withArticle("United States")).toBe("the United States");
    expect(withArticle("Netherlands")).toBe("the Netherlands");
    expect(withArticle("Czech Republic")).toBe("the Czech Republic");
    expect(withArticle("Germany")).toBe("Germany");
    expect(withArticle("Saudi Arabia")).toBe("Saudi Arabia");
  });
});
