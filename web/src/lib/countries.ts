// Plain words about countries, shared by the setup wizard and Outgoing calls.
import type { Country } from "@/hooks/useCountries";

/** "United Arab Emirates" for "AE", from the list or the browser's own names. */
export function countryName(code: string, list?: Country[] | null): string {
  const found = list?.find((c) => c.code === code);
  if (found) return found.name;
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

/** "999, 998, 997, 112 and 901". */
export function emergencyWords(country: Country | undefined): string {
  const n = country?.emergency_numbers.map((e) => e.number) ?? [];
  if (n.length === 0) return "the emergency numbers";
  if (n.length === 1) return n[0]!;
  return `${n.slice(0, -1).join(", ")} and ${n[n.length - 1]}`;
}

const WITH_THE = new Set(["Netherlands", "Philippines", "Bahamas", "Gambia", "Maldives", "Seychelles", "Comoros", "Vatican City"]);

/** "the United Arab Emirates", "Germany": for "in …" and "for …" in sentences. */
export function withArticle(name: string): string {
  return /^United |Republic$|Islands$/.test(name) || WITH_THE.has(name) ? `the ${name}` : name;
}
