// The DNS company and its key (docs/ui/SCREENS_PHASE1F.md §3.2): the
// company starts on the one the domain's name servers show, and only the
// fields that company needs are asked. Used by Server settings' DNS row and
// both install pages' token steps.
import { useState } from "react";
import { Info } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { companyById, companyByName, DNS_COMPANIES, fillWhere, type DnsKeyValue } from "@/lib/dnsCompanies";

/** The "Not in the list" choice: records by hand. */
export const BY_HAND = "by-hand";

export function DnsKeyFields({ domain, address, detected, value, onChange, disabled = false, idPrefix = "dns" }: {
  domain: string;
  /** This network's public address, for Namecheap's allow list. */
  address?: string;
  /** The company the name servers show ("Porkbun"), if Linx knows it. */
  detected?: string;
  value: DnsKeyValue;
  onChange: (v: DnsKeyValue) => void;
  disabled?: boolean;
  idPrefix?: string;
}) {
  const [shown, setShown] = useState<Record<string, boolean>>({});
  const duck = domain.endsWith(".duckdns.org");
  const choices = DNS_COMPANIES.filter((c) => (c.id === "duckdns") === duck);
  const c = companyById(value.provider);
  const fromNS = !!c && companyByName(detected)?.id === c.id;
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${idPrefix}-company`}>DNS company</Label>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <Select value={value.provider} disabled={disabled || duck}
            onValueChange={(p) => { setShown({}); onChange({ provider: p, key: {} }); }}>
            <SelectTrigger id={`${idPrefix}-company`} className="w-full sm:w-64"><SelectValue /></SelectTrigger>
            <SelectContent>
              {choices.map((o) => <SelectItem key={o.id} value={o.id}>{o.name}</SelectItem>)}
              {!duck && <SelectItem value={BY_HAND}>Not in the list</SelectItem>}
            </SelectContent>
          </Select>
          {fromNS && <span className="text-sm text-muted-foreground">(from its name servers)</span>}
        </div>
      </div>

      {value.provider === BY_HAND && (
        <p className="text-sm">
          Linx can't change records there by itself. Add them by hand: the records list shows each one, and checks it for you.
        </p>
      )}

      {c && c.fields.map((f) => {
        const id = `${idPrefix}-${f.key}`;
        const v = value.key[f.key] ?? "";
        const set = (x: string) => onChange({ provider: value.provider, key: { ...value.key, [f.key]: x } });
        return (
          <div key={f.key} className="flex flex-col gap-1.5">
            <Label htmlFor={id}>{f.label}</Label>
            {f.options ? (
              <Select value={v} disabled={disabled} onValueChange={set}>
                <SelectTrigger id={id} className="w-full sm:w-64"><SelectValue placeholder="Choose…" /></SelectTrigger>
                <SelectContent>
                  {f.options.map((o) => <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>)}
                </SelectContent>
              </Select>
            ) : (
              <div className="flex gap-2">
                <Input id={id} type={f.secret && !shown[f.key] ? "password" : "text"} autoComplete="off" spellCheck={false}
                  disabled={disabled} value={v} onChange={(e) => set(e.target.value)} className="min-w-0 flex-1" />
                {f.secret && (
                  <Button type="button" variant="outline" disabled={disabled} aria-label={`${shown[f.key] ? "Hide" : "Show"} the ${f.label.toLowerCase()}`}
                    onClick={() => setShown({ ...shown, [f.key]: !shown[f.key] })}>
                    {shown[f.key] ? "Hide" : "Show"}
                  </Button>
                )}
              </div>
            )}
          </div>
        );
      })}

      {c && (
        <div className="flex flex-col gap-2 text-sm">
          <p className="break-words text-muted-foreground">
            <span className="font-medium text-foreground">Where to find {c.fields.length > 1 ? "them" : "it"}: </span>
            {fillWhere(c.where, domain, address)}
          </p>
          {c.note && (
            <p className="flex items-start gap-2 break-words">
              <Info aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0">{fillWhere(c.note, domain, address)}</span>
            </p>
          )}
        </div>
      )}
    </div>
  );
}
