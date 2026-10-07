// Outgoing calls (docs/ui/ADMIN_SCREENS_PHASE1E.md §8): the country, which
// line is tried first, what every phone can call (the one "Everyone"
// level, flat: owner decision 2026-09-27) and the calls-abroad alert.
import { useCallback, useEffect, useState } from "react";
import { ArrowDown, ArrowUp, Lock } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { navigate } from "@/hooks/useRoute";
import { AbroadChoice } from "@/components/AbroadChoice";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useCountries } from "@/hooks/useCountries";
import { categorySwitches, EXPERT_CATEGORY_SWITCHES, type NumberCategory } from "@/lib/categories";
import { countryName, emergencyWords, withArticle } from "@/lib/countries";
import { trunkDot, trunkWords } from "@/lib/lines";
import { hasScope, isReadOnlyAdmin } from "@/lib/roles";
import { useRoutingPutBack } from "@/lib/routingUndo";

type Trunk = components["schemas"]["Trunk"];
type Level = components["schemas"]["CallPermissionLevel"];
type Settings = components["schemas"]["Settings"];

function Section({ title, children, aside }: { title: string; children: React.ReactNode; aside?: React.ReactNode }) {
  return (
    <section className="rounded-lg border p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-lg font-semibold">{title}</h2>
        {aside}
      </div>
      <div className="mt-3">{children}</div>
    </section>
  );
}

export function OutgoingCallsScreen({ me, simpleMode }: { me: Me; simpleMode: boolean }) {
  const [country, setCountry] = useState("");
  const countries = useCountries();
  // Changing the country here (ADR-085): the picked one, while choosing.
  const [newCountry, setNewCountry] = useState<string | null>(null);
  const [order, setOrder] = useState<Trunk[] | null>(null);
  const [all, setAll] = useState<Trunk[]>([]);
  const [alertLimits, setAlertLimits] = useState({ minutes: "", calls: "" });
  const [settings, setSettings] = useState<Settings | null>(null);
  const [level, setLevel] = useState<Level | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const confirm = useConfirmIdentity(me);
  const canWrite = hasScope(me, "routing:write") && !isReadOnlyAdmin(me);

  const load = useCallback(async () => {
    const [r, t, s] = await Promise.all([
      api.GET("/api/v1/outbound-routing"),
      api.GET("/api/v1/trunks", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/settings"),
    ]);
    if (r.data) {
      setCountry(r.data.country);
      setOrder(r.data.trunks);
      setAlertLimits({ minutes: String(r.data.international_alert.minutes), calls: String(r.data.international_alert.calls) });
    }
    if (t.data) setAll(t.data.items);
    if (s.data) {
      setSettings(s.data);
      if (s.data.default_call_permission_level_id) {
        const { data } = await api.GET("/api/v1/call-permission-levels/{id}", { params: { path: { id: s.data.default_call_permission_level_id } } });
        if (data) setLevel(data);
      }
    }
  }, []);
  useEffect(() => { void load(); }, [load]);
  useRoutingPutBack(load);

  const say = (what: string) => { setSaved(what); window.setTimeout(() => setSaved(""), 2500); };

  const saveOrder = async (ids: string[]) => {
    // No line left means no emergency calls either: say so first (Phase 1E review).
    if (ids.length === 0 && !window.confirm("With no line for outgoing calls, nobody can call out, not even emergency numbers. Stop using it anyway?")) return;
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PUT("/api/v1/outbound-routing", { body: { order: ids } });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setOrder(data.trunks);
    say("Saved: the order");
  };
  const move = (i: number, by: number) => {
    if (!order) return;
    const ids = order.map((t) => t.id);
    const [id] = ids.splice(i, 1);
    ids.splice(i + by, 0, id!);
    void saveOrder(ids);
  };

  const setCategories = (keys: NumberCategory[], on: boolean) => confirm.run(async () => {
    if (!level) return { confirm: false };
    const next = new Set(level.allowed_categories);
    for (const k of keys) { if (on) next.add(k); else next.delete(k); }
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/call-permission-levels/{id}", {
      params: { path: { id: level.id }, header: { "If-Match": level.etag } }, body: { allowed_categories: [...next] },
    });
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setLevel(data);
    say("Saved");
    return { confirm: false };
  });
  const setWithhold = async (on: boolean) => {
    if (!level) return;
    const { data, error: err } = await api.PATCH("/api/v1/call-permission-levels/{id}", {
      params: { path: { id: level.id }, header: { "If-Match": level.etag } }, body: { withhold_caller_id: on },
    });
    if (!data) { setError(problemMessage(err)); return; }
    setLevel(data);
    say("Saved");
  };

  const saveCountry = async () => {
    if (!newCountry) return;
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/settings", { body: { country: newCountry } });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setCountry(data.country);
    setSettings(data);
    setNewCountry(null);
    say("Saved: the country");
  };
  // Where calls abroad may go. Reaching more countries asks "confirm it's
  // you", like allowing calls abroad at all.
  const setAbroad = (codes: string[]) => confirm.run(async () => {
    if (!level) return { confirm: false };
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/call-permission-levels/{id}", {
      params: { path: { id: level.id }, header: { "If-Match": level.etag } }, body: { abroad_countries: codes },
    });
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setLevel(data);
    say("Saved");
    return { confirm: false };
  });

  const saveAlert = async () => {
    if (!order) return;
    const minutes = Number(alertLimits.minutes);
    const calls = Number(alertLimits.calls);
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PUT("/api/v1/outbound-routing", {
      body: { order: order.map((t) => t.id), international_alert: { minutes, calls } },
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    say("Saved: the alert");
  };

  if (order === null) return <div className="p-6" aria-busy="true" />;
  const unused = all.filter((t) => !order.some((o) => o.id === t.id));
  const readOnlyReason = !canWrite ? "Changing these needs an admin with routing rights." : "";

  return (
    <div className="w-full max-w-3xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">Outgoing calls</h1>
      <p className="mt-2 min-h-5 text-sm text-status-available" role="status">{saved}</p>

      <div className="flex flex-col gap-6">
        <Section title="Country" aside={canWrite && newCountry === null && <Button size="sm" variant="outline" disabled={!countries} onClick={() => setNewCountry(country)}>Change</Button>}>
          {newCountry === null ? (
            <>
              <p className="text-sm">{countryName(country, countries)}</p>
              <p className="mt-1 text-sm text-muted-foreground">Numbers are read the way people dial them there: local numbers, mobiles and emergency numbers are recognised for {withArticle(countryName(country, countries))}.</p>
            </>
          ) : (
            <div className="flex flex-col gap-3">
              <Label htmlFor="country">Which country are your phone lines in?</Label>
              <Select value={newCountry} onValueChange={setNewCountry}>
                <SelectTrigger id="country" className="w-full sm:w-80"><SelectValue /></SelectTrigger>
                <SelectContent className="max-h-80">
                  {(countries ?? []).map((c) => <SelectItem key={c.code} value={c.code}>{c.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <p className="text-sm text-muted-foreground">
                Extension numbers that look like the new country's outside or emergency numbers are listed on Extensions to renumber.
              </p>
              <div className="flex gap-2">
                <Button size="sm" disabled={busy || newCountry === country} onClick={() => void saveCountry()}>Save</Button>
                <Button size="sm" variant="outline" disabled={busy} onClick={() => setNewCountry(null)}>Cancel</Button>
              </div>
            </div>
          )}
        </Section>

        <Section title="Which line first">
          <p className="text-sm text-muted-foreground">Linx tries the next line only if one is down or full, never after someone answers.</p>
          {order.length === 0 ? (
            <p className="mt-3 text-sm">
              No line is used for outgoing calls, so outside calls, emergency ones included, can't go out.{" "}
              {all.length === 0 && <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/lines")}>Connect a line</button>}
            </p>
          ) : (
            <ol className="mt-3 flex flex-col gap-2" aria-label="Lines for outgoing calls">
              {order.map((t, i) => (
                <li key={t.id} className="flex flex-wrap items-center gap-2 rounded-md border p-2.5 text-sm">
                  <span className="w-5 text-center font-medium">{i + 1}</span>
                  <span className="min-w-0 flex-1 font-medium">{t.name}</span>
                  <span className="flex items-center gap-1.5"><Dot tone={trunkDot[t.status] ?? "neutral"} />{trunkWords[t.status] ?? t.status}</span>
                  {canWrite && (
                    <span className="flex gap-1">
                      <Button size="icon" variant="ghost" aria-label={`Move ${t.name} up`} disabled={busy || i === 0} onClick={() => move(i, -1)}>
                        <ArrowUp aria-hidden="true" className="size-4" />
                      </Button>
                      <Button size="icon" variant="ghost" aria-label={`Move ${t.name} down`} disabled={busy || i === order.length - 1} onClick={() => move(i, 1)}>
                        <ArrowDown aria-hidden="true" className="size-4" />
                      </Button>
                      <Button size="sm" variant="outline" disabled={busy} onClick={() => void saveOrder(order.filter((o) => o.id !== t.id).map((o) => o.id))}>
                        Stop using
                      </Button>
                    </span>
                  )}
                </li>
              ))}
            </ol>
          )}
          {unused.length > 0 && (
            <div className="mt-3 flex flex-col gap-2 text-sm">
              <p className="text-muted-foreground">Not used for outgoing calls:</p>
              {unused.map((t) => (
                <div key={t.id} className="flex items-center gap-2">
                  <span className="min-w-0 flex-1">{t.name}{!t.enabled && <span className="text-muted-foreground"> · turned off</span>}</span>
                  {canWrite && t.enabled && (
                    <Button size="sm" variant="outline" disabled={busy} onClick={() => void saveOrder([...order.map((o) => o.id), t.id])}>Use</Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </Section>

        <Section title="What your phones can call" aside={<span className="text-sm text-muted-foreground">Every extension</span>}>
          {!level ? (
            <p className="text-sm text-muted-foreground">
              {settings?.default_call_permission_level_id ? "Loading…" : "Chosen in the setup wizard's Calls step. Until then, phones can call emergency numbers only."}
            </p>
          ) : (
            <div className="flex flex-col gap-4">
              {[...categorySwitches(countryName(country, countries)), ...(simpleMode ? [] : EXPERT_CATEGORY_SWITCHES)].map((sw) => {
                const on = sw.key.every((k) => level.allowed_categories.includes(k));
                const id = `cat-${sw.key.join("-")}`;
                return (
                  <div key={sw.label} className="flex flex-col gap-3">
                  <div className="flex items-center justify-between gap-4">
                    <Label htmlFor={id} className="flex flex-col items-start gap-0.5 font-normal">
                      <span className="text-sm font-medium">{sw.label}</span>
                      {sw.hint && <span className="text-sm text-muted-foreground">{sw.hint}</span>}
                      {"costly" in sw && !!sw.costly && !on && canWrite && (
                        <span className="text-sm text-muted-foreground">Turning this on asks you to confirm it's you: calls like these are where phone fraud costs money.</span>
                      )}
                    </Label>
                    <Switch id={id} checked={on} disabled={!canWrite} onCheckedChange={(v) => void setCategories(sw.key, v)} />
                  </div>
                  {sw.key.includes("international") && on && (
                    <AbroadChoice countries={countries} value={level.abroad_countries} onChange={(c) => void setAbroad(c)}
                      disabled={!canWrite} home={country} />
                  )}
                  </div>
                );
              })}
              <div className="flex items-center justify-between gap-4">
                <span className="flex flex-col gap-0.5">
                  <span className="text-sm font-medium">Emergency</span>
                  <span className="text-sm text-muted-foreground">Always: {emergencyWords(countries?.find((c) => c.code === country))}, from every phone.</span>
                </span>
                <span className="flex items-center gap-2">
                  <Lock aria-label="Always on" className="size-4 text-muted-foreground" />
                  <Switch checked disabled aria-label="Emergency (always on)" />
                </span>
              </div>
              {!simpleMode && (
                <div className="flex items-center justify-between gap-4">
                  <Label htmlFor="withhold" className="text-sm font-medium">Hide our number on outgoing calls</Label>
                  <Switch id="withhold" checked={level.withhold_caller_id} disabled={!canWrite} onCheckedChange={(v) => void setWithhold(v)} />
                </div>
              )}
              <p className="text-sm text-muted-foreground">Different rules for different people come later.</p>
            </div>
          )}
        </Section>

        <Section title="Alerts for calls abroad">
          <p className="text-sm text-muted-foreground">You're always told when someone calls a country for the first time, and about every emergency call.</p>
          <div className="mt-3 flex flex-wrap items-end gap-3 text-sm">
            <span>Tell me when an hour has more than</span>
            <Input aria-label="Calls abroad in an hour" className="w-20" inputMode="numeric" value={alertLimits.calls} disabled={!canWrite}
              onChange={(e) => setAlertLimits({ ...alertLimits, calls: e.target.value })} />
            <span>calls or</span>
            <Input aria-label="Minutes abroad in an hour" className="w-20" inputMode="numeric" value={alertLimits.minutes} disabled={!canWrite}
              onChange={(e) => setAlertLimits({ ...alertLimits, minutes: e.target.value })} />
            <span>minutes abroad.</span>
            {canWrite && <Button size="sm" disabled={busy} onClick={() => void saveAlert()}>Save</Button>}
          </div>
        </Section>

        {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
        {readOnlyReason && <p className="text-sm text-muted-foreground">{readOnlyReason}</p>}
      </div>
      {confirm.dialog}
    </div>
  );
}
