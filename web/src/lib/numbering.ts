// The setup wizard's numbering picture and recommended ranges
// (docs/ADMIN.md §4): people 100–599, groups 600–699, reserved 700–899,
// avoided 900–999 for 3 digits. Other digit counts scale the same way:
// with base = 10^(digits-1), people gets 5x base, groups 1x, reserved 2x,
// avoided (the country's own short codes, not a stored range) 1x.
export type Range = { from: number; to: number } | null;

export function defaultRanges(digits: number): { people: Range; groups: Range; reserved: Range } {
  const base = 10 ** (digits - 1);
  return {
    people: { from: base, to: 6 * base - 1 },
    groups: { from: 6 * base, to: 7 * base - 1 },
    reserved: { from: 7 * base, to: 9 * base - 1 },
  };
}

export function avoidedRange(digits: number): Range {
  const base = 10 ** (digits - 1);
  return { from: 9 * base, to: 10 * base - 1 };
}

/** Pads n to exactly `digits` digits (for showing/incrementing extension numbers). */
export function pad(n: number, digits: number): string {
  return String(n).padStart(digits, "0");
}
