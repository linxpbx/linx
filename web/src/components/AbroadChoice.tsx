// Where calls abroad may go (ADR-085): everywhere, or only the countries
// chosen. Shown under the "Abroad" switch in the setup wizard and Outgoing
// calls when calls abroad are on. An empty list means everywhere (the API's
// abroad_countries), so "Only these countries" saves nothing until one is
// picked.
import { useState } from "react";
import { X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import type { Country } from "@/hooks/useCountries";
import { countryName } from "@/lib/countries";

export function AbroadChoice({ countries, value, onChange, disabled, home }: {
  countries: Country[] | null;
  value: string[];
  onChange: (codes: string[]) => void;
  disabled?: boolean;
  /** The home country, which isn't abroad and isn't offered. */
  home: string;
}) {
  const [only, setOnly] = useState(value.length > 0);
  const choices = (countries ?? []).filter((c) => c.code !== home);
  return (
    <fieldset className="flex flex-col gap-3 rounded-md border p-3" disabled={disabled}>
      <legend className="px-1 text-sm font-medium">Calls abroad can go to</legend>
      <RadioGroup value={only ? "only" : "everywhere"}
        onValueChange={(v) => {
          const o = v === "only";
          setOnly(o);
          if (!o && value.length > 0) onChange([]);
        }}>
        <label htmlFor="abroad-everywhere" className="flex cursor-pointer items-start gap-3">
          <RadioGroupItem id="abroad-everywhere" value="everywhere" className="mt-0.5" />
          <span className="text-sm">Every country</span>
        </label>
        <label htmlFor="abroad-only" className="flex cursor-pointer items-start gap-3">
          <RadioGroupItem id="abroad-only" value="only" className="mt-0.5" />
          <span className="text-sm">
            Only the countries I choose
            <span className="block text-muted-foreground">The safest way to allow calls abroad: anywhere else is refused.</span>
          </span>
        </label>
      </RadioGroup>
      {only && (
        <>
          <CountryChooser countries={choices} value={value} onChange={onChange} />
          {value.length === 0 && (
            <p className="text-sm text-muted-foreground">Add at least one country. Until you do, calls abroad can still go anywhere.</p>
          )}
        </>
      )}
    </fieldset>
  );
}

/** Chosen countries as chips, and a box to find and add another. */
export function CountryChooser({ countries, value, onChange }: {
  countries: Country[];
  value: string[];
  onChange: (codes: string[]) => void;
}) {
  const [q, setQ] = useState("");
  const query = q.trim().toLowerCase();
  const matches = query === "" ? [] : countries
    .filter((c) => !value.includes(c.code) && (c.name.toLowerCase().includes(query) || c.code.toLowerCase() === query))
    .sort((a, b) => Number(!a.name.toLowerCase().startsWith(query)) - Number(!b.name.toLowerCase().startsWith(query)))
    .slice(0, 6);
  const add = (code: string) => { onChange([...value, code]); setQ(""); };
  return (
    <div className="flex flex-col gap-2">
      {value.length > 0 && (
        <ul aria-label="Countries calls abroad can go to" className="flex flex-wrap gap-2">
          {value.map((code) => {
            const name = countryName(code, countries);
            return (
              <li key={code} className="flex items-center gap-1 rounded-full border bg-card py-0.5 ps-3 pe-1 text-sm">
                {name}
                <button type="button" aria-label={`Remove ${name}`} onClick={() => onChange(value.filter((c) => c !== code))}
                  className="rounded-full p-1 text-muted-foreground hover:bg-muted hover:text-foreground">
                  <X aria-hidden="true" className="size-3.5" />
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <Label htmlFor="abroad-add" className="sr-only">Add a country</Label>
      <Input id="abroad-add" placeholder="Add a country" value={q} autoComplete="off"
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter" && matches[0]) { e.preventDefault(); add(matches[0].code); } }} />
      {matches.length > 0 && (
        <ul aria-label="Matching countries" className="flex flex-col overflow-hidden rounded-md border">
          {matches.map((c) => (
            <li key={c.code}>
              <button type="button" onClick={() => add(c.code)} className="w-full px-3 py-2 text-start text-sm hover:bg-muted">
                {c.name}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
