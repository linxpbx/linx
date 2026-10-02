// The DNS records by hand (docs/ui/SCREENS_PHASE1F.md §3.1, docs/SIMPLER.md
// §3.1): what to add, at which DNS company (told by the domain's name
// servers, never asked), and what each name shows right now at those name
// servers, so no cache is in the way. Checked when shown and on Check
// again, never on a timer. When Linx keeps them right itself (§3.2), it
// says at which company and when the address last changed.
import { useCallback, useEffect, useRef, useState } from "react";
import { Check, CircleDashed, LoaderCircle, TriangleAlert, X } from "lucide-react";
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { CopyButton } from "@/components/InstallFrame";
import { Button } from "@/components/ui/button";
import { companyByName } from "@/lib/dnsCompanies";
import { cn } from "@/lib/utils";

type Records = components["schemas"]["DnsRecords"];
type Rec = Records["records"][number];

const USES: Record<Rec["use"], string> = {
  web: "The web app and sign-in",
  turn: "Call audio through firewalls",
  sip: "Desk phones and phone systems on your network",
};

/** name's part before zone, as a DNS company's name field wants it ("@" for the zone itself). */
export function relativeName(name: string, zone: string) {
  if (!zone || name === zone) return zone ? "@" : "";
  return name.endsWith("." + zone) ? name.slice(0, -zone.length - 1) : "";
}

export function DnsRecords({ className, onLoaded, onAutomatic }: {
  className?: string;
  /** Each answer, for the page around it (the company the name servers show). */
  onLoaded?: (r: Records) => void;
  /** Shows "Set it up automatically" (Server settings only: the key is given there). */
  onAutomatic?: () => void;
}) {
  const [r, setR] = useState<Records | null>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const run = useRef(0);
  // The latest onLoaded, without asking again whenever the page draws.
  const loaded = useRef(onLoaded);
  useEffect(() => { loaded.current = onLoaded; });

  const load = useCallback(async () => {
    const mine = ++run.current;
    setBusy(true);
    setError("");
    const { data, error: err } = await api.GET("/api/v1/system/dns-records");
    if (run.current !== mine) return;
    setBusy(false);
    if (data) {
      setR(data);
      loaded.current?.(data);
    } else setError(problemMessage(err));
  }, []);

  useEffect(() => {
    void load();
    return () => { run.current++; };
  }, [load]);

  if (!r) {
    return (
      <div className={cn("text-sm", className)}>
        {busy && (
          <p role="status" className="flex items-center gap-2 text-muted-foreground">
            <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Asking your DNS company's name servers…
          </p>
        )}
        {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
      </div>
    );
  }

  const where = r.zone || r.domain;
  const allKept = r.records.length > 0 && r.records.every((x) => x.kept);
  const auto = r.automatic;
  const ours = companyByName(r.company);
  return (
    <section aria-labelledby="dns-records" className={cn("flex min-w-0 flex-col gap-3 rounded-md border p-4 text-sm", className)}>
      <h3 id="dns-records" className="font-display text-base font-semibold break-words">Records for {r.domain}</h3>
      <div className="flex flex-col gap-1">
        <p>
          {auto
            ? `Linx updates these at ${auto.company} for you, even when your address changes.`
            : allKept ? "Linx updates these for you, with your DNS key, even when your address changes." : `Add these at the company that runs ${where}'s DNS.`}
        </p>
        {auto && (auto.changed_at || auto.error) && (
          <p className="break-words text-muted-foreground">
            {auto.changed_at && <>Last change {ago(auto.changed_at)}{auto.previous && ` (${auto.previous} → ${auto.address})`}.</>}
            {auto.error && <span className="ms-1 font-medium text-destructive">The last try failed: {auto.error}</span>}
          </p>
        )}
        {r.name_servers.length > 0 ? (
          <p className="break-words text-muted-foreground">
            Its name servers: {r.name_servers.join(", ")}{r.company && <> (so: <span className="font-medium text-foreground">{r.company}</span>)</>}
          </p>
        ) : (
          <p className="break-words text-muted-foreground">
            Linx couldn't find {where}'s name servers. A domain bought in the last few hours may not show yet.
          </p>
        )}
      </div>

      <div role="table" aria-label={`Records for ${r.domain}`} className="flex min-w-0 flex-col">
        <div role="row" className="hidden gap-3 border-b pb-1 text-xs font-medium text-muted-foreground sm:grid sm:grid-cols-[minmax(0,1.3fr)_2.5rem_minmax(0,1fr)_minmax(0,1fr)]">
          <span role="columnheader">Name</span><span role="columnheader">Type</span>
          <span role="columnheader">Value</span><span role="columnheader">Now</span>
        </div>
        {r.records.map((x) => <RecordRow key={x.name} rec={x} zone={r.zone} />)}
      </div>

      <div className="flex flex-col gap-1 text-muted-foreground">
        {r.zone && <p>In the name field, some companies want only the part in brackets, and @ for {r.zone} itself (also called the root or apex).</p>}
        {(r.company === "Cloudflare" || !r.company) && <p>At Cloudflare, keep Proxy off (grey cloud, “DNS only”).</p>}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground" aria-live="polite">
          {busy ? "Checking…" : `Checked ${new Date(r.checked_at).toLocaleTimeString()}. Changes can take a few minutes to show.`}
        </p>
        <Button variant="outline" disabled={busy} onClick={() => void load()}>
          {busy && <LoaderCircle aria-hidden="true" className="animate-spin" />}
          Check again
        </Button>
      </div>
      {onAutomatic && !auto && (
        <div className="flex flex-col gap-2 border-t pt-3">
          <p>Your address changes from time to time? Let Linx update these for you:</p>
          <Button variant="outline" className="w-fit" onClick={onAutomatic}>
            {ours ? `Let Linx update these at ${ours.name} for you` : "Set it up automatically"}
          </Button>
        </div>
      )}
      {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
    </section>
  );
}

/** "3 days ago", "5 minutes ago", in the browser's language. */
function ago(iso: string) {
  const s = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
  const f = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [unit, n] of [["day", 86400], ["hour", 3600], ["minute", 60]] as const) {
    if (Math.abs(s) >= n) return f.format(Math.round(s / n), unit);
  }
  return f.format(s, "second");
}

function RecordRow({ rec, zone }: { rec: Rec; zone: string }) {
  const rel = relativeName(rec.name, zone);
  return (
    <div role="row" className="flex min-w-0 flex-col gap-1 border-b py-2 last:border-b-0 sm:grid sm:grid-cols-[minmax(0,1.3fr)_2.5rem_minmax(0,1fr)_minmax(0,1fr)] sm:items-center sm:gap-3">
      <span role="cell" className="flex min-w-0 flex-col">
        <span className="font-mono break-all">
          {rec.name}{rel && <span className="text-muted-foreground"> ({rel})</span>}
        </span>
        <span className="text-xs text-muted-foreground">{USES[rec.use]}</span>
      </span>
      <span role="cell" className="font-mono"><span className="text-muted-foreground sm:hidden">Type </span>{rec.type}</span>
      <span role="cell" className="flex min-w-0 items-center gap-1">
        <span className="text-muted-foreground sm:hidden">Value </span>
        {rec.value ? (
          <>
            <span className="min-w-0 font-mono break-all">{rec.value}</span>
            <CopyButton text={rec.value} label={`value for ${rec.name}`} />
          </>
        ) : (
          <span className="text-muted-foreground">this network's public address</span>
        )}
      </span>
      <span role="cell" className="flex min-w-0 flex-col">
        <State rec={rec} />
        {rec.kept && <span className="text-xs text-muted-foreground">Linx keeps this one right</span>}
      </span>
    </div>
  );
}

function State({ rec }: { rec: Rec }) {
  const c = "mt-0.5 size-4 shrink-0";
  const shows = rec.seen.join(", ");
  const [icon, words] = {
    ok: [<Check key="i" aria-hidden="true" className={cn(c, "text-status-available")} />, "Right"],
    wrong: [<X key="i" aria-hidden="true" className={cn(c, "text-destructive")} />, `Shows ${shows}`],
    missing: [<CircleDashed key="i" aria-hidden="true" className={cn(c, "text-muted-foreground")} />, "Not found yet"],
    error: [<TriangleAlert key="i" aria-hidden="true" className={cn(c, "text-status-away")} />, "Couldn't ask just now"],
    unknown: [<CircleDashed key="i" aria-hidden="true" className={cn(c, "text-muted-foreground")} />, `Shows ${shows}`],
  }[rec.state];
  return <span className="flex min-w-0 items-start gap-1.5"><span className="sr-only sm:hidden">Now: </span>{icon}<span className="min-w-0 break-words">{words}</span></span>;
}
