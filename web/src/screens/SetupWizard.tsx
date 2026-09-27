// The first-run setup wizard (docs/ADMIN.md §4, docs/ui/ADMIN_SCREENS_PHASE1E.md
// §3.2): a full-page, resumable, one-question-per-step flow. Each step saves
// to the real resource it fills in (settings, people, the "Everyone" calling
// level) and moves `PUT /setup`'s step pointer on, so leaving and coming
// back resumes at the right place.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Check, Phone, X } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { Wordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { avoidedRange, defaultRanges, pad } from "@/lib/numbering";
import { usePhoneLine, usePhoneState } from "@/phone/context";
import { ECHO_TEST } from "@/phone/line";
import { CallPanel } from "./CallPanel";
import { RestoreFromBackup, StartChoice, type RestoreStatus } from "./SetupRestore";

type Role = components["schemas"]["Role"];
type NumberCategory = components["schemas"]["NumberCategory"];
type User = components["schemas"]["User"];

const STEP_LABELS = ["Place", "Country", "Numbers", "People", "Line", "Calls", "Test"];
const TOTAL_STEPS = STEP_LABELS.length;

function Progress({ step }: { step: number }) {
  return (
    <ol className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm" aria-label="Setup steps">
      {STEP_LABELS.map((label, i) => {
        const n = i + 1;
        const state = n < step ? "done" : n === step ? "current" : "later";
        return (
          <li key={label} className="flex items-center gap-2">
            {i > 0 && <span aria-hidden="true" className="text-muted-foreground/50">─</span>}
            <span className={
              state === "current" ? "font-medium text-foreground"
                : state === "done" ? "text-muted-foreground"
                  : "text-muted-foreground/50"
            }>
              {state === "done" ? <Check aria-hidden="true" className="inline size-3.5 align-[-2px]" /> : n}. {label}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

function Recommended({ children }: { children: ReactNode }) {
  return <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">{children}</span>;
}

function FormError({ message }: { message: string }) {
  if (!message) return null;
  return <p role="alert" className="text-sm font-medium text-destructive">{message}</p>;
}

/** The page chrome: progress, question heading, Back/Skip/Next. */
function StepShell({ step, title, lead, canSkip, nextLabel, nextDisabled, busy, error, onBack, onNext, onSkip, onFinishLater, children }: {
  step: number; title: string; lead?: string; canSkip: boolean; nextLabel?: string; nextDisabled?: boolean; busy: boolean;
  error?: string; onBack?: () => void; onNext: () => void; onSkip?: () => void; onFinishLater: () => void; children: ReactNode;
}) {
  return (
    <main className="flex min-h-dvh flex-col bg-background">
      <header className="flex items-center justify-between gap-4 border-b px-4 py-4 md:px-8">
        <div className="flex items-center gap-6">
          <Wordmark className="text-2xl" />
          <h1 className="hidden font-display text-lg font-semibold sm:block">Set up Linx</h1>
        </div>
        <button type="button" className="text-sm text-link underline-offset-4 hover:underline" onClick={onFinishLater}>
          Finish later →
        </button>
      </header>
      <div className="border-b bg-card px-4 py-3 md:px-8">
        <Progress step={step} />
      </div>
      <div className="flex flex-1 items-start justify-center overflow-y-auto px-4 py-10 md:px-8">
        <div className="w-full max-w-xl">
          <h2 className="font-display text-2xl font-semibold tracking-tight">{title}</h2>
          {lead && <p className="mt-2 text-sm text-muted-foreground">{lead}</p>}
          <div className="mt-6">{children}</div>
          <FormError message={error ?? ""} />
        </div>
      </div>
      <footer className="flex items-center justify-between gap-3 border-t px-4 py-4 md:px-8">
        <Button type="button" variant="outline" onClick={onBack} disabled={!onBack || busy}>Back</Button>
        <div className="flex gap-2">
          {canSkip && <Button type="button" variant="outline" onClick={onSkip} disabled={busy}>Skip for now</Button>}
          <Button type="button" onClick={onNext} disabled={busy || nextDisabled} aria-busy={busy}>{nextLabel ?? "Next"}</Button>
        </div>
      </footer>
    </main>
  );
}

// --- Step 1: Place ---

function PlaceStep(props: { value: "" | "home" | "business"; onChange: (v: "home" | "business") => void } & StepProps) {
  const { value, onChange, ...shell } = props;
  return (
    <StepShell {...shell} title="Where will you use Linx?" nextDisabled={!value}>
      <div className="grid gap-3 sm:grid-cols-2">
        {(["home", "business"] as const).map((k) => (
          <button key={k} type="button" onClick={() => onChange(k)}
            className={`rounded-lg border p-5 text-start transition-colors ${value === k ? "border-primary ring-1 ring-primary" : "hover:bg-card"}`}>
            <p className="font-display text-lg font-semibold">{k === "home" ? "Home" : "Business"}</p>
            <p className="mt-1 text-sm text-muted-foreground">{k === "home" ? "Family and a few phones." : "Staff, a reception, office hours."}</p>
          </button>
        ))}
      </div>
      <p className="mt-4 text-sm text-muted-foreground">This only changes examples and suggestions. Everything works the same.</p>
    </StepShell>
  );
}

// --- Step 2: Country ---

function CountryStep(props: StepProps) {
  return (
    <StepShell {...props} title="Which country are your phone lines in?">
      <Label htmlFor="country">Country</Label>
      <Select value="AE" disabled>
        <SelectTrigger id="country" className="mt-2 w-full"><SelectValue /></SelectTrigger>
        <SelectContent><SelectItem value="AE">United Arab Emirates</SelectItem></SelectContent>
      </Select>
      <p className="mt-2 text-sm text-muted-foreground">More countries coming.</p>
      <p className="mt-4 text-sm text-muted-foreground">Linx uses this to recognise mobile, local and emergency numbers.</p>
      <p className="mt-2 text-sm text-muted-foreground">Emergency numbers 999, 998, 997, 112 and 901 always work, from every phone.</p>
    </StepShell>
  );
}

// --- Step 3: Numbers ---

function NumberBar({ digits, ranges }: { digits: number; ranges: ReturnType<typeof defaultRanges> }) {
  const base = 10 ** (digits - 1);
  const total = 9 * base;
  const avoided = avoidedRange(digits)!;
  const seg = (r: { from: number; to: number } | null, label: string, className: string) => {
    if (!r) return null;
    const width = ((r.to - r.from + 1) / total) * 100;
    return (
      <div key={label} style={{ width: `${width}%` }} className={`flex flex-col items-center justify-center border-e p-1.5 text-center last:border-e-0 ${className}`}>
        <span className="text-xs font-medium">{r.from}–{r.to}</span>
        <span className="text-xs text-muted-foreground">{label}</span>
      </div>
    );
  };
  return (
    <div className="flex overflow-hidden rounded-md border text-foreground">
      {seg(ranges.people, "People", "bg-primary/10")}
      {seg(ranges.groups, "Groups (later)", "bg-muted")}
      {seg(ranges.reserved, "Kept free", "bg-muted/60")}
      {seg(avoided, "Avoided", "bg-destructive/10")}
    </div>
  );
}

function RangeFields({ label, range, onChange }: { label: string; range: { from: number; to: number } | null; onChange: (r: { from: number; to: number } | null) => void }) {
  return (
    <div className="grid grid-cols-[auto_1fr_1fr] items-center gap-2">
      <span className="text-sm">{label}</span>
      <Input type="number" aria-label={`${label} from`} value={range?.from ?? ""}
        onChange={(e) => onChange(e.target.value === "" ? null : { from: Number(e.target.value), to: range?.to ?? Number(e.target.value) })} />
      <Input type="number" aria-label={`${label} to`} value={range?.to ?? ""}
        onChange={(e) => onChange(e.target.value === "" ? null : { from: range?.from ?? Number(e.target.value), to: Number(e.target.value) })} />
    </div>
  );
}

function NumbersStep(props: {
  digits: number; onDigits: (d: number) => void;
  ranges: ReturnType<typeof defaultRanges>; onRanges: (r: ReturnType<typeof defaultRanges>) => void;
  siteKind: "" | "home" | "business";
} & StepProps) {
  const { digits, onDigits, ranges, onRanges, siteKind, ...shell } = props;
  const [expanded, setExpanded] = useState(false);
  const example = siteKind === "home" ? "Mum → " : "Sara → ";
  return (
    <StepShell {...shell} title="How should extension numbers look?">
      <div className="flex flex-wrap gap-4" role="radiogroup" aria-label="Digits">
        {[2, 3, 4, 5, 6].map((d) => (
          <label key={d} className="flex cursor-pointer items-center gap-2 text-sm">
            <input type="radio" name="digits" checked={digits === d} onChange={() => { onDigits(d); onRanges(defaultRanges(d)); }} />
            {d}{d === 3 && <Recommended>Recommended</Recommended>}
          </label>
        ))}
      </div>
      <div className="mt-5">
        <NumberBar digits={digits} ranges={ranges} />
        <p className="mt-2 text-sm text-muted-foreground">Example: {example}{ranges.people?.from ?? ""}</p>
      </div>
      <button type="button" className="mt-4 text-sm text-link underline-offset-4 hover:underline" onClick={() => setExpanded(!expanded)}>
        {expanded ? "Hide the ranges" : "Change the ranges"}
      </button>
      {expanded && (
        <div className="mt-3 flex flex-col gap-3 rounded-md border bg-card p-4">
          <RangeFields label="People" range={ranges.people} onChange={(r) => onRanges({ ...ranges, people: r })} />
          <RangeFields label="Groups" range={ranges.groups} onChange={(r) => onRanges({ ...ranges, groups: r })} />
          <RangeFields label="Kept free" range={ranges.reserved} onChange={(r) => onRanges({ ...ranges, reserved: r })} />
          <Button type="button" variant="outline" className="self-start" onClick={() => onRanges(defaultRanges(digits))}>
            Use the recommended ranges
          </Button>
        </div>
      )}
      <p className="mt-4 text-sm text-muted-foreground">3 digits gives room for 500 people and is quick to dial.</p>
    </StepShell>
  );
}

// --- Step 4: People ---

type PeopleRow = { name: string; email: string; role: Role; number: string };

function PeopleStep(props: {
  me: Me; existing: User[]; rows: PeopleRow[]; onRows: (r: PeopleRow[]) => void; startNumber: number | null; digits: number;
  results: { row: PeopleRow; link: string; qr: string }[]; onCreate: () => void; creating: boolean; createError: string;
} & StepProps) {
  const { me, existing, rows, onRows, startNumber, digits, results, onCreate, creating, createError, ...shell } = props;
  const addRow = () => {
    const number = startNumber === null ? "" : pad(startNumber + rows.length, digits);
    onRows([...rows, { name: "", email: "", role: "user", number }]);
  };
  const updateRow = (i: number, patch: Partial<PeopleRow>) => onRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const removeRow = (i: number) => onRows(rows.filter((_, j) => j !== i));

  return (
    <StepShell {...shell} title="Who will use Linx?">
      <div className="flex flex-col gap-2">
        <div className="grid grid-cols-[1fr_1fr_8rem_4rem] gap-2 text-xs font-medium text-muted-foreground">
          <span>Name</span><span>Email</span><span>Role</span><span>Ext</span>
        </div>
        <div className="grid grid-cols-[1fr_1fr_8rem_4rem] items-center gap-2 text-sm">
          <span>{me.name} (you)</span><span className="truncate text-muted-foreground">{me.email}</span>
          <span className="text-muted-foreground">System admin</span><span className="font-mono">{me.extension ?? "—"}</span>
        </div>
        {existing.filter((u) => u.id !== me.id).map((u) => (
          <div key={u.id} className="grid grid-cols-[1fr_1fr_8rem_4rem] items-center gap-2 text-sm text-muted-foreground">
            <span>{u.name}</span><span className="truncate">{u.email}</span><span className="capitalize">{u.role.replace("_", " ")}</span>
            <span className="font-mono">—</span>
          </div>
        ))}
        {rows.map((row, i) => (
          <div key={i} className="grid grid-cols-[1fr_1fr_8rem_4rem_auto] items-center gap-2">
            <Input aria-label="Name" value={row.name} onChange={(e) => updateRow(i, { name: e.target.value })} disabled={creating} />
            <Input aria-label="Email" type="email" value={row.email} onChange={(e) => updateRow(i, { email: e.target.value })} disabled={creating} />
            <Select value={row.role} onValueChange={(v) => updateRow(i, { role: v as Role })} disabled={creating}>
              <SelectTrigger aria-label="Role"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="user">Person</SelectItem>
                <SelectItem value="reporter">Reporter</SelectItem>
                <SelectItem value="admin">Admin</SelectItem>
              </SelectContent>
            </Select>
            <Input aria-label="Extension" className="font-mono" value={row.number} onChange={(e) => updateRow(i, { number: e.target.value })} disabled={creating} />
            <button type="button" aria-label="Remove row" onClick={() => removeRow(i)} disabled={creating} className="text-muted-foreground hover:text-foreground">
              <X aria-hidden="true" className="size-4" />
            </button>
          </div>
        ))}
      </div>
      <button type="button" className="mt-3 text-sm text-link underline-offset-4 hover:underline" onClick={addRow} disabled={creating}>
        + Add another row
      </button>
      {rows.length > 0 && (
        <div className="mt-4">
          <Button type="button" onClick={onCreate} disabled={creating || rows.every((r) => !r.name.trim() || !r.email.trim())}>
            Create and get invite links
          </Button>
          <FormError message={createError} />
        </div>
      )}
      {results.length > 0 && (
        <div className="mt-5 flex flex-col gap-3">
          <p className="text-sm text-muted-foreground">Invite links work once, for 24 hours. Email invites come later.</p>
          {results.map((r) => (
            <div key={r.row.email} className="flex items-center gap-3 rounded-md border bg-card p-3">
              <img src={r.qr} alt={`QR code to invite ${r.row.name}`} width={64} height={64} className="rounded-sm border" />
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium">{r.row.name} <span className="font-mono text-muted-foreground">· {r.row.number}</span></p>
                <p className="truncate text-xs text-muted-foreground">{r.link}</p>
              </div>
              <Button type="button" variant="outline" size="sm" onClick={() => void navigator.clipboard?.writeText(r.link)}>Copy</Button>
            </div>
          ))}
        </div>
      )}
    </StepShell>
  );
}

// --- Step 5: Phone line ---

function LineStep(props: StepProps) {
  return (
    <StepShell {...props} title="Connect a phone line now?">
      <div className="rounded-lg border p-5">
        <p className="font-display text-lg font-semibold">Later</p>
        <p className="mt-1 text-sm text-muted-foreground">Linx works between extensions without one.</p>
      </div>
      <p className="mt-4 text-sm text-muted-foreground">
        Connecting a line by template (a provider or another phone system) arrives in a later session — for now,
        continue and connect one afterwards with <code className="font-mono">linx trunk add</code>.
      </p>
    </StepShell>
  );
}

// --- Step 6: Calls ---

const CATEGORY_SWITCHES: { key: NumberCategory[]; label: string; hint?: string; recommended: boolean }[] = [
  { key: ["landline", "service"], label: "Local numbers", recommended: true },
  { key: ["mobile"], label: "Mobiles", recommended: true },
  { key: ["national"], label: "Other cities in the UAE", recommended: true },
  { key: ["toll_free"], label: "Free numbers (800)", recommended: true },
  { key: ["international"], label: "Abroad", hint: "Recommended off: most phone fraud is calls abroad", recommended: false },
  { key: ["premium"], label: "Premium-rate", hint: "Costs a lot per minute", recommended: false },
];

function CallsStep(props: { categories: Set<NumberCategory>; onChange: (c: Set<NumberCategory>) => void } & StepProps) {
  const { categories, onChange, ...shell } = props;
  const toggle = (keys: NumberCategory[], on: boolean) => {
    const next = new Set(categories);
    for (const k of keys) { if (on) next.add(k); else next.delete(k); }
    onChange(next);
  };
  return (
    <StepShell {...shell} title="What can your phones call?">
      <div className="flex flex-col gap-4">
        {CATEGORY_SWITCHES.map((sw) => {
          const on = sw.key.every((k) => categories.has(k));
          return (
            <label key={sw.label} className="flex items-center justify-between gap-4">
              <span>
                <span className="text-sm font-medium">{sw.label}</span>
                {sw.hint && <span className="block text-sm text-muted-foreground">{sw.hint}</span>}
              </span>
              <Switch checked={on} onCheckedChange={(v) => toggle(sw.key, v)} />
            </label>
          );
        })}
        <label className="flex items-center justify-between gap-4 opacity-70">
          <span className="text-sm font-medium">Emergency numbers — always on</span>
          <Switch checked disabled />
        </label>
      </div>
      <p className="mt-4 text-sm text-muted-foreground">
        This applies to every phone. Letting some people call abroad and not others comes later.
      </p>
    </StepShell>
  );
}

// --- Step 7: Test ---

function TestStep(props: StepProps) {
  const line = usePhoneLine();
  const { status, call } = usePhoneState();
  const [heard, setHeard] = useState<boolean | null>(null);
  if (call && call.peer.number === ECHO_TEST) {
    return (
      <StepShell {...props} title="Make a test call" nextDisabled={heard !== true}>
        <p className="text-sm text-muted-foreground">Speak — you should hear yourself back.</p>
        <div className="mt-4 max-w-sm"><CallPanel call={call} /></div>
        {call.phase === "active" && heard === null && (
          <div className="mt-4 flex gap-2">
            <Button type="button" onClick={() => setHeard(true)}>I heard myself</Button>
            <Button type="button" variant="outline" onClick={() => setHeard(false)}>I didn't</Button>
          </div>
        )}
        {heard === false && (
          <p className="mt-3 text-sm text-muted-foreground">
            Check your microphone and speaker in <a className="text-link underline-offset-4 hover:underline" href="/settings">Settings</a>,
            or the server's status in System status.
          </p>
        )}
      </StepShell>
    );
  }
  return (
    <StepShell {...props} title="Make a test call" nextDisabled>
      <Button type="button" disabled={status !== "ready"} onClick={() => line.call(ECHO_TEST, "Echo test")}>
        <Phone aria-hidden="true" className="size-4" />
        Call the echo test
      </Button>
      {status !== "ready" && <p className="mt-2 text-sm text-muted-foreground">Waiting for your browser's phone line…</p>}
    </StepShell>
  );
}

// --- Done ---

function DoneStep({ onGoHome }: { onGoHome: () => void }) {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-6 bg-background px-4 text-center">
      <Wordmark className="text-3xl" />
      <div>
        <h1 className="font-display text-2xl font-semibold">Linx is ready</h1>
        <p className="mt-2 max-w-sm text-sm text-muted-foreground">
          Your numbering plan, people and what your phones can call are all set. Add a phone line whenever you're ready.
        </p>
      </div>
      <Button onClick={onGoHome}>Go to admin home</Button>
    </main>
  );
}

// --- Wizard ---

type StepProps = { step: number; canSkip: boolean; busy: boolean; error?: string; onBack?: () => void; onNext: () => void; onSkip?: () => void; onFinishLater: () => void };

export function SetupWizardScreen({ me, onExit }: { me: Me; onExit: () => void }) {
  const confirm = useConfirmIdentity(me);
  const [step, setStep] = useState(1);
  const [loaded, setLoaded] = useState(false);
  const [done, setDone] = useState(false);
  // A system admin on a fresh install first chooses: set up fresh, or
  // restore from a backup (docs/BACKUP.md §4).
  const [start, setStart] = useState<"choose" | "fresh" | "restore">("fresh");
  const [restore, setRestore] = useState<RestoreStatus | undefined>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const [siteKind, setSiteKind] = useState<"" | "home" | "business">("");
  const [digits, setDigits] = useState(3);
  const [ranges, setRanges] = useState(defaultRanges(3));
  const [existingUsers, setExistingUsers] = useState<User[]>([]);
  const [rows, setRows] = useState<PeopleRow[]>([]);
  const [results, setResults] = useState<{ row: PeopleRow; link: string; qr: string }[]>([]);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState("");
  const [categories, setCategories] = useState<Set<NumberCategory>>(new Set(["landline", "service", "mobile", "national", "toll_free"]));

  const nextNumber = useRef<number | null>(null);

  useEffect(() => {
    void (async () => {
      const [{ data: setup }, { data: settings }] = await Promise.all([
        api.GET("/api/v1/setup"), api.GET("/api/v1/settings"),
      ]);
      if (setup?.completed) { setDone(true); setLoaded(true); return; }
      if (me.role === "system_admin") {
        const { data: r } = await api.GET("/api/v1/backup-restore");
        if (r && r.status !== "none") {
          setRestore(r);
          setStart("restore");
        } else if ((setup?.step ?? 1) <= 1) {
          // Nothing done yet: the server keeps the step to resume at,
          // starting at 1 (migration 0021).
          setStart("choose");
        }
      }
      if (settings) {
        setSiteKind(settings.site_kind);
        setDigits(settings.extension_digits);
        const find = (k: string) => settings.extension_ranges.find((r) => r.kind === k);
        setRanges({
          people: find("people") ?? defaultRanges(settings.extension_digits).people,
          groups: find("groups") ?? null,
          reserved: find("reserved") ?? null,
        });
      }
      // GET /setup's step is where to pick back up (1 on a fresh install).
      setStep(Math.min(TOTAL_STEPS, Math.max(1, setup?.step ?? 1)));
      setLoaded(true);
    })();
  }, [me.role]);

  useEffect(() => {
    if (step !== 4 || loaded === false) return;
    void (async () => {
      const [{ data: list }, { data: next }] = await Promise.all([
        api.GET("/api/v1/users"), api.GET("/api/v1/numbering/next"),
      ]);
      if (list) setExistingUsers(list.items);
      nextNumber.current = next ? Number(next.number) : null;
    })();
    // Runs once when the People step is reached.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step]);

  const advance = useCallback(async (toStep: number, complete = false) => {
    setBusy(true);
    setError("");
    // Saves where to resume: the step being moved to (the last one again
    // once finished; `completed` then says it's done).
    const { data, error: err } = await api.PUT("/api/v1/setup", { body: { step: Math.min(TOTAL_STEPS, Math.max(1, toStep)), complete } });
    if (!data) { setBusy(false); setError(problemMessage(err)); return false; }
    if (complete) {
      // The wizard's "Calls" step picks the categories; PUT /setup only
      // creates "Everyone" with the fixed defaults the first time
      // (services/control-plane/api/settings.go), so apply the chosen
      // ones on top of it now that it exists.
      const { data: levels } = await api.GET("/api/v1/call-permission-levels");
      const everyone = levels?.items.find((l) => l.name === "Everyone");
      const chosen = [...categories].sort();
      if (everyone && JSON.stringify([...everyone.allowed_categories].sort()) !== JSON.stringify(chosen)) {
        await api.PATCH("/api/v1/call-permission-levels/{id}", {
          params: { path: { id: everyone.id } }, headers: { "If-Match": everyone.etag }, body: { allowed_categories: chosen },
        });
      }
      setBusy(false);
      setDone(true);
    } else {
      setBusy(false);
      setStep(toStep);
    }
    return true;
  }, [categories]);

  const saveSettingsPatch = async (patch: components["schemas"]["SettingsPatch"]) => {
    const { error: err } = await api.PATCH("/api/v1/settings", { body: patch });
    if (err) { setError(problemMessage(err)); return false; }
    return true;
  };

  const shellProps = (canSkip: boolean, onNext: () => void, onSkip?: () => void): StepProps => ({
    step, canSkip, busy, error, onBack: step > 1 ? () => setStep(step - 1) : undefined,
    onNext, onSkip, onFinishLater: onExit,
  });

  const rangesForApi = () => {
    const out: { kind: "people" | "groups" | "reserved"; from: number; to: number }[] = [];
    if (ranges.people) out.push({ kind: "people", ...ranges.people });
    if (ranges.groups) out.push({ kind: "groups", ...ranges.groups });
    if (ranges.reserved) out.push({ kind: "reserved", ...ranges.reserved });
    return out;
  };

  const createRow = async (row: PeopleRow): Promise<{ link: string; qr: string } | "confirm" | null> => {
    const ext = await api.POST("/api/v1/extensions", { body: { number: row.number, display_name: row.name } });
    if (!ext.data) { setCreateError(problemMessage(ext.error)); return null; }
    const created = await api.POST("/api/v1/users", { body: { email: row.email, name: row.name, role: row.role, extension_id: ext.data.id } });
    if (needsConfirm(created.error)) return "confirm";
    if (!created.data) { setCreateError(problemMessage(created.error)); return null; }
    const link = `${window.location.origin}/setup/${created.data.setup_link_token}`;
    const QRCode = (await import("qrcode")).default;
    return { link, qr: await QRCode.toDataURL(link, { margin: 1, width: 128 }) };
  };

  const onCreatePeople = async () => {
    setCreating(true);
    setCreateError("");
    const remaining: PeopleRow[] = [];
    for (const row of rows) {
      if (!row.name.trim() || !row.email.trim()) { remaining.push(row); continue; }
      const out = await createRow(row);
      if (out === "confirm") {
        remaining.push(row);
        setRows(remaining.concat(rows.slice(remaining.length)));
        setCreating(false);
        confirm.ask();
        setCreateError("Confirm it's you, then press Create again to add the rest.");
        return;
      }
      if (out === null) { remaining.push(row); continue; }
      setResults((prev) => [...prev, { row, ...out }]);
    }
    setRows(remaining);
    setCreating(false);
  };

  if (!loaded) return <div className="min-h-dvh" aria-busy="true" />;
  if (done) return <DoneStep onGoHome={onExit} />;
  if (start === "choose") {
    return <StartChoice onFresh={() => setStart("fresh")} onRestore={() => setStart("restore")} onFinishLater={onExit} />;
  }
  if (start === "restore") {
    return <RestoreFromBackup me={me} initial={restore} onFinishLater={onExit}
      onBack={() => { setRestore(undefined); setStart("choose"); }} />;
  }

  return (
    <>
      {step === 1 && (
        <PlaceStep value={siteKind} onChange={(v) => setSiteKind(v)}
          {...shellProps(true, async () => { if (await saveSettingsPatch({ site_kind: siteKind })) void advance(2); }, () => void advance(2))} />
      )}
      {step === 2 && (
        <CountryStep {...shellProps(true, () => void advance(3), () => void advance(3))} />
      )}
      {step === 3 && (
        <NumbersStep digits={digits} onDigits={setDigits} ranges={ranges} onRanges={setRanges} siteKind={siteKind}
          {...shellProps(false, async () => {
            if (await saveSettingsPatch({ extension_digits: digits, extension_ranges: rangesForApi() })) void advance(4);
          })} />
      )}
      {step === 4 && (
        <PeopleStep me={me} existing={existingUsers} rows={rows} onRows={setRows} startNumber={nextNumber.current} digits={digits}
          results={results} onCreate={() => void onCreatePeople()} creating={creating} createError={createError}
          {...shellProps(true, () => void advance(5), () => void advance(5))} />
      )}
      {step === 5 && (
        <LineStep {...shellProps(true, () => void advance(6), () => void advance(6))} />
      )}
      {step === 6 && (
        <CallsStep categories={categories} onChange={setCategories}
          {...shellProps(true, () => void advance(7), () => void advance(7))} />
      )}
      {step === 7 && (
        <TestStep {...shellProps(true, () => void advance(8, true), () => void advance(8, true))} />
      )}
      {confirm.dialog}
    </>
  );
}
