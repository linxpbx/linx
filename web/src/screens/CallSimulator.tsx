// Call simulator (docs/ui/ADMIN_SCREENS_PHASE1E.md §9): where a call would
// go, without making it. POST /route-test asks the same database function
// real calls use.
import { useEffect, useState, type FormEvent } from "react";
import { Check, X } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { navigate } from "@/hooks/useRoute";
import { cn } from "@/lib/utils";

type RouteTest = components["schemas"]["RouteTest"];
type Extension = components["schemas"]["Extension"];
type Did = components["schemas"]["Did"];
type Direction = "out" | "in";
type Recent = { direction: Direction; number: string; from?: string; ok: boolean };

const RECENT_KEY = "linx.simulator.recent";
function readRecent(): Recent[] {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]") as unknown;
    return Array.isArray(v) ? (v as Recent[]).slice(0, 5) : [];
  } catch {
    return [];
  }
}
function writeRecent(list: Recent[]) {
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(list.slice(0, 5)));
  } catch {
    // This browser only, best effort.
  }
}

export function CallSimulatorScreen({ me }: { me: Me }) {
  const params = new URLSearchParams(window.location.search);
  const [direction, setDirection] = useState<Direction>(params.get("direction") === "in" ? "in" : "out");
  const [number, setNumber] = useState(params.get("number") ?? "");
  const [from, setFrom] = useState("");
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [dids, setDids] = useState<Did[]>([]);
  const [result, setResult] = useState<RouteTest | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [recent, setRecent] = useState<Recent[]>(readRecent);

  useEffect(() => {
    void Promise.all([
      api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/inbound-routes", { params: { query: { limit: 200 } } }),
    ]).then(([e, d]) => {
      if (e.data) {
        setExtensions(e.data.items);
        setFrom((f) => f || (e.data.items.find((x) => x.number === me.extension) ?? e.data.items[0])?.number || "");
      }
      if (d.data) setDids(d.data.items);
    });
  }, [me.extension]);

  const check = async (e?: FormEvent, again?: Recent) => {
    e?.preventDefault();
    const dir = again?.direction ?? direction;
    const num = (again?.number ?? number).trim();
    const fromExt = again?.from ?? from;
    if (!num) return;
    if (again) { setDirection(dir); setNumber(num); if (fromExt) setFrom(fromExt); }
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/route-test", {
      body: dir === "in" ? { direction: "inbound", number: num } : { direction: "outbound", number: num, ...(fromExt ? { from: fromExt } : {}) },
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); setResult(null); return; }
    setResult(data);
    const ok = dir === "in" ? data.reason === "routed" : !!data.allowed;
    const next = [{ direction: dir, number: num, from: dir === "out" ? fromExt : undefined, ok },
      ...recent.filter((r) => !(r.direction === dir && r.number === num))].slice(0, 5);
    setRecent(next);
    writeRecent(next);
  };

  const ok = result ? (direction === "in" ? result.reason === "routed" : !!result.allowed) : false;
  return (
    <div className="w-full max-w-3xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">Call simulator</h1>
      <p className="mt-1 text-sm text-muted-foreground">See where a call would go, without making it.</p>

      <form onSubmit={(e) => void check(e)} className="mt-6 flex flex-col gap-4" noValidate>
        <RadioGroup value={direction} onValueChange={(v) => { setDirection(v as Direction); setResult(null); setNumber(""); }}
          className="flex flex-wrap gap-4" aria-label="Which way">
          <label htmlFor="sim-out" className="flex cursor-pointer items-center gap-2 text-sm">
            <RadioGroupItem id="sim-out" value="out" /> Someone here calls out
          </label>
          <label htmlFor="sim-in" className="flex cursor-pointer items-center gap-2 text-sm">
            <RadioGroupItem id="sim-in" value="in" /> Someone calls in
          </label>
        </RadioGroup>

        {direction === "out" ? (
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="sim-from">From extension</Label>
              {/* Mounted once its choices are there: inside a form, Radix's
                  Select clears a value it was given before its items. */}
              {extensions.length === 0 ? <Input id="sim-from" className="w-56" disabled placeholder="Loading…" /> : (
                <Select value={from} onValueChange={(v) => { if (v) setFrom(v); }}>
                  <SelectTrigger id="sim-from" className="w-56"><SelectValue placeholder="Choose" /></SelectTrigger>
                  <SelectContent>{extensions.map((x) => <SelectItem key={x.id} value={x.number}>{x.number} {x.display_name}</SelectItem>)}</SelectContent>
                </Select>
              )}
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="sim-number">Number</Label>
              <Input id="sim-number" className="w-56 font-mono" inputMode="tel" value={number} onChange={(e) => setNumber(e.target.value)} placeholder="050 123 4567" />
            </div>
            <Button type="submit" disabled={busy || !number.trim()} aria-busy={busy}>Check</Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="sim-did">Your number</Label>
              {dids.length > 0 ? (
                <Select value={number} onValueChange={(v) => { if (v) setNumber(v); }}>
                  <SelectTrigger id="sim-did" className="w-56"><SelectValue placeholder="Choose" /></SelectTrigger>
                  <SelectContent>{dids.map((d) => <SelectItem key={d.id} value={d.number}>{d.number}</SelectItem>)}</SelectContent>
                </Select>
              ) : (
                <Input id="sim-did" className="w-56 font-mono" value={number} onChange={(e) => setNumber(e.target.value)} />
              )}
            </div>
            <Button type="submit" disabled={busy || !number.trim()} aria-busy={busy}>Check</Button>
          </div>
        )}
      </form>

      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}

      {result && (
        <div className={cn("mt-6 flex flex-col gap-2 rounded-lg border p-4", ok ? "border-status-available/50" : "border-status-busy/50")} aria-live="polite">
          <p className="flex items-center gap-2 font-medium">
            {ok ? <Check aria-hidden="true" className="size-5 text-status-available" /> : <X aria-hidden="true" className="size-5 text-status-busy" />}
            {direction === "in"
              ? (ok ? `Rings ${result.extension ? `extension ${result.extension.number} (${result.extension.display_name})` : "an extension"}` : "Rings nobody")
              : result.category === "emergency" ? "Always allowed: emergency"
              : ok ? "Allowed" : result.reason === "no_lines" ? "Would fail: no phone line can take it" : "Not allowed"}
          </p>
          <p className="whitespace-pre-wrap text-sm">{result.words}</p>
          {direction === "out" && (result.lines?.length ?? 0) > 0 && (
            <ul className="text-sm text-muted-foreground">
              {result.lines!.map((l, i) => (
                <li key={i}>{i === 0 ? "Goes out on" : "If that one is down or full:"} "{l.trunk}" as <span className="font-mono">{l.number}</span>
                  {l.caller_id ? <>, showing <span className="font-mono">{l.caller_id}</span></> : null}.</li>
              ))}
            </ul>
          )}
          {direction === "out" && result.reason === "not_permitted" && (
            <Button size="sm" variant="outline" className="self-start" onClick={() => navigate("/admin/outgoing")}>Change what phones can call</Button>
          )}
          {direction === "out" && result.reason === "no_lines" && (
            <Button size="sm" variant="outline" className="self-start" onClick={() => navigate("/admin/lines")}>Connect a line</Button>
          )}
          {direction === "in" && !ok && (
            <Button size="sm" variant="outline" className="self-start" onClick={() => navigate("/admin/incoming")}>Choose where it rings</Button>
          )}
          <p className="text-xs text-muted-foreground">This is exactly what a real call does.</p>
          {direction === "in" && <p className="text-xs text-muted-foreground">Office hours and groups will show here when they arrive.</p>}
        </div>
      )}

      {recent.length > 0 && (
        <p className="mt-6 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
          <span>Recent checks:</span>
          {recent.map((r, i) => (
            <button key={i} type="button" className="flex items-center gap-1 font-mono text-link underline-offset-4 hover:underline"
              onClick={() => void check(undefined, r)}>
              {r.direction === "in" ? "→ " : ""}{r.number}
              {r.ok ? <Check aria-label="allowed" className="size-3.5" /> : <X aria-label="refused" className="size-3.5" />}
            </button>
          ))}
        </p>
      )}
    </div>
  );
}
