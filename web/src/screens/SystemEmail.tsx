// System → Settings → Email (ADR-066, docs/ui/SCREENS_PHASE1F.md §5.1):
// Linx sends invites, password resets, voicemail and alerts through a mail
// account the owner already has. Always encrypted; the password is sealed
// and never shown again. A system admin changes it, after "confirm it's
// you"; other admins see it read-only.
import { useCallback, useEffect, useState } from "react";
import { Check, TriangleAlert, X } from "lucide-react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Guarded, SystemCard } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import presets from "@/lib/email-presets.json";
import { hasScope } from "@/lib/roles";

type Email = components["schemas"]["Email"];
type Patch = components["schemas"]["EmailPatch"];
type Test = components["schemas"]["EmailTest"];
type Stage = Test["passed"][number];
type Preset = { id: string; name: string; host?: string; port?: number; security?: string; password: string;
  username_is_address: boolean; username?: string; where: string; link?: string; warning?: string };
const PRESETS = presets as Preset[];

const STAGES: { id: Stage; label: (host: string) => string }[] = [
  { id: "connect", label: (h) => `Connected to ${h}` },
  { id: "encrypt", label: () => "Encrypted" },
  { id: "certificate", label: () => "Certificate checked" },
  { id: "sign_in", label: () => "Signed in" },
  { id: "send", label: () => "Sent" },
];

/** What to check when the test email didn't arrive. */
const NOT_ARRIVED = [
  "Look in the spam or junk folder.",
  "Wait a minute: some providers hold the first email from a new app.",
  "A sending service (Postmark, Brevo, Amazon SES, Mailgun) only sends from an address or domain you've verified with it.",
  "Google and Microsoft may send from your own address only, or from an alias you've added to that account.",
];

function when(iso: string) {
  const d = new Date(iso);
  const t = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (d.toDateString() === new Date().toDateString()) return `today ${t}`;
  return `${d.toLocaleDateString([], { day: "numeric", month: "short" })} ${t}`;
}

function presetOf(id: string) {
  return PRESETS.find((p) => p.id === id) ?? PRESETS[PRESETS.length - 1]!;
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 py-1.5 text-sm">
      <span className="w-28 shrink-0 font-medium">{label}</span>
      <span className="min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

function TestResult({ test, host }: { test: Test; host: string }) {
  return (
    <ul className="flex flex-col gap-1.5 text-sm" aria-label="Test email">
      {STAGES.map((s) => {
        const passed = test.passed.includes(s.id);
        const failed = !test.ok && test.stage === s.id;
        if (!passed && !failed) return null;
        return (
          <li key={s.id} className="flex items-start gap-2">
            {passed
              ? <Check aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-available" />
              : <X aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-busy" />}
            <span className="min-w-0 break-words">{passed ? s.label(host) : test.error}</span>
          </li>
        );
      })}
    </ul>
  );
}

/** Set up email: who sends it, signing in, the test, done. */
function SetupEmail({ me, open, onOpenChange, saved, onSaved }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; saved: Email; onSaved: (e: Email) => void;
}) {
  const [step, setStep] = useState(1);
  const [preset, setPreset] = useState(saved.preset);
  const [host, setHost] = useState(saved.host);
  const [port, setPort] = useState(String(saved.port));
  const [security, setSecurity] = useState<Email["security"]>(saved.security);
  const [username, setUsername] = useState(saved.username);
  const [from, setFrom] = useState(saved.from_address || me.email || "");
  const [fromName, setFromName] = useState(saved.from_name || "Linx");
  const [password, setPassword] = useState("");
  const [limit, setLimit] = useState(String(saved.hourly_limit));
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [blocked, setBlocked] = useState("");
  const [test, setTest] = useState<Test | null>(null);
  const [notArrived, setNotArrived] = useState(false);
  const confirm = useConfirmIdentity(me);
  const p = presetOf(preset);
  const asksServer = !p.host;

  // Start afresh each time it opens (not when a save updates saved).
  const [opened, setOpened] = useState(false);
  if (open !== opened) {
    setOpened(open);
    if (open) start();
  }
  function start() {
    setStep(saved.host ? 2 : 1);
    setPreset(saved.preset); setHost(saved.host); setPort(String(saved.port)); setSecurity(saved.security);
    setUsername(saved.username); setFrom(saved.from_address || me.email || ""); setFromName(saved.from_name || "Linx");
    setPassword(""); setLimit(String(saved.hourly_limit)); setMore(false); setError(""); setBlocked(""); setTest(null); setNotArrived(false);
  }

  const choose = (id: string) => {
    const next = presetOf(id);
    setPreset(id);
    if (next.host) setHost(next.host);
    else if (id !== saved.preset) setHost("");
    if (next.port) { setPort(String(next.port)); setSecurity(next.security as Email["security"]); }
    if (!next.username_is_address && id !== saved.preset) setUsername("");
  };

  const body = (): Patch => {
    const b: Patch = { preset, from_address: from.trim(), from_name: fromName.trim(), host: host.trim(), port: Number(port), security };
    if (!p.username_is_address) b.username = username.trim();
    if (password) b.password = password;
    if (Number(limit) !== saved.hourly_limit) b.hourly_limit = Number(limit);
    return b;
  };
  const doSave = async () => {
    setBusy(true);
    setError("");
    setBlocked("");
    const { data, error: err } = await api.PATCH("/api/v1/email", { params: { header: { "If-Match": saved.etag } }, body: body() });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) {
      setError(problemMessage(err));
      if (problemCode(err) === "host_blocked") setBlocked(host.trim());
      return { confirm: false };
    }
    onSaved(data);
    setStep(3);
    void runTest();
    return { confirm: false };
  };
  const save = () => confirm.run(doSave);
  // A mail server on your own network: allowed on purpose (ADR-028), a
  // confirm-it's-you action of its own, as with alert channels.
  const allowAndSave = () => confirm.run(async () => {
    setBusy(true);
    const { error: err } = await api.POST("/api/v1/outbound-allowlist", { body: { value: blocked, description: "Email" } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (err && problemCode(err) !== "allowlist_duplicate") { setError(problemMessage(err)); return { confirm: false }; }
    return doSave();
  });
  const runTest = async () => {
    setBusy(true);
    setTest(null);
    setNotArrived(false);
    const { data, error: err } = await api.POST("/api/v1/email/test");
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setTest(data);
    if (data.blocked_host) setBlocked(data.blocked_host);
  };
  const arrived = () => confirm.run(async () => {
    setBusy(true);
    const { data, error: err } = await api.PATCH("/api/v1/email", { body: { arrived: true, enabled: true } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    onSaved(data);
    setStep(4);
    return { confirm: false };
  });

  const ready = from.trim() !== "" && host.trim() !== "" && Number(port) > 0 && (password !== "" || saved.password_set) &&
    (p.username_is_address || username.trim() !== "");
  const passwordLabel = saved.password_set ? `${p.password} (leave empty to keep the one saved)` : p.password;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Set up email</SheetTitle>
          <SheetDescription>1 Who sends it · 2 Sign in · 3 Test · 4 Done</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4 text-sm">
          {step === 1 && (
            <RadioGroup value={preset} onValueChange={choose} aria-label="Who sends it">
              {PRESETS.map((x) => (
                <label key={x.id} htmlFor={`email-${x.id}`} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                  <RadioGroupItem id={`email-${x.id}`} value={x.id} className="mt-0.5" />
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="flex flex-wrap items-center gap-2 font-medium">
                      {x.name}{x.id === "google" && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended if you use it</span>}
                    </span>
                    {x.warning && preset === x.id && (
                      <span className="flex items-start gap-1.5 text-status-away">
                        <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" /><span className="min-w-0">{x.warning}</span>
                      </span>
                    )}
                  </span>
                </label>
              ))}
            </RadioGroup>
          )}
          {step === 2 && (
            <>
              <p className="font-medium">{p.name}</p>
              <p className="text-muted-foreground">
                {p.where}{p.link && <> <a className="text-link underline-offset-4 hover:underline break-all" href={p.link} target="_blank" rel="noreferrer noopener">Open it</a></>}
              </p>
              <div className="flex flex-col gap-2">
                <Label htmlFor="email-from">Send from this address</Label>
                <Input id="email-from" type="email" autoComplete="off" value={from} onChange={(e) => setFrom(e.target.value)} placeholder="pbx@example.com" />
              </div>
              {!p.username_is_address && (
                <div className="flex flex-col gap-2">
                  <Label htmlFor="email-username">{p.username}</Label>
                  <Input id="email-username" autoComplete="off" className="font-mono" value={username} onChange={(e) => setUsername(e.target.value)} />
                </div>
              )}
              <div className="flex flex-col gap-2">
                <Label htmlFor="email-password">{passwordLabel}</Label>
                <Input id="email-password" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="email-name">"From" name</Label>
                <Input id="email-name" value={fromName} onChange={(e) => setFromName(e.target.value)} maxLength={100} />
              </div>
              {(asksServer || more) && (
                <div className="flex flex-col gap-4 rounded-md border p-3">
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="email-host">Mail server</Label>
                    <Input id="email-host" autoComplete="off" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)}
                      placeholder={preset === "ses" ? "email-smtp.eu-west-1.amazonaws.com" : "smtp.example.com"} />
                  </div>
                  <div className="flex flex-col gap-2">
                    <span className="font-medium">Encryption</span>
                    <RadioGroup value={security} onValueChange={(v) => { setSecurity(v as Email["security"]); setPort(v === "tls" ? "465" : "587"); }} aria-label="Encryption">
                      <label htmlFor="email-tls" className="flex items-center gap-2"><RadioGroupItem id="email-tls" value="tls" /> From the start (port 465)</label>
                      <label htmlFor="email-starttls" className="flex items-center gap-2"><RadioGroupItem id="email-starttls" value="starttls" /> After connecting, STARTTLS (port 587)</label>
                    </RadioGroup>
                  </div>
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="email-port">Port</Label>
                    <Input id="email-port" inputMode="numeric" className="w-28" value={port} onChange={(e) => setPort(e.target.value)} />
                  </div>
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="email-limit">At most this many emails an hour</Label>
                    <Input id="email-limit" inputMode="numeric" className="w-28" value={limit} onChange={(e) => setLimit(e.target.value)} />
                    <span className="text-muted-foreground">If an account here is taken over, Linx can't be used to send spam.</span>
                  </div>
                </div>
              )}
              {!asksServer && !more && (
                <Button variant="link" className="h-auto w-fit p-0" onClick={() => setMore(true)}>More: server, port, emails an hour</Button>
              )}
              <p className="text-muted-foreground">Linx only sends encrypted, and checks the server's certificate. The password is never shown again.</p>
            </>
          )}
          {step === 3 && (
            <>
              <p>Linx sends a test to your own address{test ? `, ${test.to}` : ""}.</p>
              {busy && !test && <p role="status">Sending…</p>}
              {test && <TestResult test={test} host={host.trim()} />}
              {test?.ok && !notArrived && (
                <div className="flex flex-col gap-2">
                  <p className="font-medium">Did it arrive?</p>
                  <div className="flex flex-wrap gap-2">
                    <Button disabled={busy} onClick={() => void arrived()}>Yes</Button>
                    <Button variant="outline" onClick={() => setNotArrived(true)}>No, show me what to check</Button>
                  </div>
                </div>
              )}
              {notArrived && (
                <div className="flex flex-col gap-2">
                  <ul className="list-disc space-y-1 ps-5">{NOT_ARRIVED.map((n) => <li key={n}>{n}</li>)}</ul>
                  <div className="flex flex-wrap gap-2">
                    <Button variant="outline" disabled={busy} onClick={() => void runTest()}>Send it again</Button>
                    <Button disabled={busy} onClick={() => void arrived()}>It arrived now</Button>
                  </div>
                </div>
              )}
              {test && !test.ok && (
                <div className="flex flex-wrap gap-2">
                  <Button variant="outline" onClick={() => setStep(2)}>Back to signing in</Button>
                  <Button variant="outline" disabled={busy} onClick={() => void runTest()}>Try again</Button>
                </div>
              )}
            </>
          )}
          {step === 4 && (
            <div className="flex flex-col gap-2">
              <p className="flex items-center gap-2 font-medium"><Check aria-hidden="true" className="size-4 text-status-available" /> Email is on.</p>
              <p className="text-muted-foreground">Linx will now offer to send invites by email, and you can add an email alert channel in System → Alerts.</p>
            </div>
          )}
          {blocked && (
            <div className="flex flex-col gap-2 rounded-md border border-status-away/50 p-3">
              <p>{blocked} is on your own network. Linx only sends there if you allow it.</p>
              <Button size="sm" className="self-start" disabled={busy} onClick={() => void allowAndSave()}>Allow {blocked} and continue</Button>
            </div>
          )}
          {error && !blocked && <p role="alert" className="font-medium text-destructive break-words">{error}</p>}
        </div>
        <div className="mt-auto flex items-center justify-between gap-2 border-t p-4">
          {step === 1 && <><span /><Button onClick={() => setStep(2)}>Next</Button></>}
          {step === 2 && (
            <>
              <Button variant="outline" disabled={busy} onClick={() => setStep(1)}>Back</Button>
              <Button disabled={busy || !ready} aria-busy={busy} onClick={() => void save()}>Save and send a test</Button>
            </>
          )}
          {step === 3 && <><span /><Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button></>}
          {step === 4 && <><span /><Button onClick={() => onOpenChange(false)}>Done</Button></>}
        </div>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

export function EmailCard({ me }: { me: Me }) {
  const [email, setEmail] = useState<Email | null>(null);
  const [setup, setSetup] = useState(false);
  const [test, setTest] = useState<Test | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const canChange = me.role === "system_admin" && hasScope(me, "settings:write");
  const reason = "Only a system admin can change how Linx sends email";

  const load = useCallback(async () => {
    const { data } = await api.GET("/api/v1/email");
    if (data) setEmail(data);
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (!email) return null;
  const p = presetOf(email.preset);
  const sendTest = async () => {
    setBusy(true);
    setTest(null);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/email/test");
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setTest(data);
    void load();
  };
  const turn = (on: boolean) => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/email", { params: { header: { "If-Match": email.etag } }, body: { enabled: on } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setEmail(data);
    return { confirm: false };
  });
  const setUp = email.host !== "" && email.from_address !== "";
  const s = email.status;

  return (
    <SystemCard title="Email" action={!setUp && (
      <Guarded allowed={canChange} reason={reason}><Button size="sm" disabled={!canChange} onClick={() => setSetup(true)}>Set up email</Button></Guarded>
    )}>
      <p className="text-sm text-muted-foreground">
        Linx sends invites, "Forgot your password?", voicemail and alerts through a mail account you already have.
      </p>
      {!setUp && <p className="mt-2 text-sm" role="status">Not set up.</p>}
      {setUp && (
        <div className="mt-3 flex flex-col">
          <Row label="Status">{email.enabled ? "On" : "Off: nothing is sent"}{!email.arrived_at && email.enabled ? " (no test email confirmed yet)" : ""}</Row>
          <Row label="Sending as">{email.from_name ? `${email.from_name} <${email.from_address}>` : email.from_address}</Row>
          <Row label="Through">{p.id === "other" ? email.host : p.name} ({email.host}, encrypted)</Row>
          <Row label="Last sent">
            {s.last_sent_at ? when(s.last_sent_at) : "Nothing yet"} · {s.sent_last_hour} this hour (limit {email.hourly_limit})
          </Row>
          <Row label="Queue">
            {s.waiting === 0 && !s.last_error && <span className="flex items-center gap-1.5"><Check aria-hidden="true" className="size-4 text-status-available" /> Nothing waiting</span>}
            {(s.waiting > 0 || s.last_error) && (
              <span className="flex items-start gap-1.5">
                <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
                <span className="min-w-0 break-words">{s.waiting > 0 ? `${s.waiting} waiting` : "Not sent"}{s.last_error ? `: ${s.last_error}` : ""}</span>
              </span>
            )}
          </Row>
          {test && <div className="mt-2"><TestResult test={test} host={email.host} /></div>}
          {error && <p role="alert" className="mt-2 text-sm font-medium text-destructive break-words">{error}</p>}
          <div className="mt-3 flex flex-wrap gap-2">
            <Guarded allowed={canChange} reason={reason}>
              <Button size="sm" variant="outline" disabled={!canChange || busy} onClick={() => void sendTest()}>Send a test email</Button>
            </Guarded>
            <Guarded allowed={canChange} reason={reason}>
              <Button size="sm" variant="outline" disabled={!canChange || busy} onClick={() => setSetup(true)}>Change</Button>
            </Guarded>
            <Guarded allowed={canChange} reason={reason}>
              <Button size="sm" variant="outline" disabled={!canChange || busy} onClick={() => void turn(!email.enabled)}>{email.enabled ? "Turn off" : "Turn on"}</Button>
            </Guarded>
          </div>
        </div>
      )}
      <SetupEmail me={me} open={setup} onOpenChange={(o) => { setSetup(o); if (!o) void load(); }} saved={email} onSaved={setEmail} />
      {confirm.dialog}
    </SystemCard>
  );
}
