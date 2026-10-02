// Phone lines (docs/ui/ADMIN_SCREENS_PHASE1E.md §6, docs/SIMPLER.md §1):
// list, add (guided or quick), the connection test as a checklist, and the
// line detail sheet. A phone system or gateway signs in to Linx with a
// login Linx makes; a phone company is added from its template or from
// what it sent you, then tested before it's turned on.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, CircleAlert, CircleDashed, Loader2, Search, TriangleAlert, X } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { AddChooserDialog, useAlwaysQuickAdd } from "@/components/AddChooser";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { DataTable } from "@/components/DataTable";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { navigate } from "@/hooks/useRoute";
import { GATEWAY_PRODUCTS, gatewaySteps, type GatewayProduct } from "@/lib/gatewayHowTo";
import { kindWords, ordinal, securityWords, sinceWords, trunkDot, trunkWords } from "@/lib/lines";
import { parseProviderText, type PastedSettings } from "@/lib/providerPaste";
import { isReadOnlyAdmin } from "@/lib/roles";
import { cn } from "@/lib/utils";

type Trunk = components["schemas"]["Trunk"];
type Did = components["schemas"]["Did"];
type Extension = components["schemas"]["Extension"];
type TrunkTest = components["schemas"]["TrunkTest"];
type TrunkLogin = components["schemas"]["TrunkLogin"];

const NOBODY = "none";

// --- Small shared pieces ------------------------------------------------------

function Field({ label, htmlFor, hint, children }: { label: string; htmlFor: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
    </div>
  );
}
function FormError({ message }: { message: string }) {
  if (!message) return null;
  return <p role="alert" className="text-sm font-medium text-destructive">{message}</p>;
}
function Choice({ id, value, title, hint, badge }: { id: string; value: string; title: string; hint?: string; badge?: string }) {
  return (
    <label htmlFor={id} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
      <RadioGroupItem id={id} value={value} className="mt-0.5" />
      <span className="flex flex-col gap-0.5">
        <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
          {title}
          {badge && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">{badge}</span>}
        </span>
        {hint && <span className="text-sm text-muted-foreground">{hint}</span>}
      </span>
    </label>
  );
}

function extLabel(e: Extension) {
  return `${e.number} ${e.display_name}`;
}

/** "Rings" picker: an extension, or nobody. */
export function RingsSelect({ id, value, onChange, extensions, disabled, label }: {
  id: string; value: string | undefined; onChange: (v: string | undefined) => void; extensions: Extension[]; disabled?: boolean; label: string;
}) {
  return (
    <Select value={value ?? NOBODY} onValueChange={(v) => onChange(v === NOBODY ? undefined : v)} disabled={disabled}>
      <SelectTrigger id={id} aria-label={label} className="w-full min-w-0 sm:w-56"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value={NOBODY}>Nobody (not answered)</SelectItem>
        {extensions.filter((e) => e.enabled).map((e) => <SelectItem key={e.id} value={e.id}>{extLabel(e)}</SelectItem>)}
      </SelectContent>
    </Select>
  );
}

/** Where a number goes when it's more than one person: set in Incoming. */
function InIncoming({ what }: { what: string }) {
  return (
    <span className="flex items-center gap-2 text-sm text-muted-foreground">
      {what}
      <Button size="sm" variant="ghost" onClick={() => navigate("/admin/incoming")}>Change in Incoming</Button>
    </span>
  );
}

function StatusLine({ t }: { t: Trunk }) {
  return (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <Dot tone={trunkDot[t.status] ?? "neutral"} />
      <span>{t.kind === "registers_here" && t.status === "registered" ? "Signed in" : trunkWords[t.status] ?? t.status}</span>
      {t.status_since && (t.status === "unreachable" || t.status === "rejected") && (
        <span className="text-muted-foreground">{sinceWords(t.status_since)}</span>
      )}
    </span>
  );
}

// --- The connection test, as a checklist (§6.2 step 4) ----------------------

const stepTitle: Record<string, string> = {
  tunnel: "Private connection", address: "Found its address", connection: "Connected", certificate: "Its certificate",
  sip: "It answers", login: "Signed in", audio_encryption: "Encrypted audio", tls_offered: "Encryption offered",
  signed_in: "Signed in to Linx",
};

function StepIcon({ result }: { result: string }) {
  if (result === "ok") return <Check aria-label="Passed" className="size-4 shrink-0 text-status-available" />;
  if (result === "failed") return <X aria-label="Failed" className="size-4 shrink-0 text-status-busy" />;
  if (result === "warning") return <TriangleAlert aria-label="Warning" className="size-4 shrink-0 text-status-away" />;
  return <CircleDashed aria-label="Not checked" className="size-4 shrink-0 text-muted-foreground" />;
}

/**
 * Runs POST /trunks/{id}/test and shows it filling in. An untrusted
 * certificate can be trusted from here (its fingerprint compared first);
 * `onResult` hears every finished run.
 */
export function TestPanel({ trunk, onTrunk, onResult, readOnly }: {
  trunk: Trunk; onTrunk: (t: Trunk) => void; onResult?: (r: TrunkTest) => void; readOnly?: boolean;
}) {
  const [result, setResult] = useState<TrunkTest | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const heard = useRef(onResult);
  heard.current = onResult;

  const run = useCallback(async () => {
    setRunning(true);
    setError("");
    setResult(null);
    const { data, error: err } = await api.POST("/api/v1/trunks/{id}/test", { params: { path: { id: trunk.id } } });
    setRunning(false);
    if (!data) { setError(problemMessage(err)); return; }
    setResult(data);
    heard.current?.(data);
  }, [trunk.id]);
  // Once, and again whenever the line's settings change (a trusted
  // certificate, a new address): not when it's turned on after passing.
  const settingsKey = `${trunk.host}|${trunk.port}|${trunk.username ?? ""}|${trunk.cert_trust}|${trunk.pinned_certificate ?? ""}`;
  useEffect(() => { void run(); }, [run, settingsKey]);

  const trust = async () => {
    const cert = result?.certificates?.at(-1);
    if (!cert) return;
    const { data, error: err } = await api.PATCH("/api/v1/trunks/{id}", {
      params: { path: { id: trunk.id }, header: { "If-Match": trunk.etag } },
      body: { cert_trust: "pinned", pinned_certificate: cert.pem },
    });
    if (!data) { setError(problemMessage(err)); return; }
    onTrunk(data);
  };

  // The certificate trusting pins: the top of what it presented (its CA,
  // or itself when self-signed), so its fingerprint is the one to compare,
  // as `linx trunk add` shows (Phase 1E review).
  const pinnable = result?.certificates?.at(-1);
  return (
    <div className="flex flex-col gap-3 rounded-md border bg-card p-4" aria-live="polite">
      <div className="flex items-center justify-between gap-2">
        <p className="font-medium">{running ? "Testing…" : result?.ok ? "It works" : result ? "Something needs fixing" : "Test"}</p>
        {!running && <Button size="sm" variant="outline" onClick={() => void run()}>Test again</Button>}
      </div>
      {running && <p className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 aria-hidden="true" className="size-4 animate-spin" /> This takes up to 20 seconds.</p>}
      {result && (
        <ul className="flex flex-col gap-2">
          {result.steps.map((s, i) => (
            <li key={i} className="flex items-start gap-2.5 text-sm">
              <span className="mt-0.5"><StepIcon result={s.result} /></span>
              <span className="min-w-0">
                <span className="font-medium">{stepTitle[s.name] ?? s.name}</span>
                <span className="block text-muted-foreground">{s.words}</span>
              </span>
            </li>
          ))}
        </ul>
      )}
      {result?.untrusted && pinnable && !readOnly && (
        <div className="flex flex-col gap-2 rounded-md border border-status-away/50 p-3 text-sm">
          <p className="font-medium">Its certificate isn't from a company Linx knows.</p>
          <p className="text-muted-foreground">
            If this is the phone system or company you expect, check this fingerprint matches the one it shows for its certificate, then trust it.
          </p>
          <dl className="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-y-0.5">
            <dt className="text-muted-foreground">For</dt><dd className="break-all">{pinnable.subject}</dd>
            <dt className="text-muted-foreground">Issued by</dt><dd className="break-all">{pinnable.issuer}</dd>
            <dt className="text-muted-foreground">Valid until</dt><dd>{new Date(pinnable.not_after).toLocaleDateString()}</dd>
          </dl>
          <p className="break-all font-mono text-xs">SHA-256 {pinnable.sha256}</p>
          <Button size="sm" className="self-start" onClick={() => void trust()}>Trust this certificate</Button>
        </div>
      )}
      <FormError message={error} />
    </div>
  );
}

// --- A phone system's login, shown once ----------------------------------------

function LoginBox({ trunk, login, onClose, closeLabel = "I've entered it" }: {
  trunk: Trunk; login: TrunkLogin; onClose: () => void; closeLabel?: string;
}) {
  const [show, setShow] = useState(false);
  const [product, setProduct] = useState<GatewayProduct>("ucm");
  const [status, setStatus] = useState(trunk.status);

  // Live: "Waiting for it to sign in…" turns into "Signed in" as soon as it does.
  useEffect(() => {
    if (status === "registered") return;
    const t = window.setInterval(() => {
      void api.GET("/api/v1/trunks/{id}", { params: { path: { id: trunk.id } } }).then(({ data }) => { if (data) setStatus(data.status); });
    }, 3000);
    return () => window.clearInterval(t);
  }, [trunk.id, status]);

  const copy = (v: string) => void navigator.clipboard?.writeText(v);
  return (
    <div className="flex flex-col gap-3 rounded-md border bg-card p-4">
      <p className="font-medium">Enter this on "{trunk.name}"</p>
      <p className="text-sm text-muted-foreground">On the phone system or gateway, add a SIP trunk that registers (signs in) with:</p>
      <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">Server</dt><dd className="break-all font-mono">{login.server}</dd>
        <dt className="text-muted-foreground">Port</dt><dd className="font-mono">{login.port}</dd>
        <dt className="text-muted-foreground">Transport</dt><dd>TLS</dd>
        <dt className="text-muted-foreground">Username</dt>
        <dd className="flex flex-wrap items-center gap-2">
          <span className="break-all font-mono">{login.username}</span>
          <Button size="sm" variant="outline" onClick={() => copy(login.username)}>Copy</Button>
        </dd>
        <dt className="text-muted-foreground">Password</dt>
        <dd className="flex flex-wrap items-center gap-2">
          <span className="break-all font-mono">{show ? login.password : "••••••••"}</span>
          <Button size="sm" variant="outline" onClick={() => setShow((v) => !v)}>{show ? "Hide" : "Show"}</Button>
          <Button size="sm" variant="outline" onClick={() => copy(login.password)}>Copy</Button>
        </dd>
        <dt className="text-muted-foreground">Audio</dt><dd>Encrypted (SRTP), required</dd>
      </dl>
      <Field label="How to enter this on" htmlFor="gw-product">
        <Select value={product} onValueChange={(v) => setProduct(v as GatewayProduct)}>
          <SelectTrigger id="gw-product" className="w-full sm:w-64"><SelectValue /></SelectTrigger>
          <SelectContent>{GATEWAY_PRODUCTS.map((p) => <SelectItem key={p.id} value={p.id}>{p.label}</SelectItem>)}</SelectContent>
        </Select>
      </Field>
      <ol className="list-decimal space-y-1 ps-5 text-sm text-muted-foreground">
        {gatewaySteps(product, login).map((s, i) => <li key={i}>{s}</li>)}
      </ol>
      <p className="text-sm text-muted-foreground">
        It signs in from your phone networks only. You won't see the password again; lost it? Use "New password".
      </p>
      <p className="flex items-center gap-2 text-sm font-medium" aria-live="polite">
        {status === "registered"
          ? <><Check aria-hidden="true" className="size-4 text-status-available" /> Signed in</>
          : <><Loader2 aria-hidden="true" className="size-4 animate-spin text-muted-foreground" /> Waiting for it to sign in…</>}
      </p>
      <Button onClick={onClose}>{closeLabel}</Button>
    </div>
  );
}

// --- Numbers on a line (§6.2 step 6) --------------------------------------------

type NumberRow = { number: string; rings?: string };

function NumbersEditor({ rows, setRows, extensions, defaultRings }: {
  rows: NumberRow[]; setRows: (r: NumberRow[]) => void; extensions: Extension[]; defaultRings?: string;
}) {
  return (
    <div className="flex flex-col gap-3">
      {rows.map((r, i) => (
        <div key={i} className="flex flex-wrap items-end gap-2">
          <Field label={`Number ${i + 1}`} htmlFor={`line-number-${i}`}>
            <Input id={`line-number-${i}`} className="w-44 font-mono" inputMode="tel" value={r.number} placeholder="+971 4 200 0100"
              onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, number: e.target.value } : x)))} />
          </Field>
          <div className="flex flex-col gap-2">
            <Label htmlFor={`line-rings-${i}`}>Rings</Label>
            <RingsSelect id={`line-rings-${i}`} label={`Number ${i + 1} rings`} value={r.rings} extensions={extensions}
              onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, rings: v } : x)))} />
          </div>
          <Button variant="outline" size="sm" onClick={() => setRows(rows.filter((_, j) => j !== i))}>Remove</Button>
        </div>
      ))}
      <Button variant="outline" size="sm" className="self-start" onClick={() => setRows([...rows, { number: "", rings: rows.at(-1)?.rings ?? defaultRings }])}>
        + Add a number
      </Button>
    </div>
  );
}

const cleanNumber = (n: string) => n.replace(/[\s().-]/g, "");

async function addNumbers(trunkId: string, rows: NumberRow[]): Promise<string> {
  for (const r of rows) {
    if (!cleanNumber(r.number)) continue;
    const { error } = await api.POST("/api/v1/trunks/{id}/dids", {
      params: { path: { id: trunkId } }, body: { number: cleanNumber(r.number), ...(r.rings ? { extension_id: r.rings } : {}) },
    });
    if (error) return `${r.number}: ${problemMessage(error)}`;
  }
  return "";
}

// --- Outgoing use (§6.2 step 7) ---------------------------------------------------

type OutgoingUse = "main" | "backup" | "none";

async function useForOutgoing(trunkId: string, use: OutgoingUse): Promise<string> {
  if (use === "none") return "";
  const { data } = await api.GET("/api/v1/outbound-routing");
  const order = (data?.trunks ?? []).map((t) => t.id).filter((id) => id !== trunkId);
  const next = use === "main" ? [trunkId, ...order] : [...order, trunkId];
  const { error } = await api.PUT("/api/v1/outbound-routing", { body: { order: next } });
  return error ? problemMessage(error) : "";
}

function OutgoingChoice({ value, onChange, hasOther }: { value: OutgoingUse; onChange: (v: OutgoingUse) => void; hasOther: boolean }) {
  return (
    <RadioGroup value={value} onValueChange={(v) => onChange(v as OutgoingUse)}>
      <Choice id="out-main" value="main" title={hasOther ? "Yes, as the main line (the others move down)" : "Yes, as the main line"}
        badge={hasOther ? undefined : "Recommended"} hint="Outside calls go out on it first." />
      {hasOther && <Choice id="out-backup" value="backup" title="As the backup" badge="Recommended"
        hint="Used only when the lines above are down or full." />}
      <Choice id="out-none" value="none" title="Only for incoming calls" />
    </RadioGroup>
  );
}

// --- Add a phone line: guided (§6.2) ---------------------------------------------

type Who = "system" | "company";
type Company = "telnyx" | "voipms" | "twilio" | "generic";
const COMPANIES: { id: Company; label: string; hint: string }[] = [
  { id: "telnyx", label: "Telnyx", hint: "SIP connection credentials from the Telnyx portal." },
  { id: "voipms", label: "VoIP.ms", hint: "A sub account's username and password." },
  { id: "twilio", label: "Twilio", hint: "An Elastic SIP Trunk's credential list." },
  { id: "generic", label: "Another company", hint: "Any company that gives you a SIP server, username and password." },
];

function PasteBox({ onUse }: { onUse: (s: PastedSettings) => void }) {
  const [text, setText] = useState("");
  const parsed = useMemo(() => (text.trim() ? parseProviderText(text) : null), [text]);
  const found = parsed && (parsed.host || parsed.username || parsed.password || parsed.numbers.length > 0);
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor="paste-settings">Or paste what the company sent you</Label>
      <textarea id="paste-settings" rows={5} value={text} onChange={(e) => setText(e.target.value)}
        placeholder={"SIP server: sip.example.com\nUsername: …\nPassword: …"}
        className="w-full min-w-0 rounded-md border bg-background px-3 py-2 font-mono text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50" />
      <p className="text-sm text-muted-foreground">Read here in your browser only; nothing is sent anywhere until you save.</p>
      {parsed && (
        found ? (
          <div className="flex flex-col gap-2 rounded-md border bg-card p-3 text-sm">
            <p className="font-medium">Linx understood:</p>
            <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-y-1">
              {parsed.host && <><dt className="text-muted-foreground">Server</dt><dd className="break-all font-mono">{parsed.host}{parsed.port ? `:${parsed.port}` : ""}</dd></>}
              {parsed.username && <><dt className="text-muted-foreground">Username</dt><dd className="break-all font-mono">{parsed.username}</dd></>}
              {parsed.password && <><dt className="text-muted-foreground">Password</dt><dd>••••••••</dd></>}
              {parsed.numbers.length > 0 && <><dt className="text-muted-foreground">Numbers</dt><dd className="font-mono">{parsed.numbers.join(", ")}</dd></>}
            </dl>
            <Button size="sm" className="self-start" onClick={() => onUse(parsed)}>Use these</Button>
          </div>
        ) : <p className="text-sm text-muted-foreground">Linx didn't find settings in that. Fill in the fields instead.</p>
      )}
    </div>
  );
}

function GuidedAddLine({ me, open, onOpenChange, extensions, trunks, onDone }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; extensions: Extension[]; trunks: Trunk[]; onDone: () => void;
}) {
  const myExt = extensions.find((e) => e.number === me.extension)?.id;
  const [step, setStep] = useState(1);
  const [who, setWho] = useState<Who>("system");
  const [company, setCompany] = useState<Company>("telnyx");
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState<number | undefined>();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [rows, setRows] = useState<NumberRow[]>([]);
  const [otherRings, setOtherRings] = useState<string | undefined>();
  const [use, setUse] = useState<OutgoingUse>("main");
  const [trunk, setTrunk] = useState<Trunk | null>(null);
  const [login, setLogin] = useState<TrunkLogin | null>(null);
  const [tested, setTested] = useState<TrunkTest | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const hasOther = trunks.some((t) => t.outbound_priority !== undefined);

  useEffect(() => {
    if (!open) return;
    setStep(1); setWho("system"); setCompany("telnyx"); setName(""); setHost(""); setPort(undefined); setUsername(""); setPassword("");
    setRows([]); setOtherRings(myExt); setUse(hasOther ? "backup" : "main"); setTrunk(null); setLogin(null); setTested(null); setError("");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const system = who === "system";
  // Steps: 1 Who · 2 Name (system) / Company (company) · 3 Details (company) · 4 Test (company) · 5 Numbers · 6 Outgoing · 7 Done
  const titles = system ? ["Who", "Name", "Numbers", "Outgoing", "Sign-in"] : ["Who", "Company", "Details", "Test", "Numbers", "Outgoing", "Done"];
  const last = titles.length;

  const usePasted = (s: PastedSettings) => {
    if (s.host) setHost(s.host);
    if (s.port && s.transport !== "udp" && s.transport !== "tcp") setPort(s.port);
    if (s.username) setUsername(s.username);
    if (s.password) setPassword(s.password);
    if (s.numbers.length) setRows(s.numbers.map((n) => ({ number: n, rings: myExt })));
    setStep(3);
  };

  // A phone system: saved at the end, when its login is shown.
  const createSystem = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/trunks", {
      body: { name: name.trim(), kind: "registers_here", template: "phone_system", ...(otherRings ? { rings_extension_id: otherRings } : {}) },
    });
    if (needsConfirm(err)) { setBusy(false); return { confirm: true }; }
    if (!data) { setBusy(false); setError(problemMessage(err)); return { confirm: false }; }
    const problem = (await addNumbers(data.id, rows)) || (await useForOutgoing(data.id, use));
    setBusy(false);
    setTrunk(data);
    setLogin(data.login ?? null);
    if (problem) setError(problem);
    setStep(last);
    return { confirm: false };
  });

  // A phone company: saved turned off, tested, turned on once it works.
  const saveCompany = async () => {
    setBusy(true);
    setError("");
    const fields = { host: host.trim(), username: username.trim(), ...(password ? { password } : {}), ...(port ? { port } : {}) };
    const res = trunk
      ? await api.PATCH("/api/v1/trunks/{id}", { params: { path: { id: trunk.id }, header: { "If-Match": trunk.etag } }, body: fields })
      : await api.POST("/api/v1/trunks", {
        body: { name: name.trim() || COMPANIES.find((c) => c.id === company)!.label, kind: "registration", template: company, enabled: false, ...fields },
      });
    setBusy(false);
    if (!res.data) { setError(problemMessage(res.error)); return; }
    setTrunk(res.data);
    setTested(null);
    setStep(4);
  };

  const onTestResult = useCallback(async (r: TrunkTest) => {
    setTested(r);
    if (r.ok && trunk && !trunk.enabled) {
      const { data } = await api.PATCH("/api/v1/trunks/{id}", { params: { path: { id: trunk.id } }, body: { enabled: true } });
      if (data) setTrunk(data);
    }
  }, [trunk]);

  const finishCompany = async () => {
    if (!trunk) return;
    setBusy(true);
    const problem = (await addNumbers(trunk.id, rows)) || (trunk.enabled ? await useForOutgoing(trunk.id, use) : "");
    setBusy(false);
    if (problem) { setError(problem); return; }
    setStep(last);
  };

  const discard = () => {
    if (step > 1 && step < last && !trunk && !window.confirm("Discard this?")) return;
    onOpenChange(false);
    if (trunk) onDone();
  };

  const next = () => {
    setError("");
    if (system && step === 4) { void createSystem(); return; }
    if (!system && step === 3) { void saveCompany(); return; }
    if (!system && step === 6) { void finishCompany(); return; }
    setStep(step + 1);
  };
  const canNext = !busy && (
    (step === 2 && system && !!name.trim())
    || (step === 2 && !system)
    || (step === 3 && !system && !!host.trim() && !!username.trim() && (!!password || !!trunk))
    || (step === 4 && !system && !!tested)
    || (step === 3 && system) || (step === 4 && system)
    || (step === 5 && !system) || (step === 6 && !system)
    || step === 1
  );

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) discard(); else onOpenChange(o); }}>
      <SheetContent className="w-full sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>Add a phone line</SheetTitle>
          <SheetDescription>{titles.map((t, i) => `${i + 1} ${t}`).join(" · ")}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4">
          {step === 1 && (
            <RadioGroup value={who} onValueChange={(v) => setWho(v as Who)}>
              <Choice id="who-system" value="system" title="Another phone system or gateway" badge="Recommended"
                hint="Like a Grandstream UCM, GXW or HT, Yeastar or FreePBX, with your landlines. It signs in to Linx, like a desk phone." />
              <Choice id="who-company" value="company" title="An internet phone company"
                hint="Telnyx, VoIP.ms, Twilio or another company that gives you phone numbers." />
            </RadioGroup>
          )}
          {step === 1 && (
            <p className="text-sm text-muted-foreground">
              A phone system that can't sign in anywhere, or another Linx server, is added with{" "}
              <span className="font-mono">sudo linx trunk add</span> on the server.
            </p>
          )}

          {system && step === 2 && (
            <Field label="Name" htmlFor="line-name" hint="What you'll see in lists, e.g. where the landlines are.">
              <Input id="line-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="UCM landlines" autoFocus />
            </Field>
          )}

          {!system && step === 2 && (
            <>
              <RadioGroup value={company} onValueChange={(v) => setCompany(v as Company)}>
                {COMPANIES.map((c) => <Choice key={c.id} id={`company-${c.id}`} value={c.id} title={c.label} hint={c.hint} />)}
              </RadioGroup>
              <PasteBox onUse={usePasted} />
            </>
          )}

          {!system && step === 3 && (
            <>
              <Field label="Name" htmlFor="line-name">
                <Input id="line-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={COMPANIES.find((c) => c.id === company)!.label} />
              </Field>
              <Field label="Server" htmlFor="line-host" hint="Its SIP server or registrar, e.g. sip.telnyx.com.">
                <Input id="line-host" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)} autoFocus />
              </Field>
              <Field label="Username" htmlFor="line-username">
                <Input id="line-username" className="font-mono" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
              </Field>
              <Field label="Password" htmlFor="line-password" hint={trunk ? "Leave empty to keep the one you gave." : undefined}>
                <Input id="line-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
              </Field>
              <p className="text-sm text-muted-foreground">Linx connects with encryption (TLS, port {port ?? 5061}). Next, it tests the line before turning it on.</p>
            </>
          )}

          {!system && step === 4 && trunk && (
            <>
              <TestPanel trunk={trunk} onTrunk={setTrunk} onResult={(r) => void onTestResult(r)} />
              {tested && !tested.ok && (
                <p className="text-sm text-muted-foreground">
                  Go back to change the details, or continue: the line is saved turned off until a test passes.
                </p>
              )}
            </>
          )}

          {((system && step === 3) || (!system && step === 5)) && (
            <>
              <p className="text-sm">Which phone numbers come with this line?</p>
              <NumbersEditor rows={rows} setRows={setRows} extensions={extensions} defaultRings={myExt} />
              {system && (
                <div className="flex flex-col gap-2">
                  <Label htmlFor="line-other-rings">Calls for any other number ring</Label>
                  <RingsSelect id="line-other-rings" label="Calls for any other number ring" value={otherRings} extensions={extensions} onChange={setOtherRings} />
                  <p className="text-sm text-muted-foreground">A landline often sends no number: its calls ring here.</p>
                </div>
              )}
            </>
          )}

          {((system && step === 4) || (!system && step === 6)) && (
            <>
              <p className="text-sm">Use it for outgoing calls?</p>
              <OutgoingChoice value={use} onChange={setUse} hasOther={hasOther} />
              {!system && trunk && !trunk.enabled && (
                <p className="text-sm text-muted-foreground">It's turned off until its test passes; choose this again from Outgoing calls then.</p>
              )}
            </>
          )}

          {step === last && trunk && (
            system && login ? (
              <LoginBox trunk={trunk} login={login} closeLabel="Done" onClose={() => { onOpenChange(false); onDone(); }} />
            ) : (
              <div className="flex flex-col gap-2 text-sm">
                <p className="font-medium">"{trunk.name}" is {trunk.enabled ? "ready" : "saved, turned off"}.</p>
                {trunk.enabled
                  ? <p className="text-muted-foreground">Make a test call from the Dialer to a mobile number.</p>
                  : <p className="text-muted-foreground">Open it from the list to test it again once the details are fixed.</p>}
              </div>
            )
          )}
          <FormError message={error} />
        </div>
        {step < last && (
          <div className="mt-auto flex items-center justify-between gap-2 border-t p-4">
            <Button variant="outline" disabled={step === 1 || busy || (system && step > 4) || (!system && step > 4 && !!trunk)}
              onClick={() => setStep(step - 1)}>Back</Button>
            <Button disabled={!canNext} aria-busy={busy} onClick={next}>
              {system && step === 4 ? "Create" : !system && step === 3 ? "Save and test" : !system && step === 6 ? "Finish" : "Next"}
            </Button>
          </div>
        )}
        {step === last && !(system && login) && (
          <div className="mt-auto flex justify-end border-t p-4">
            <Button onClick={() => { onOpenChange(false); onDone(); }}>Back to lines</Button>
          </div>
        )}
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

// --- Quick add (§6.3) ---------------------------------------------------------------

function QuickAddLine({ me, open, onOpenChange, onGuideInstead, always, onDone }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; onGuideInstead: () => void; always: boolean; onDone: () => void;
}) {
  const [who, setWho] = useState<"system" | Company>("system");
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [trunk, setTrunk] = useState<Trunk | null>(null);
  const [login, setLogin] = useState<TrunkLogin | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);

  useEffect(() => {
    if (open) { setWho("system"); setName(""); setHost(""); setUsername(""); setPassword(""); setTrunk(null); setLogin(null); setError(""); }
  }, [open]);

  const submit = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const body = who === "system"
      ? { name: name.trim(), kind: "registers_here" as const, template: "phone_system" }
      : { name: name.trim(), kind: "registration" as const, template: who, host: host.trim(), username: username.trim(), password, enabled: false };
    const { data, error: err } = await api.POST("/api/v1/trunks", { body });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setTrunk(data);
    setLogin(data.login ?? null);
    return { confirm: false };
  });

  const close = () => { onOpenChange(false); if (trunk) onDone(); };
  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader><DialogTitle>Add a phone line</DialogTitle></DialogHeader>
        {always && !trunk && (
          <button type="button" className="-mt-2 self-start text-sm text-link underline-offset-4 hover:underline" onClick={() => { onOpenChange(false); onGuideInstead(); }}>
            Guide me instead
          </button>
        )}
        {!trunk && (
          <div className="flex flex-col gap-4">
            <Field label="What is it?" htmlFor="quick-line-who">
              <Select value={who} onValueChange={(v) => setWho(v as typeof who)}>
                <SelectTrigger id="quick-line-who"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="system">A phone system or gateway (signs in to Linx)</SelectItem>
                  {COMPANIES.map((c) => <SelectItem key={c.id} value={c.id}>{c.label}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Name" htmlFor="quick-line-name"><Input id="quick-line-name" value={name} onChange={(e) => setName(e.target.value)} /></Field>
            {who !== "system" && (
              <>
                <Field label="Server" htmlFor="quick-line-host"><Input id="quick-line-host" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)} /></Field>
                <Field label="Username" htmlFor="quick-line-username"><Input id="quick-line-username" className="font-mono" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" /></Field>
                <Field label="Password" htmlFor="quick-line-password"><Input id="quick-line-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" /></Field>
              </>
            )}
            <FormError message={error} />
            <DialogFooter>
              <Button disabled={busy || !name.trim() || (who !== "system" && (!host.trim() || !username.trim() || !password))} aria-busy={busy}
                onClick={() => void submit()}>
                {who === "system" ? "Add" : "Add and test"}
              </Button>
            </DialogFooter>
          </div>
        )}
        {trunk && login && <LoginBox trunk={trunk} login={login} closeLabel="Done" onClose={close} />}
        {trunk && !login && (
          <div className="flex flex-col gap-3">
            <TestPanel trunk={trunk} onTrunk={setTrunk} onResult={(r) => {
              if (r.ok && !trunk.enabled) {
                void api.PATCH("/api/v1/trunks/{id}", { params: { path: { id: trunk.id } }, body: { enabled: true } })
                  .then(({ data }) => { if (data) setTrunk(data); });
              }
            }} />
            <p className="text-sm text-muted-foreground">
              {trunk.enabled ? "It's on. Add its numbers from its page." : "Saved turned off until a test passes (save anyway, fix later)."}
            </p>
            <DialogFooter><Button onClick={close}>Done</Button></DialogFooter>
          </div>
        )}
        {confirm.dialog}
      </DialogContent>
    </Dialog>
  );
}

// --- Line detail (§6.4) --------------------------------------------------------------

function LineSheet({ me, trunk: initial, dids, extensions, readOnly, onClose, onChanged }: {
  me: Me; trunk: Trunk; dids: Did[]; extensions: Extension[]; readOnly: boolean; onClose: () => void; onChanged: () => void;
}) {
  const [trunk, setTrunk] = useState(initial);
  const [numbers, setNumbers] = useState(dids);
  const [testing, setTesting] = useState(false);
  const [login, setLogin] = useState<TrunkLogin | null>(null);
  const [askNewPassword, setAskNewPassword] = useState(false);
  const [editing, setEditing] = useState(false);
  const [host, setHost] = useState(initial.host);
  const [username, setUsername] = useState(initial.username ?? "");
  const [password, setPassword] = useState("");
  const [maxCalls, setMaxCalls] = useState(String(initial.max_calls));
  const [callerId, setCallerId] = useState(initial.caller_id_number ?? "");
  const [newNumber, setNewNumber] = useState<NumberRow>({ number: "" });
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const system = trunk.kind === "registers_here";

  useEffect(() => { setTrunk(initial); }, [initial]);
  useEffect(() => { setNumbers(dids); }, [dids]);
  const changed = (t: Trunk) => { setTrunk(t); onChanged(); };

  const patch = async (body: components["schemas"]["TrunkPatch"]) => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/trunks/{id}", { params: { path: { id: trunk.id }, header: { "If-Match": trunk.etag } }, body });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return false; }
    changed(data);
    return true;
  };

  const newPassword = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/trunks/{id}/reset-password", { params: { path: { id: trunk.id } } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setAskNewPassword(false);
    setTrunk(data);
    setLogin(data.login ?? null);
    return { confirm: false };
  });

  const saveConnection = async () => {
    const body: components["schemas"]["TrunkPatch"] = {};
    if (!system) {
      if (host.trim() !== trunk.host) body.host = host.trim();
      if (username.trim() !== (trunk.username ?? "")) body.username = username.trim();
      if (password) body.password = password;
    }
    const mc = Number(maxCalls);
    if (mc !== trunk.max_calls) body.max_calls = mc;
    if (cleanNumber(callerId) !== (trunk.caller_id_number ?? "")) body.caller_id_number = cleanNumber(callerId);
    if (await patch(body)) { setEditing(false); setPassword(""); }
  };

  const setRings = async (d: Did, ext: string | undefined) => {
    const { data, error: err } = await api.PATCH("/api/v1/dids/{id}", {
      params: { path: { id: d.id }, header: { "If-Match": d.etag } }, body: { extension_id: ext ?? "" },
    });
    if (!data) { setError(problemMessage(err)); return; }
    setNumbers((list) => list.map((x) => (x.id === d.id ? data : x)));
    onChanged();
  };
  const removeNumber = async (d: Did) => {
    const { response, error: err } = await api.DELETE("/api/v1/dids/{id}", { params: { path: { id: d.id } } });
    if (!response.ok) { setError(problemMessage(err)); return; }
    setNumbers((list) => list.filter((x) => x.id !== d.id));
    onChanged();
  };
  const addNumber = async () => {
    const problem = await addNumbers(trunk.id, [newNumber]);
    if (problem) { setError(problem); return; }
    setNewNumber({ number: "" });
    const { data } = await api.GET("/api/v1/trunks/{id}/dids", { params: { path: { id: trunk.id } } });
    if (data) setNumbers(data.items);
    onChanged();
  };

  const remove = async () => {
    setBusy(true);
    const { response, error: err } = await api.DELETE("/api/v1/trunks/{id}", { params: { path: { id: trunk.id } } });
    setBusy(false);
    setConfirmRemove(false);
    if (!response.ok) { setError(problemMessage(err)); return; }
    onChanged();
    onClose();
  };

  const sec = securityWords(trunk, true);
  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{trunk.name}</SheetTitle>
          <SheetDescription>{kindWords[trunk.kind] ?? trunk.kind} · {sec.text}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 overflow-y-auto px-4 pb-4">
          <section className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">Status</h3>
              <Button size="sm" variant="outline" onClick={() => setTesting(true)}>Test again</Button>
            </div>
            <div className="text-sm"><StatusLine t={trunk} /></div>
            {trunk.status_detail && <p className="text-sm text-muted-foreground">{trunk.status_detail}</p>}
            {testing && <TestPanel trunk={trunk} onTrunk={changed} readOnly={readOnly} />}
          </section>

          {system ? (
            <section className="flex flex-col gap-2">
              <h3 className="text-sm font-semibold">Sign-in</h3>
              <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-y-1 text-sm">
                <dt className="text-muted-foreground">Username</dt><dd className="break-all font-mono">{trunk.username}</dd>
                <dt className="text-muted-foreground">Password</dt><dd>Shown once when it was made</dd>
              </dl>
              {!readOnly && <Button size="sm" variant="outline" className="self-start" onClick={() => setAskNewPassword(true)}>New password</Button>}
            </section>
          ) : null}

          <section className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">{system ? "Settings" : "Connection"}</h3>
              {!readOnly && !editing && <Button size="sm" variant="outline" onClick={() => setEditing(true)}>Edit</Button>}
            </div>
            {!editing ? (
              <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-y-1 text-sm">
                {!system && <><dt className="text-muted-foreground">Server</dt><dd className="break-all font-mono">{trunk.host}:{trunk.port}</dd></>}
                {!system && trunk.username && <><dt className="text-muted-foreground">Username</dt><dd className="break-all font-mono">{trunk.username}</dd></>}
                <dt className="text-muted-foreground">Calls at once</dt><dd>{trunk.max_calls}</dd>
                <dt className="text-muted-foreground">Caller ID</dt>
                <dd>{trunk.caller_id_number ? <span className="font-mono">{trunk.caller_id_number}</span> : "The company's default"}</dd>
              </dl>
            ) : (
              <div className="flex flex-col gap-3">
                {!system && (
                  <>
                    <Field label="Server" htmlFor="edit-line-host"><Input id="edit-line-host" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)} /></Field>
                    <Field label="Username" htmlFor="edit-line-username"><Input id="edit-line-username" className="font-mono" value={username} onChange={(e) => setUsername(e.target.value)} /></Field>
                    <Field label="New password" htmlFor="edit-line-password" hint="Leave empty to keep it.">
                      <Input id="edit-line-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
                    </Field>
                  </>
                )}
                <Field label="Calls at once" htmlFor="edit-line-max"><Input id="edit-line-max" className="w-24" inputMode="numeric" value={maxCalls} onChange={(e) => setMaxCalls(e.target.value)} /></Field>
                <Field label="Caller ID shown to others" htmlFor="edit-line-cid" hint="Empty: the phone company's default. Each number's own is used when a person has one.">
                  <Input id="edit-line-cid" className="w-52 font-mono" value={callerId} onChange={(e) => setCallerId(e.target.value)} />
                </Field>
                <div className="flex gap-2">
                  <Button size="sm" disabled={busy} onClick={() => void saveConnection()}>Save</Button>
                  <Button size="sm" variant="outline" onClick={() => setEditing(false)}>Cancel</Button>
                </div>
              </div>
            )}
          </section>

          <section className="flex flex-col gap-2">
            <h3 className="text-sm font-semibold">Numbers</h3>
            {numbers.length === 0 && <p className="text-sm text-muted-foreground">No numbers on this line yet.</p>}
            {numbers.map((d) => (
              <div key={d.id} className="flex flex-wrap items-center gap-2 rounded-md border p-2.5 text-sm">
                <span className="min-w-0 flex-1 break-all font-mono">{d.number}</span>
                {d.ring_group_id ? <InIncoming what="Rings a ring group" /> : (
                  <RingsSelect id={`rings-${d.id}`} label={`${d.number} rings`} value={d.extension_id} extensions={extensions}
                    disabled={readOnly} onChange={(v) => void setRings(d, v)} />
                )}
                {!readOnly && <Button size="sm" variant="outline" onClick={() => void removeNumber(d)}>Remove</Button>}
              </div>
            ))}
            {!readOnly && (
              <div className="flex flex-wrap items-end gap-2">
                <Field label="Add a number" htmlFor="add-line-number">
                  <Input id="add-line-number" className="w-44 font-mono" inputMode="tel" value={newNumber.number}
                    onChange={(e) => setNewNumber({ ...newNumber, number: e.target.value })} />
                </Field>
                <RingsSelect id="add-line-number-rings" label="The new number rings" value={newNumber.rings} extensions={extensions}
                  onChange={(v) => setNewNumber({ ...newNumber, rings: v })} />
                <Button size="sm" disabled={!cleanNumber(newNumber.number)} onClick={() => void addNumber()}>Add</Button>
              </div>
            )}
            <div className="mt-2 flex flex-col gap-2">
              <Label htmlFor="line-other">Calls for any other number ring</Label>
              {trunk.rings_ring_group_id ? <InIncoming what="A ring group" /> : (
                <RingsSelect id="line-other" label="Calls for any other number ring" value={trunk.rings_extension_id} extensions={extensions}
                  disabled={readOnly || busy} onChange={(v) => void patch({ rings_extension_id: v ?? "" })} />
              )}
              <p className="text-sm text-muted-foreground">For calls that come with none of these numbers, like a landline that sends none.</p>
            </div>
          </section>

          <FormError message={error} />

          {!readOnly && (
            <section className="rounded-md border border-destructive/30 p-3">
              <h3 className="text-sm font-semibold text-destructive">Danger zone</h3>
              <div className="mt-2 flex flex-wrap gap-2">
                <Button size="sm" variant="outline" disabled={busy} onClick={() => void patch({ enabled: !trunk.enabled })}>
                  {trunk.enabled ? "Turn off" : "Turn on"}
                </Button>
                <Button size="sm" variant="outline" onClick={() => setConfirmRemove(true)}>Remove</Button>
              </div>
            </section>
          )}
        </div>

        <Dialog open={askNewPassword} onOpenChange={setAskNewPassword}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>New password for "{trunk.name}"?</DialogTitle>
              <DialogDescription>It stops working until you enter the new password on it.</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setAskNewPassword(false)}>Cancel</Button>
              <Button disabled={busy} aria-busy={busy} onClick={() => void newPassword()}>New password</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        <Dialog open={!!login} onOpenChange={(o) => { if (!o) setLogin(null); }}>
          <DialogContent className="max-h-[90dvh] overflow-y-auto">
            <DialogHeader><DialogTitle>New sign-in for "{trunk.name}"</DialogTitle></DialogHeader>
            {login && <LoginBox trunk={trunk} login={login} onClose={() => setLogin(null)} />}
          </DialogContent>
        </Dialog>
        <Dialog open={confirmRemove} onOpenChange={setConfirmRemove}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Remove "{trunk.name}"?</DialogTitle>
              <DialogDescription>
                {numbers.length > 0 ? `Its ${numbers.length} number${numbers.length === 1 ? "" : "s"} stop ringing. ` : ""}
                {trunk.outbound_priority !== undefined ? "Outside calls use the other lines. " : ""}This can't be undone.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirmRemove(false)}>Cancel</Button>
              <Button variant="destructive" disabled={busy} onClick={() => void remove()}>Remove</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

// --- The list (§6.1) -----------------------------------------------------------------

export function PhoneLinesScreen({ me }: { me: Me }) {
  const [trunks, setTrunks] = useState<Trunk[] | null>(null);
  const [dids, setDids] = useState<Did[]>([]);
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [query, setQuery] = useState("");
  const [chooserOpen, setChooserOpen] = useState(false);
  const [guidedOpen, setGuidedOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [always, setAlways] = useAlwaysQuickAdd("lines");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const readOnly = isReadOnlyAdmin(me);

  const load = useCallback(async () => {
    const [t, d, e] = await Promise.all([
      api.GET("/api/v1/trunks", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/inbound-routes", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }),
    ]);
    if (t.data) setTrunks(t.data.items);
    if (d.data) setDids(d.data.items);
    if (e.data) setExtensions(e.data.items);
  }, []);
  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 15_000);
    return () => window.clearInterval(t);
  }, [load]);

  const numbersOf = (id: string) => dids.filter((d) => d.trunk_id === id);
  const q = query.trim().toLowerCase();
  const rows = useMemo(() => (trunks ?? [])
    .filter((t) => !q || t.name.toLowerCase().includes(q) || numbersOf(t.id).some((d) => d.number.includes(q)))
    .sort((a, b) => (a.outbound_priority ?? 999) - (b.outbound_priority ?? 999) || a.name.localeCompare(b.name)),
  // eslint-disable-next-line react-hooks/exhaustive-deps
  [trunks, dids, q]);

  const columns: ColumnDef<Trunk>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-medium">{row.original.name}</span> },
    { id: "status", header: "Status", accessorFn: (t) => t.status, cell: ({ row }) => <StatusLine t={row.original} /> },
    { id: "numbers", header: "Numbers", accessorFn: (t) => numbersOf(t.id).length },
    { id: "order", header: "Order", meta: { wide: true }, accessorFn: (t) => t.outbound_priority ?? 999,
      cell: ({ row }) => (row.original.outbound_priority ? ordinal(row.original.outbound_priority) : <span className="text-muted-foreground">—</span>) },
    { id: "security", header: "Security", meta: { wide: true }, accessorFn: (t) => securityWords(t, true).text,
      cell: ({ row }) => {
        const s = securityWords(row.original, true);
        return <span className={cn(s.warn && "flex items-center gap-1.5 text-status-away")}>{s.warn && <CircleAlert aria-hidden="true" className="size-4" />}{s.text}</span>;
      } },
  ];

  if (trunks === null) return <div className="p-6" aria-busy="true" />;
  const selected = trunks.find((t) => t.id === selectedId);

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-semibold tracking-tight">Phone lines</h1>
          <p className="mt-1 text-sm text-muted-foreground">{trunks.length} line{trunks.length === 1 ? "" : "s"}</p>
        </div>
        {!readOnly && <Button onClick={() => (always ? setQuickOpen(true) : setChooserOpen(true))}>+ Add</Button>}
      </div>

      {trunks.length > 0 && (
        <div className="relative mt-4 w-full max-w-xs">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-9" aria-label="Search phone lines" />
        </div>
      )}

      <div className="mt-4">
        {trunks.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
            <p className="max-w-prose text-sm text-muted-foreground">
              A phone line connects Linx to the phone network, so you can call mobiles and landlines and be called on your number. Linx works between extensions without one.
            </p>
            {!readOnly && (
              <div className="flex gap-2">
                <Button variant="outline" onClick={() => setGuidedOpen(true)}>Guide me</Button>
                <Button onClick={() => setQuickOpen(true)}>Quick add</Button>
              </div>
            )}
          </div>
        ) : (
          <DataTable columns={columns} data={rows} onRowClick={(t) => setSelectedId(t.id)}
            emptyState={<p className="p-6 text-center text-sm text-muted-foreground">No lines match.</p>} />
        )}
      </div>
      {trunks.length > 0 && (
        <p className="mt-4 text-sm text-muted-foreground">
          Which line is tried first: <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/outgoing")}>Outgoing calls</button>.
          {" "}Where numbers ring: <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/incoming")}>Incoming calls</button>.
        </p>
      )}

      <AddChooserDialog open={chooserOpen} onOpenChange={setChooserOpen} title="Add a phone line"
        guideHint="Pick what it is, its numbers and whether it's for outgoing calls, step by step." quickHint="A name (and a login, for a phone company). Test and numbers after."
        always={always} onAlwaysChange={setAlways} onGuide={() => setGuidedOpen(true)} onQuick={() => setQuickOpen(true)} />
      <GuidedAddLine me={me} open={guidedOpen} onOpenChange={setGuidedOpen} extensions={extensions} trunks={trunks} onDone={() => void load()} />
      <QuickAddLine me={me} open={quickOpen} onOpenChange={setQuickOpen} always={always} onGuideInstead={() => setGuidedOpen(true)} onDone={() => void load()} />
      {selected && (
        <LineSheet me={me} trunk={selected} dids={numbersOf(selected.id)} extensions={extensions} readOnly={readOnly}
          onClose={() => setSelectedId(null)} onChanged={() => void load()} />
      )}
    </div>
  );
}

