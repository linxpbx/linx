// Another public port (advanced, ADR-064, docs/ui/SCREENS_PHASE1F.md §4):
// the owner's warning first, then the two ports, the router rules and what
// it trades away. The same on the install page and in Server settings.

import { useState } from "react";
import { TriangleAlert } from "lucide-react";

import { CopyButton, FieldMessage } from "@/components/InstallFrame";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { PUBLIC_PORT_TRADEOFFS, PUBLIC_PORT_WARNING } from "@/lib/install";

/** The owner's warning, with a way back to the front doors that keep 443. */
export function PublicPortWarning({ onDoors, onUse }: { onDoors: () => void; onUse?: () => void }) {
  return (
    <div role="note" className="flex flex-col gap-3 rounded-md border border-status-away p-4 text-sm">
      <p className="flex items-center gap-2 font-medium">
        <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-status-away" />Port 443 is the recommended choice
      </p>
      <p>{PUBLIC_PORT_WARNING}</p>
      <p>
        Before you choose this: a proxy on 443 (Pangolin, nginx, Caddy, Nginx Proxy Manager passing Linx through) or a small
        rented server as your front door keeps 443.
      </p>
      <div className="flex flex-wrap justify-end gap-3">
        <Button type="button" variant="outline" onClick={onDoors}>Show me the front-door options</Button>
        {onUse && <Button type="button" onClick={onUse}>Use another port</Button>}
      </div>
    </div>
  );
}

export function PublicPortFields({ id, port, onPort, udp, onUdp, domain, lanAddress, errors }: {
  /** Prefix for the fields' ids (two pages, one component). */
  id: string;
  port: string;
  onPort: (v: string) => void;
  udp: number;
  onUdp: (v: number) => void;
  domain: string;
  /** This server's home address; none on a rented server. */
  lanAddress?: string;
  errors: { public_port?: string; turn_udp_port?: string };
}) {
  const [udpChoice, setUdpChoice] = useState(udp === 443 || udp === 3478 ? String(udp) : "other");
  const [other, setOther] = useState(udp === 443 || udp === 3478 ? "" : String(udp));
  const tcp = Number(port) || 0;
  const shownTcp = tcp > 0 ? String(tcp) : "8443";
  const address = `https://${domain || "pbx.example.com"}:${shownTcp}`;
  const rules: [string, string][] = lanAddress
    ? [[`TCP ${shownTcp}`, `${lanAddress} port ${shownTcp}`], [`UDP ${udp || 443}`, `${lanAddress} port ${udp || 443}`]]
    : [];
  const pickUdp = (v: string) => {
    setUdpChoice(v);
    onUdp(v === "other" ? Number(other) || 0 : Number(v));
  };
  return (
    <div className="flex min-w-0 flex-col gap-4 text-sm">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-public-port`}>Public web port</Label>
        <div className="flex items-center gap-2">
          <Input id={`${id}-public-port`} inputMode="numeric" className="w-28" value={port} placeholder="8443"
            onChange={(e) => onPort(e.target.value.replace(/\D/g, ""))} aria-invalid={errors.public_port ? true : undefined}
            aria-describedby={`${id}-public-port-hint`} />
          <span id={`${id}-public-port-hint`} className="text-muted-foreground">1024–65535</span>
        </div>
        <FieldMessage message={errors.public_port ?? ""} />
      </div>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1.5 font-medium">Public port for call audio (UDP)</legend>
        <RadioGroup value={udpChoice} onValueChange={pickUdp} className="gap-2" aria-label="Public port for call audio (UDP)">
          <label htmlFor={`${id}-udp-443`} className="flex items-start gap-2">
            <RadioGroupItem id={`${id}-udp-443`} value="443" className="mt-0.5" />
            <span>443 <span className="text-muted-foreground">(Recommended: often free even when TCP 443 isn't)</span></span>
          </label>
          <label htmlFor={`${id}-udp-3478`} className="flex items-start gap-2">
            <RadioGroupItem id={`${id}-udp-3478`} value="3478" className="mt-0.5" />
            <span>3478 <span className="text-muted-foreground">(if your router can't forward UDP 443)</span></span>
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <label htmlFor={`${id}-udp-other`} className="flex items-center gap-2">
              <RadioGroupItem id={`${id}-udp-other`} value="other" />
              <span>Another:</span>
            </label>
            <Input inputMode="numeric" className="w-28" value={other} aria-label="Another UDP port for call audio"
              aria-invalid={errors.turn_udp_port ? true : undefined}
              onFocus={() => udpChoice !== "other" && pickUdp("other")}
              onChange={(e) => { const v = e.target.value.replace(/\D/g, ""); setOther(v); setUdpChoice("other"); onUdp(Number(v) || 0); }} />
          </div>
        </RadioGroup>
        <FieldMessage message={errors.turn_udp_port ?? ""} />
      </fieldset>
      {lanAddress ? (
        <div className="flex flex-col gap-1">
          <p className="font-medium">On your router, forward:</p>
          <ul className="flex flex-col gap-1">
            {rules.map(([from, to]) => (
              <li key={from} className="flex min-w-0 flex-wrap items-center gap-x-2">
                <code className="font-mono">{from}</code>
                <span aria-hidden="true">→</span>
                <span className="sr-only">goes to</span>
                <span className="flex min-w-0 items-center gap-1">
                  <code className="min-w-0 font-mono break-all">{to}</code>
                  <CopyButton text={`${from} → ${to}`} label={`router rule for ${from}`} />
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <p>Linx will open TCP {shownTcp} and UDP {udp || 443} on this server's firewall: there's nothing to forward.</p>
      )}
      <p>
        {domain ? "People will open " : `People will open your domain with :${shownTcp} at the end, like `}
        <code className="font-mono break-all">{address}</code>
      </p>
      <ul className="flex flex-col gap-2">
        {PUBLIC_PORT_TRADEOFFS.map((t) => (
          <li key={t} className="flex items-start gap-2">
            <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
            <span className="min-w-0">{t}</span>
          </li>
        ))}
      </ul>
      {lanAddress && (
        <p className="text-muted-foreground">
          People at the office use the same address. If it doesn't open there, turn on NAT loopback (hairpin) on your router, or
          add {domain || "your domain"} to your local DNS pointing at {lanAddress}.
        </p>
      )}
      <p className="text-muted-foreground">
        Without port 443, Let's Encrypt can only check your domain through your DNS company, so Linx needs its key.
      </p>
    </div>
  );
}
