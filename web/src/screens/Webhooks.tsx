// Webhooks, an expert page (docs/ui/ADMIN_SCREENS_PHASE1E.md §10.4): list,
// add (guided: address, events, a test delivery; or quick: address, all
// events), and a detail sheet with events, secret rotation (shown once),
// recent deliveries with Resend, and "turn back on" after failures.
import { useCallback, useEffect, useMemo, useState } from "react";
import { Check, X } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { AddChooserDialog, useAlwaysQuickAdd } from "@/components/AddChooser";
import { DataTable } from "@/components/DataTable";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { hasScope } from "@/lib/roles";

type Webhook = components["schemas"]["Webhook"];
type Delivery = components["schemas"]["WebhookDelivery"];
type EventType = components["schemas"]["EventType"];

const GROUPS: { label: string; prefixes: string[] }[] = [
  { label: "People", prefixes: ["user."] },
  { label: "Extensions and phones", prefixes: ["extension.", "device."] },
  { label: "Calls", prefixes: ["call."] },
  { label: "Lines", prefixes: ["trunk."] },
];
function groupOf(name: string) {
  return GROUPS.find((g) => g.prefixes.some((p) => name.startsWith(p)))?.label ?? "Other";
}

function EventsPicker({ types, value, onChange }: { types: EventType[]; value: string[]; onChange: (v: string[]) => void }) {
  const groups = useMemo(() => {
    const m = new Map<string, EventType[]>();
    for (const t of types) { const g = groupOf(t.name); m.set(g, [...(m.get(g) ?? []), t]); }
    return [...m.entries()];
  }, [types]);
  const all = value.length === 0;
  return (
    <div className="flex flex-col gap-3 text-sm">
      <label className="flex items-center gap-2 font-medium">
        <Checkbox checked={all} onCheckedChange={(v) => onChange(v ? [] : types.slice(0, 1).map((t) => t.name))} /> Every event
      </label>
      {!all && groups.map(([label, list]) => (
        <fieldset key={label}>
          <legend className="font-medium">{label}</legend>
          <div className="mt-1 grid grid-cols-1 gap-1 sm:grid-cols-2">
            {list.map((t) => (
              <label key={t.name} className="flex items-start gap-2" title={t.description}>
                <Checkbox className="mt-0.5" checked={value.includes(t.name)}
                  onCheckedChange={(v) => onChange(v ? [...value, t.name] : value.filter((x) => x !== t.name))} />
                <span className="font-mono text-xs">{t.name}</span>
              </label>
            ))}
          </div>
        </fieldset>
      ))}
    </div>
  );
}

function SecretBox({ secret }: { secret: string }) {
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-card p-3 text-sm">
      <p className="font-medium">Its signing secret, shown once:</p>
      <p className="break-all font-mono text-xs">{secret}</p>
      <Button size="sm" variant="outline" className="self-start" onClick={() => void navigator.clipboard?.writeText(secret)}>Copy</Button>
      <p className="text-muted-foreground">Your software checks each request with it (Standard Webhooks).</p>
    </div>
  );
}

function DeliveryResult({ d }: { d: Delivery }) {
  const last = d.log?.at(-1);
  return (
    <p className="flex items-start gap-2 text-sm" role="status">
      {d.status === "succeeded" ? <Check aria-hidden="true" className="mt-0.5 size-4 text-status-available" /> : <X aria-hidden="true" className="mt-0.5 size-4 text-status-busy" />}
      <span>{d.status === "succeeded" ? `It answered ${last?.status_code ?? ""} in ${last?.duration_ms ?? 0} ms.` : `It didn't take it: ${last?.error ?? `answered ${last?.status_code ?? "nothing"}`}.`}</span>
    </p>
  );
}

function AddWebhook({ open, onOpenChange, quick, types, onDone }: {
  open: boolean; onOpenChange: (o: boolean) => void; quick: boolean; types: EventType[]; onDone: () => void;
}) {
  const [step, setStep] = useState(1);
  const [url, setUrl] = useState("");
  const [description, setDescription] = useState("");
  const [events, setEvents] = useState<string[]>([]);
  const [created, setCreated] = useState<{ webhook: Webhook; secret: string } | null>(null);
  const [test, setTest] = useState<Delivery | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { if (open) { setStep(1); setUrl(""); setDescription(""); setEvents([]); setCreated(null); setTest(null); setError(""); } }, [open]);

  const create = async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/webhooks", { body: { url: url.trim(), description: description.trim(), event_types: events } });
    if (!data) { setBusy(false); setError(problemMessage(err)); return; }
    setCreated(data);
    setStep(3);
    const t = await api.POST("/api/v1/webhooks/{id}/test", { params: { path: { id: data.webhook.id } } });
    setBusy(false);
    if (t.data) setTest(t.data);
  };
  const close = () => { onOpenChange(false); if (created) onDone(); };

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Add a webhook</DialogTitle>
          {!quick && <DialogDescription>1 Address · 2 Events · 3 Test</DialogDescription>}
        </DialogHeader>
        <div className="flex flex-col gap-4 text-sm">
          {step === 1 && (
            <>
              <div className="flex flex-col gap-2">
                <Label htmlFor="wh-url">Address</Label>
                <Input id="wh-url" className="font-mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://crm.example.com/linx" autoFocus />
                <p className="text-muted-foreground">HTTPS only. An address on your own network needs allowing first (System → Alerts shows how).</p>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="wh-desc">What it's for (optional)</Label>
                <Input id="wh-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
              </div>
              {quick && <p className="text-muted-foreground">It gets every event; narrow it down later.</p>}
            </>
          )}
          {step === 2 && <EventsPicker types={types} value={events} onChange={setEvents} />}
          {step === 3 && created && (
            <>
              <p className="font-medium">Added. A test event was sent.</p>
              {test ? <DeliveryResult d={test} /> : <p className="text-muted-foreground">Sending…</p>}
              <SecretBox secret={created.secret} />
            </>
          )}
          {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          {step === 3 ? <Button onClick={close}>Done</Button> : (
            <>
              {step === 2 && <Button variant="outline" onClick={() => setStep(1)}>Back</Button>}
              <Button disabled={busy || !url.trim().startsWith("https://")} aria-busy={busy}
                onClick={() => (quick || step === 2 ? void create() : setStep(2))}>
                {quick || step === 2 ? "Add and test" : "Next"}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function WebhookSheet({ webhook: initial, types, canWrite, onClose, onChanged }: {
  webhook: Webhook; types: EventType[]; canWrite: boolean; onClose: () => void; onChanged: () => void;
}) {
  const [w, setW] = useState(initial);
  const [deliveries, setDeliveries] = useState<Delivery[]>([]);
  const [events, setEvents] = useState(initial.event_types);
  const [secret, setSecret] = useState("");
  const [test, setTest] = useState<Delivery | null>(null);
  const [removing, setRemoving] = useState(false);
  const [error, setError] = useState("");

  const loadDeliveries = useCallback(async () => {
    const { data } = await api.GET("/api/v1/webhooks/{id}/deliveries", { params: { path: { id: w.id }, query: { limit: 20 } } });
    if (data) setDeliveries(data.items);
  }, [w.id]);
  useEffect(() => { void loadDeliveries(); }, [loadDeliveries]);

  const patch = async (body: components["schemas"]["WebhookPatch"]) => {
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/webhooks/{id}", { params: { path: { id: w.id }, header: { "If-Match": w.etag } }, body });
    if (!data) { setError(problemMessage(err)); return; }
    setW(data);
    onChanged();
  };
  const rotate = async () => {
    const { data, error: err } = await api.POST("/api/v1/webhooks/{id}/rotate-secret", { params: { path: { id: w.id } } });
    if (!data) { setError(problemMessage(err)); return; }
    setW(data.webhook);
    setSecret(data.secret);
  };
  const sendTest = async () => {
    setTest(null);
    const { data, error: err } = await api.POST("/api/v1/webhooks/{id}/test", { params: { path: { id: w.id } } });
    if (!data) { setError(problemMessage(err)); return; }
    setTest(data);
  };
  const resend = async (d: Delivery) => {
    const { error: err } = await api.POST("/api/v1/webhook-deliveries/{id}/replay", { params: { path: { id: d.id } } });
    if (err) { setError(problemMessage(err)); return; }
    void loadDeliveries();
  };
  const remove = async () => {
    const { response, error: err } = await api.DELETE("/api/v1/webhooks/{id}", { params: { path: { id: w.id } } });
    setRemoving(false);
    if (!response.ok) { setError(problemMessage(err)); return; }
    onChanged();
    onClose();
  };
  const offWhy = w.disabled_reason === "failing" ? "every delivery failed for 5 days" : w.disabled_reason === "gone" ? "it answered that it's gone (410)" : "someone turned it off";

  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full sm:max-w-lg">
        <SheetHeader>
          <SheetTitle className="break-all">{w.url}</SheetTitle>
          <SheetDescription>{w.description || "Webhook"}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 overflow-y-auto px-4 pb-4 text-sm">
          {!w.enabled && (
            <div className="flex flex-col gap-2 rounded-md border border-status-away/50 p-3">
              <p>It's turned off: {offWhy}.</p>
              {canWrite && <Button size="sm" className="self-start" onClick={() => void patch({ enabled: true })}>Turn back on</Button>}
            </div>
          )}
          <section className="flex flex-col gap-2">
            <h3 className="font-semibold">Events</h3>
            <EventsPicker types={types} value={events} onChange={setEvents} />
            {canWrite && events.join() !== w.event_types.join() && (
              <Button size="sm" className="self-start" onClick={() => void patch({ event_types: events })}>Save events</Button>
            )}
          </section>
          {canWrite && (
            <section className="flex flex-col gap-2">
              <h3 className="font-semibold">Test and secret</h3>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" onClick={() => void sendTest()}>Send a test</Button>
                <Button size="sm" variant="outline" onClick={() => void rotate()}>New secret</Button>
              </div>
              {test && <DeliveryResult d={test} />}
              {secret && <SecretBox secret={secret} />}
              {w.previous_secret_expires_at && <p className="text-muted-foreground">The old secret keeps signing until {new Date(w.previous_secret_expires_at).toLocaleString()}.</p>}
            </section>
          )}
          <section className="flex flex-col gap-2">
            <h3 className="font-semibold">Recent deliveries</h3>
            {deliveries.length === 0 && <p className="text-muted-foreground">None yet.</p>}
            <ul className="flex flex-col divide-y">
              {deliveries.map((d) => (
                <li key={d.id} className="flex flex-wrap items-center gap-2 py-2">
                  {d.status === "succeeded" ? <Check aria-label="Delivered" className="size-4 text-status-available" />
                    : d.status === "pending" ? <span className="text-xs text-muted-foreground">…</span> : <X aria-label="Failed" className="size-4 text-status-busy" />}
                  <span className="min-w-0 flex-1 font-mono text-xs">{d.event_type}</span>
                  <span className="text-muted-foreground">{new Date(d.created_at).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</span>
                  {canWrite && d.status !== "pending" && <Button size="sm" variant="outline" onClick={() => void resend(d)}>Resend</Button>}
                </li>
              ))}
            </ul>
          </section>
          {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
          {canWrite && (
            <section className="rounded-md border border-destructive/30 p-3">
              <h3 className="font-semibold text-destructive">Danger zone</h3>
              <div className="mt-2 flex gap-2">
                {w.enabled && <Button size="sm" variant="outline" onClick={() => void patch({ enabled: false })}>Turn off</Button>}
                <Button size="sm" variant="outline" onClick={() => setRemoving(true)}>Remove</Button>
              </div>
            </section>
          )}
        </div>
        <Dialog open={removing} onOpenChange={setRemoving}>
          <DialogContent>
            <DialogHeader><DialogTitle>Remove this webhook?</DialogTitle><DialogDescription>Events stop going to it at once.</DialogDescription></DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setRemoving(false)}>Cancel</Button>
              <Button variant="destructive" onClick={() => void remove()}>Remove</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </SheetContent>
    </Sheet>
  );
}

export function WebhooksScreen({ me }: { me: Me }) {
  const [hooks, setHooks] = useState<Webhook[] | null>(null);
  const [types, setTypes] = useState<EventType[]>([]);
  const [chooser, setChooser] = useState(false);
  const [guided, setGuided] = useState(false);
  const [quick, setQuick] = useState(false);
  const [always, setAlways] = useAlwaysQuickAdd("webhooks");
  const [selected, setSelected] = useState<Webhook | null>(null);
  const canWrite = hasScope(me, "webhooks:write");

  const load = useCallback(async () => {
    const [h, t] = await Promise.all([
      api.GET("/api/v1/webhooks", { params: { query: { limit: 100 } } }),
      api.GET("/api/v1/event-types", { params: { query: { limit: 200 } } }),
    ]);
    setHooks(h.data?.items ?? []);
    if (t.data) setTypes(t.data.items);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const columns: ColumnDef<Webhook>[] = [
    { accessorKey: "url", header: "Address", cell: ({ row }) => <span className="break-all font-mono text-xs">{row.original.url}</span> },
    { id: "events", header: "Events", meta: { wide: true }, accessorFn: (w) => (w.event_types.length === 0 ? "Every event" : `${w.event_types.length} events`) },
    { id: "last", header: "Last delivery", accessorFn: (w) => (w.failing_since ? "Failing" : w.last_success_at ? "Delivered" : "None yet"),
      cell: ({ row }) => {
        const w = row.original;
        if (!w.enabled) return <span className="text-status-away">Turned off</span>;
        if (w.failing_since) return <span className="flex items-center gap-1.5 text-status-busy"><X aria-hidden="true" className="size-4" />Failing</span>;
        if (w.last_success_at) return <span className="flex items-center gap-1.5"><Check aria-hidden="true" className="size-4 text-status-available" />Delivered</span>;
        return <span className="text-muted-foreground">None yet</span>;
      } },
  ];

  if (hooks === null) return <div className="p-6" aria-busy="true" />;
  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-semibold tracking-tight">Webhooks</h1>
          <p className="mt-1 text-sm text-muted-foreground">Linx tells your own software when something happens: a call, a new person, a line going down.</p>
        </div>
        {canWrite && <Button onClick={() => (always ? setQuick(true) : setChooser(true))}>+ Add</Button>}
      </div>
      <div className="mt-4">
        {hooks.length === 0
          ? <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">None yet.</p>
          : <DataTable columns={columns} data={hooks} onRowClick={setSelected} />}
      </div>
      <AddChooserDialog open={chooser} onOpenChange={setChooser} title="Add a webhook" guideHint="Address, which events, then a test delivery."
        quickHint="Just the address, every event." always={always} onAlwaysChange={setAlways} onGuide={() => setGuided(true)} onQuick={() => setQuick(true)} />
      <AddWebhook open={guided} onOpenChange={setGuided} quick={false} types={types} onDone={() => void load()} />
      <AddWebhook open={quick} onOpenChange={setQuick} quick types={types} onDone={() => void load()} />
      {selected && <WebhookSheet webhook={selected} types={types} canWrite={canWrite} onClose={() => setSelected(null)} onChanged={() => void load()} />}
    </div>
  );
}
