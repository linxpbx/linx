// The DNS companies Linx can keep records right at (docs/SIMPLER.md §3,
// ADR-063): the list is the server's own (internal/dnsapi), written to
// dns-companies.json by its tests, so the forms ask for exactly what the
// server checks.
import list from "./dns-companies.json";

export type DnsField = { key: string; label: string; secret?: boolean; options?: { value: string; label: string }[] };
export type DnsCompany = { id: string; name: string; fields: DnsField[]; where: string; note?: string; one_address?: boolean };

export const DNS_COMPANIES: DnsCompany[] = list;

/** A company's form as filled in: its id and each field's value by key. */
export type DnsKeyValue = { provider: string; key: Record<string, string> };

/** The API's DnsKey: one field goes as `token`, more as `key`. */
export function dnsKeyBody(v: DnsKeyValue): { provider: string; token?: string; key?: Record<string, string> } {
  const c = companyById(v.provider);
  if (!c) return { provider: v.provider };
  const key = Object.fromEntries(c.fields.map((f) => [f.key, (v.key[f.key] ?? "").trim()]));
  return c.fields.length === 1 ? { provider: c.id, token: key[c.fields[0]!.key] } : { provider: c.id, key };
}

/** Every field has something in it. */
export function dnsKeyFilled(v: DnsKeyValue) {
  const c = companyById(v.provider);
  return !!c && c.fields.every((f) => (v.key[f.key] ?? "").trim() !== "");
}

export function companyById(id: string | undefined) {
  return DNS_COMPANIES.find((c) => c.id === id);
}

/** The company dnscheck named from the name servers ("Porkbun"), if one of ours. */
export function companyByName(name: string | undefined) {
  return DNS_COMPANIES.find((c) => c.name === name);
}

/** Where-to-find-it words with the domain and this network's address put in. */
export function fillWhere(text: string, domain: string, address?: string) {
  return text.replaceAll("{domain}", domain).replaceAll("{address}", address || "this network's public address");
}

/** The company a form starts on: the one named, then the one the name servers show, then Cloudflare (DuckDNS for its names). */
export function startingCompany(domain: string, current?: string, detected?: string) {
  if (domain.endsWith(".duckdns.org")) return "duckdns";
  return companyById(current)?.id ?? companyByName(detected)?.id ?? "cloudflare";
}
