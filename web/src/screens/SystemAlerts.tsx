// System → Alerts (docs/ui/ADMIN_SCREENS_PHASE1E.md §10.2): open and recent
// alerts, and where alerts go (channels), with a guided add that ends with
// a test message.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { CircleCheck, Info, TriangleAlert } from "lucide-react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { AddChooserDialog, useAlwaysQuickAdd } from "@/components/AddChooser";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Guarded, SystemCard, SystemHeader } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { navigate } from "@/hooks/useRoute";
import { hasScope } from "@/lib/roles";
import { cn } from "@/lib/utils";

type Alert = components["schemas"]["Alert"];
type Channel = components["schemas"]["AlertChannel"];
type Kind = components["schemas"]["AlertChannelKind"];
type Config = components["schemas"]["AlertChannelConfig"];
type Severity = components["schemas"]["AlertSeverity"];
type TestResult = components["schemas"]["AlertChannelTestResult"];

// Each kind: one line about it, and the fields it needs with where to find them.
const KINDS: { kind: Kind; label: string; hint: string; fields: { key: keyof Config; label: string; help: string; secret?: boolean; optional?: boolean }[] }[] = [
  { kind: "ntfy", label: "ntfy", hint: "A free app for phones: alerts arrive as notifications.", fields: [
    { key: "topic", label: "Topic", help: "Make up a long, hard-to-guess name, then subscribe to it in the ntfy app." },
    { key: "server_url", label: "Server", help: "Leave empty for ntfy.sh, or your own ntfy server's address.", optional: true },
    { key: "access_token", label: "Access token", help: "Only if your topic is protected.", secret: true, optional: true },
  ] },
  { kind: "gotify", label: "Gotify", hint: "Your own notification server.", fields: [
    { key: "server_url", label: "Server", help: "Its address, e.g. https://gotify.example.com." },
    { key: "app_token", label: "App token", help: "Gotify → Apps → Create application, then copy its token.", secret: true },
  ] },
  { kind: "slack", label: "Slack", hint: "A message in a Slack channel.", fields: [
    { key: "url", label: "Webhook address", help: "Slack → Apps → Incoming Webhooks → Add to a channel, then copy the Webhook URL.", secret: true },
  ] },
  { kind: "teams", label: "Microsoft Teams", hint: "A message in a Teams channel.", fields: [
    { key: "url", label: "Webhook address", help: "The channel's ⋯ → Workflows → \"Post to a channel when a webhook request is received\", then copy its address.", secret: true },
  ] },
  { kind: "telegram", label: "Telegram", hint: "A message from your own Telegram bot.", fields: [
    { key: "bot_token", label: "Bot token", help: "Ask @BotFather for /newbot and copy the token it gives you.", secret: true },
    { key: "chat_id", label: "Chat ID", help: "Send your bot a message, then open api.telegram.org/bot<token>/getUpdates and copy chat.id." },
  ] },
  { kind: "webhook", label: "Webhook", hint: "For your own software: a signed HTTPS request.", fields: [
    { key: "url", label: "Address", help: "An https:// address that accepts POST requests." },
  ] },
  { kind: "email", label: "Email", hint: "An email to you or your team, through Linx's own email.", fields: [
    { key: "to", label: "Send to", help: "One or more addresses, separated by commas. \"Email isn't sending\" always goes to your other channels instead." },
  ] },
];
const EMAIL_FIRST = "Set up email first (System → Settings).";
const kindLabel = (k: string) => KINDS.find((x) => x.kind === k)?.label ?? k;

const SEVERITY_CHOICES: { value: Severity; title: string; hint: string; badge?: string }[] = [
  { value: "warning", title: "Problems", hint: "Critical ones and warnings: a line down, a failed backup, someone guessing a password.", badge: "Recommended" },
  { value: "critical", title: "Only critical", hint: "Only when calls can't be made or Linx needs you now." },
  { value: "info", title: "Everything", hint: "Also notes like a first call to a new country." },
];
const severityWords: Record<string, string> = { critical: "Only critical", warning: "Problems", info: "Everything" };

function SeverityIcon({ s }: { s: string }) {
  if (s === "critical") return <TriangleAlert aria-label="Critical" className="size-4 shrink-0 text-status-busy" />;
  if (s === "warning") return <TriangleAlert aria-label="Warning" className="size-4 shrink-0 text-status-away" />;
  return <Info aria-label="Note" className="size-4 shrink-0 text-muted-foreground" />;
}

function when(iso: string) {
  const d = new Date(iso);
  return d.toDateString() === new Date().toDateString()
    ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleDateString([], { day: "numeric", month: "short" }) + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function Field({ label, htmlFor, hint, children }: { label: string; htmlFor: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
    </div>
  );
}

function quietWords(c: Channel) {
  const q = c.quiet_hours;
  if (!q?.enabled) return "";
  return `Quiet ${q.start}–${q.end}${q.bypass_critical === false ? "" : ", critical always sent"}`;
}

/** The host a refused (private) address points at, for the allowlist offer. */
function hostOf(config: Config): string {
  const raw = config.server_url || config.url || "";
  try {
    return new URL(raw).hostname;
  } catch {
    return "";
  }
}

// --- Add a channel: guided (§10.2) and quick --------------------------------

function ChannelForm({ kind, config, setConfig }: { kind: Kind; config: Config; setConfig: (c: Config) => void }) {
  const k = KINDS.find((x) => x.kind === kind)!;
  return (
    <div className="flex flex-col gap-4">
      {k.fields.map((f) => (
        <Field key={f.key} label={f.label + (f.optional ? " (optional)" : "")} htmlFor={`ch-${f.key}`} hint={f.help}>
          <Input id={`ch-${f.key}`} className="font-mono" type={f.secret ? "password" : "text"} autoComplete="off"
            value={config[f.key] ?? ""} onChange={(e) => setConfig({ ...config, [f.key]: e.target.value })} />
        </Field>
      ))}
    </div>
  );
}

const ready = (kind: Kind, config: Config) =>
  KINDS.find((x) => x.kind === kind)!.fields.every((f) => f.optional || !!config[f.key]?.trim());

function cleanConfig(config: Config): Config {
  const out: Config = {};
  for (const [k, v] of Object.entries(config)) if (typeof v === "string" && v.trim()) out[k as keyof Config] = v.trim();
  return out;
}

function AddChannel({ me, open, onOpenChange, quick, onGuideInstead, onDone }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; quick: boolean; onGuideInstead?: () => void; onDone: () => void;
}) {
  const [step, setStep] = useState(1);
  const [kind, setKind] = useState<Kind>("ntfy");
  const [name, setName] = useState("");
  const [config, setConfig] = useState<Config>({});
  const [severity, setSeverity] = useState<Severity>("warning");
  const [quiet, setQuiet] = useState(false);
  const [start, setStart] = useState("22:00");
  const [end, setEnd] = useState("07:00");
  const [created, setCreated] = useState<Channel | null>(null);
  const [secret, setSecret] = useState("");
  const [test, setTest] = useState<TestResult | null>(null);
  const [allow, setAllow] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const canAllow = hasScope(me, "outbound_allowlist:write");
  // Email is offered greyed until it's set up (docs/ui/SCREENS_PHASE1F.md §0).
  const [emailOn, setEmailOn] = useState(false);
  useEffect(() => {
    if (open) void api.GET("/api/v1/email").then(({ data }) => setEmailOn(!!data?.enabled));
  }, [open]);

  useEffect(() => {
    if (!open) return;
    setStep(quick ? 2 : 1); setKind("ntfy"); setName(""); setConfig({}); setSeverity("warning"); setQuiet(false);
    setStart("22:00"); setEnd("07:00"); setCreated(null); setSecret(""); setTest(null); setAllow(""); setError("");
  }, [open, quick]);

  const sendTest = async (id: string) => {
    setTest(null);
    const { data, error: err } = await api.POST("/api/v1/alert-channels/{id}/test", { params: { path: { id } } });
    setTest(data ?? { succeeded: false, error: problemMessage(err) });
  };

  const create = async () => {
    setBusy(true);
    setError("");
    setAllow("");
    const { data, error: err } = await api.POST("/api/v1/alert-channels", {
      body: {
        kind, name: name.trim() || kindLabel(kind), config: cleanConfig(config), min_severity: severity,
        ...(quiet ? { quiet_hours: { enabled: true, start, end, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, bypass_critical: true } } : {}),
      },
    });
    setBusy(false);
    if (!data) {
      if (problemCode(err) === "url_blocked" && hostOf(config)) setAllow(hostOf(config));
      setError(problemMessage(err));
      return;
    }
    setCreated(data.alert_channel);
    setSecret(data.secret ?? "");
    setStep(5);
    void sendTest(data.alert_channel.id);
  };

  // ADR-028: Linx never sends to the home network unless an admin allows
  // that address; it's a confirm-it's-you action.
  const allowAndRetry = () => confirm.run(async () => {
    setBusy(true);
    const { error: err } = await api.POST("/api/v1/outbound-allowlist", { body: { value: allow, description: `Alerts: ${name.trim() || kindLabel(kind)}` } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (err && problemCode(err) !== "allowlist_duplicate") { setError(problemMessage(err)); return { confirm: false }; }
    await create();
    return { confirm: false };
  });

  const close = () => { onOpenChange(false); if (created) onDone(); };
  const titles = ["Where", "Details", "Which alerts", "Quiet hours", "Test"];

  const body = (
    <div className="flex flex-col gap-4">
      {step === 1 && (
        <RadioGroup value={kind} onValueChange={(v) => { setKind(v as Kind); setConfig({}); }}>
          {KINDS.map((k) => {
            const off = k.kind === "email" && !emailOn;
            return (
              <label key={k.kind} htmlFor={`kind-${k.kind}`}
                className={`flex items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary ${off ? "opacity-60" : "cursor-pointer"}`}>
                <RadioGroupItem id={`kind-${k.kind}`} value={k.kind} className="mt-0.5" disabled={off} />
                <span className="flex flex-col gap-0.5">
                  <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                    {k.label}
                    {k.kind === "ntfy" && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended for phones</span>}
                  </span>
                  <span className="text-sm text-muted-foreground">{k.hint}</span>
                  {off && (
                    <a className="text-sm text-link underline-offset-4 hover:underline" href="/admin/system/settings"
                      onClick={(e) => { e.preventDefault(); navigate("/admin/system/settings"); }}>{EMAIL_FIRST}</a>
                  )}
                </span>
              </label>
            );
          })}
        </RadioGroup>
      )}
      {step === 2 && (
        <>
          {quick && (
            <Field label="Where" htmlFor="ch-kind">
              <select id="ch-kind" value={kind} onChange={(e) => { setKind(e.target.value as Kind); setConfig({}); }}
                className="h-9 rounded-md border bg-background px-3 text-sm">
                {KINDS.map((k) => <option key={k.kind} value={k.kind} disabled={k.kind === "email" && !emailOn}>
                  {k.label}{k.kind === "email" && !emailOn ? ` (${EMAIL_FIRST})` : ""}</option>)}
              </select>
            </Field>
          )}
          <Field label="Name" htmlFor="ch-name" hint="So you know which one it is, e.g. My phone.">
            <Input id="ch-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={kindLabel(kind)} />
          </Field>
          <ChannelForm kind={kind} config={config} setConfig={setConfig} />
        </>
      )}
      {step === 3 && (
        <RadioGroup value={severity} onValueChange={(v) => setSeverity(v as Severity)}>
          {SEVERITY_CHOICES.map((c) => (
            <label key={c.value} htmlFor={`sev-${c.value}`} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
              <RadioGroupItem id={`sev-${c.value}`} value={c.value} className="mt-0.5" />
              <span className="flex flex-col gap-0.5">
                <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                  {c.title}{c.badge && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">{c.badge}</span>}
                </span>
                <span className="text-sm text-muted-foreground">{c.hint}</span>
              </span>
            </label>
          ))}
        </RadioGroup>
      )}
      {step === 4 && (
        <div className="flex flex-col gap-3">
          <RadioGroup value={quiet ? "on" : "off"} onValueChange={(v) => setQuiet(v === "on")}>
            <label htmlFor="quiet-off" className="flex cursor-pointer items-center gap-3 text-sm"><RadioGroupItem id="quiet-off" value="off" /> Any time</label>
            <label htmlFor="quiet-on" className="flex cursor-pointer items-center gap-3 text-sm"><RadioGroupItem id="quiet-on" value="on" /> Not at night</label>
          </RadioGroup>
          {quiet && (
            <div className="flex flex-wrap items-end gap-3">
              <Field label="From" htmlFor="quiet-start"><Input id="quiet-start" type="time" className="w-32" value={start} onChange={(e) => setStart(e.target.value)} /></Field>
              <Field label="Until" htmlFor="quiet-end"><Input id="quiet-end" type="time" className="w-32" value={end} onChange={(e) => setEnd(e.target.value)} /></Field>
            </div>
          )}
          {quiet && <p className="text-sm text-muted-foreground">Critical alerts are always sent. Times are in your time zone ({Intl.DateTimeFormat().resolvedOptions().timeZone}).</p>}
        </div>
      )}
      {step === 5 && created && (
        <div className="flex flex-col gap-3 text-sm" aria-live="polite">
          <p className="font-medium">"{created.name}" is added. A test message is on its way.</p>
          {!test && <p className="text-muted-foreground">Sending…</p>}
          {test?.succeeded && <p className="flex items-center gap-2"><CircleCheck aria-hidden="true" className="size-4 text-status-available" /> {kindLabel(kind)} took it. Did it arrive?</p>}
          {test && !test.succeeded && <p className="text-destructive">It didn't go through: {test.error}</p>}
          {secret && (
            <div className="rounded-md border bg-card p-3">
              <p className="font-medium">Its signing secret, shown once:</p>
              <p className="mt-1 break-all font-mono text-xs">{secret}</p>
            </div>
          )}
          {test && <Button size="sm" variant="outline" className="self-start" onClick={() => void sendTest(created.id)}>Send another test</Button>}
        </div>
      )}
      {allow && canAllow && (
        <div className="flex flex-col gap-2 rounded-md border border-status-away/50 p-3 text-sm">
          <p>{allow} is on your own network. Linx only sends there if you allow it.</p>
          <Button size="sm" className="self-start" disabled={busy} onClick={() => void allowAndRetry()}>Allow {allow} and add</Button>
        </div>
      )}
      {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
    </div>
  );

  const nextDisabled = busy || (step === 2 && !ready(kind, config));
  const footer = step === 5
    ? <Button onClick={close}>{test?.succeeded ? "It arrived" : "Done"}</Button>
    : (
      <div className="flex w-full items-center justify-between gap-2">
        <Button variant="outline" disabled={step === (quick ? 2 : 1) || busy} onClick={() => setStep(step - 1)}>Back</Button>
        <Button disabled={nextDisabled} aria-busy={busy} onClick={() => (quick || step === 4 ? void create() : setStep(step + 1))}>
          {quick || step === 4 ? "Add and test" : "Next"}
        </Button>
      </div>
    );

  if (quick) {
    return (
      <Dialog open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
        <DialogContent className="max-h-[90dvh] overflow-y-auto">
          <DialogHeader><DialogTitle>Add a place for alerts</DialogTitle></DialogHeader>
          {onGuideInstead && step < 5 && (
            <button type="button" className="-mt-2 self-start text-sm text-link underline-offset-4 hover:underline" onClick={() => { onOpenChange(false); onGuideInstead(); }}>
              Guide me instead
            </button>
          )}
          {body}
          <DialogFooter>{footer}</DialogFooter>
          {confirm.dialog}
        </DialogContent>
      </Dialog>
    );
  }
  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add a place for alerts</SheetTitle>
          <SheetDescription>{titles.map((t, i) => `${i + 1} ${t}`).join(" · ")}</SheetDescription>
        </SheetHeader>
        <div className="overflow-y-auto px-4 pb-4">{body}</div>
        <div className="mt-auto flex border-t p-4">{footer}</div>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

// --- The page -----------------------------------------------------------------

export function SystemAlertsScreen({ me }: { me: Me }) {
  const [alerts, setAlerts] = useState<Alert[] | null>(null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [chooserOpen, setChooserOpen] = useState(false);
  const [guided, setGuided] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [always, setAlways] = useAlwaysQuickAdd("alert-channels");
  const [tests, setTests] = useState<Record<string, TestResult | "sending">>({});
  const [removing, setRemoving] = useState<Channel | null>(null);
  const [error, setError] = useState("");
  const canWrite = hasScope(me, "alerts:write");

  const load = useCallback(async () => {
    const [a, c] = await Promise.all([
      api.GET("/api/v1/alerts", { params: { query: { limit: 50 } } }),
      api.GET("/api/v1/alert-channels", { params: { query: { limit: 100 } } }),
    ]);
    if (a.data) setAlerts(a.data.items);
    if (c.data) setChannels(c.data.items);
  }, []);
  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 15_000);
    return () => window.clearInterval(t);
  }, [load]);

  const sendTest = async (c: Channel) => {
    setTests((t) => ({ ...t, [c.id]: "sending" }));
    const { data, error: err } = await api.POST("/api/v1/alert-channels/{id}/test", { params: { path: { id: c.id } } });
    setTests((t) => ({ ...t, [c.id]: data ?? { succeeded: false, error: problemMessage(err) } }));
  };
  const toggle = async (c: Channel) => {
    const { data, error: err } = await api.PATCH("/api/v1/alert-channels/{id}", {
      params: { path: { id: c.id }, header: { "If-Match": c.etag } }, body: { enabled: !c.enabled },
    });
    if (!data) { setError(problemMessage(err)); return; }
    setChannels((list) => list.map((x) => (x.id === c.id ? data : x)));
  };
  const remove = async () => {
    if (!removing) return;
    const { response, error: err } = await api.DELETE("/api/v1/alert-channels/{id}", { params: { path: { id: removing.id } } });
    setRemoving(null);
    if (!response.ok) { setError(problemMessage(err)); return; }
    void load();
  };

  const open = (alerts ?? []).filter((a) => a.status === "open");
  const recent = (alerts ?? []).filter((a) => a.status === "resolved").slice(0, 10);
  const reason = "Only an admin can change where alerts go";

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <SystemHeader current="/admin/system/alerts" systemAdmin={me.role === "system_admin"} />
      <div className="mt-6 flex flex-col gap-6">
        <SystemCard title="Open and recent alerts">
          {alerts === null && <p className="text-sm text-muted-foreground">Loading…</p>}
          {alerts !== null && open.length === 0 && <p className="text-sm text-muted-foreground">Nothing needs you right now.</p>}
          <ul className="flex flex-col gap-3">
            {[...open, ...recent].map((a) => (
              <li key={a.id} className={cn("flex items-start gap-2.5 text-sm", a.status === "resolved" && "text-muted-foreground")}>
                <span className="mt-0.5">{a.status === "resolved" ? <CircleCheck aria-label="Resolved" className="size-4 shrink-0 text-status-available" /> : <SeverityIcon s={a.severity} />}</span>
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{a.title}</span>
                  <span className="block">{a.message}</span>
                  <span className="block text-xs text-muted-foreground">
                    Since {when(a.first_seen_at)}{a.resolved_at ? ` · resolved ${when(a.resolved_at)}` : ""}
                  </span>
                </span>
              </li>
            ))}
          </ul>
        </SystemCard>

        <SystemCard title="Where alerts go" action={
          <Guarded allowed={canWrite} reason={reason}>
            <Button size="sm" disabled={!canWrite} onClick={() => (always ? setQuickOpen(true) : setChooserOpen(true))}>+ Add</Button>
          </Guarded>
        }>
          {channels.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              Nowhere yet: alerts show here and on the home page only. Add your phone (ntfy) so a line going down reaches you.
            </p>
          ) : (
            <ul className="flex flex-col divide-y">
              {channels.map((c) => {
                const t = tests[c.id];
                return (
                  <li key={c.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3 text-sm">
                    <span className="flex min-w-48 grow basis-48 flex-col">
                      <span className="font-medium">{c.name}{!c.enabled && <span className="font-normal text-muted-foreground"> · turned off</span>}</span>
                      <span className="text-muted-foreground">{kindLabel(c.kind)} · {severityWords[c.min_severity]}{quietWords(c) ? ` · ${quietWords(c)}` : ""}</span>
                      {t && t !== "sending" && (
                        <span className={t.succeeded ? "text-status-available" : "text-destructive"} role="status">
                          {t.succeeded ? "Test sent." : `Test failed: ${t.error}`}
                        </span>
                      )}
                    </span>
                    {canWrite && (
                      <span className="flex flex-wrap gap-2">
                        <Button size="sm" variant="outline" disabled={t === "sending"} onClick={() => void sendTest(c)}>{t === "sending" ? "Sending…" : "Send a test"}</Button>
                        <Button size="sm" variant="outline" onClick={() => void toggle(c)}>{c.enabled ? "Turn off" : "Turn on"}</Button>
                        <Button size="sm" variant="outline" onClick={() => setRemoving(c)}>Remove</Button>
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </SystemCard>
        {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
      </div>

      <AddChooserDialog open={chooserOpen} onOpenChange={setChooserOpen} title="Add a place for alerts"
        guideHint="Where, which alerts, quiet hours, then a test." quickHint="Just where, and its details."
        always={always} onAlwaysChange={setAlways} onGuide={() => setGuided(true)} onQuick={() => setQuickOpen(true)} />
      <AddChannel me={me} open={guided} onOpenChange={setGuided} quick={false} onDone={() => void load()} />
      <AddChannel me={me} open={quickOpen} onOpenChange={setQuickOpen} quick onGuideInstead={() => setGuided(true)} onDone={() => void load()} />
      <Dialog open={!!removing} onOpenChange={(o) => { if (!o) setRemoving(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove "{removing?.name}"?</DialogTitle>
            <DialogDescription>Alerts stop going there at once.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRemoving(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => void remove()}>Remove</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
