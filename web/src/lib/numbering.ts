// The setup wizard's numbering picture and recommended ranges
// (docs/ADMIN.md §4): people 100–599, groups 600–699, reserved 700–899,
// avoided 900–999 for 3 digits. Other digit counts scale the same way:
// with base = 10^(digits-1), people gets 5x base, groups 1x, reserved 2x,
// avoided (the country's own short codes, not a stored range) 1x.
//
// A country whose national prefix isn't 0 can't have extensions that start
// with it (ADR-085): 1 in North America, 8 in Russia. Those hundreds are
// avoided too and the ranges move past them, so people keep five hundreds
// where possible: 200–699 in the United States.
export type Range = { from: number; to: number } | null;

/** The first digit extensions can't start with in a country, or null. */
export function blockedDigit(nationalPrefix: string | undefined): number | null {
  const d = Number(nationalPrefix?.[0]);
  return nationalPrefix && d >= 1 && d <= 9 ? d : null;
}

export function defaultRanges(digits: number, nationalPrefix?: string): { people: Range; groups: Range; reserved: Range } {
  const base = 10 ** (digits - 1);
  const blocked = blockedDigit(nationalPrefix);
  // The leading digits ranges may use, in order: 1 to 8, less the blocked one.
  const free = [1, 2, 3, 4, 5, 6, 7, 8].filter((d) => d !== blocked);
  const span = (from: number, to: number): Range => ({ from: free[from]! * base, to: (free[to]! + 1) * base - 1 });
  return {
    people: span(0, 4),
    groups: span(5, 5),
    // The rest, if they run on: with 8 blocked, reserved is only the 7s.
    reserved: free.length === 8 ? span(6, 7) : span(6, Math.min(6, free.length - 1)),
  };
}

/** The ranges extensions avoid: the 9s (short codes), and a blocked first digit. */
export function avoidedRanges(digits: number, nationalPrefix?: string): { from: number; to: number }[] {
  const base = 10 ** (digits - 1);
  const out = [{ from: 9 * base, to: 10 * base - 1 }];
  const blocked = blockedDigit(nationalPrefix);
  if (blocked !== null && blocked !== 9) out.unshift({ from: blocked * base, to: (blocked + 1) * base - 1 });
  return out;
}

/** Pads n to exactly `digits` digits (for showing/incrementing extension numbers). */
export function pad(n: number, digits: number): string {
  return String(n).padStart(digits, "0");
}
