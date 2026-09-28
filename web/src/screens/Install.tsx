// The web install's plain page on port 6464 (docs/INSTALL.md,
// docs/ui/INSTALL_SCREENS.md §2): where the server is, what's in front of
// it, the domain and who's setting it up. Nothing secret is asked here, and
// nothing changes on the server until "Check and get a certificate": then
// linx setup, on the host, checks the answers with setup.yaml's own rules.
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Building2, Check, ChevronDown, CircleAlert, House, LoaderCircle, LockOpen } from "lucide-react";
import { Wordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { cn } from "@/lib/utils";
import {
  checkAnswers, domainProblem, emailProblem, emptyAnswers, getState, LinkClosed, nameProblem, portProblem,
  proxyAddressProblem, proxyKinds, saveDraft, type Answers, type Facts, type FieldError, type FrontDoor,
  type InstallState, type Step,
} from "@/lib/install";

/** The whole install's steps; the plain page covers the first three. */
const PROGRESS = ["Server", "Domain", "You", "Certificate", "DNS", "Sign-in", "Install"] as const;
const progressIndex: Record<Step, number> = { welcome: 0, where: 0, front_door: 0, domain: 1, you: 2, checked: 3 };
const firstStepOf: Step[] = ["where", "domain", "you"];

const LE_AGREEMENT = "https://letsencrypt.org/repository/";

export default function InstallScreen() {
  const [state, setState] = useState<InstallState | "loading" | "closed">("loading");
  useEffect(() => {
    // Any failure to read the state means the page can't go on: the link
    // or its hour is over (404), or the installer has stopped.
    getState().then(setState, () => setState("closed"));
  }, []);
  if (state === "loading") return <main className="min-h-dvh bg-background" aria-busy="true" />;
  if (state === "closed") return <LinkUnusable />;
  return <Wizard initial={state} onClosed={() => setState("closed")} />;
}

function LinkUnusable() {
  return (
    <Frame>
      <h1 className="font-display text-xl font-semibold">This link can't be used</h1>
      <p className="mt-2 text-sm text-muted-foreground">Setup links work once, for one hour. For a new one, run on the server:</p>
      <code className="mt-4 block rounded-md bg-muted px-3 py-2 font-mono text-sm">sudo linx setup</code>
    </Frame>
  );
}

function Frame({ step, onStep, strip = true, children }: {
  step?: Step; onStep?: (s: Step) => void; strip?: boolean; children: ReactNode;
}) {
  return (
    <main className="flex min-h-dvh items-start justify-center bg-background px-4 py-10 sm:items-center">
      <div className="w-full max-w-xl">
        <div className="mb-8 flex justify-center">
          <Wordmark className="text-5xl" />
        </div>
        {step && step !== "welcome" && <ProgressLine step={step} onStep={onStep} />}
        <section className="rounded-lg border bg-card shadow-xs">
          {strip && (
            <p className="flex items-start gap-2 border-b px-6 py-3 text-sm text-muted-foreground">
              <LockOpen aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              <span>This page isn't encrypted yet. Nothing secret is asked here.</span>
            </p>
          )}
          <div className="p-6">{children}</div>
        </section>
      </div>
    </main>
  );
}

/** "● Server ─ ● Domain ─ ◉ You ─ ○ …"; "Step 3 of 7 · You" on a phone. */
function ProgressLine({ step, onStep }: { step: Step; onStep?: (s: Step) => void }) {
  const at = progressIndex[step];
  return (
    <nav aria-label="Install progress" className="mb-4">
      <p className="text-center text-sm text-muted-foreground sm:hidden">Step {at + 1} of {PROGRESS.length} · {PROGRESS[at]}</p>
      <ol className="hidden flex-wrap items-center justify-center gap-x-2 gap-y-1 text-sm sm:flex">
        {PROGRESS.map((name, i) => {
          const done = i < at;
          const back = done && i < firstStepOf.length && step !== "checked" && onStep;
          const dot = (
            <span className={cn("flex items-center gap-1.5", i === at ? "font-medium text-foreground" : done ? "text-foreground" : "text-muted-foreground")}>
              <span aria-hidden="true" className={cn("inline-block size-2.5 rounded-full border",
                done ? "border-primary bg-primary" : i === at ? "border-primary ring-2 ring-primary/30" : "border-border")} />
              {name}
              {i === at && <span className="sr-only"> (this step)</span>}
            </span>
          );
          return (
            <li key={name} className="flex items-center gap-2">
              {i > 0 && <span aria-hidden="true" className="h-px w-3 bg-border" />}
              {back ? <button type="button" className="hover:underline" onClick={() => onStep(firstStepOf[i] ?? "where")}>{dot}</button> : dot}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

function Wizard({ initial, onClosed }: { initial: InstallState; onClosed: () => void }) {
  const facts = initial.facts;
  const [step, setStep] = useState<Step>(initial.accepted ? "checked" : initial.draft?.step ?? "welcome");
  const [answers, setAnswers] = useState<Answers>(initial.accepted ?? initial.draft?.answers ?? { ...emptyAnswers, where: facts.where });
  const [serverErrors, setServerErrors] = useState<FieldError[]>([]);

  // Keep the draft on the server (a moment after the last change), so this
  // browser comes back to the same step.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) { first.current = false; return; }
    if (step === "checked") return;
    const t = setTimeout(() => { saveDraft({ step, answers }).catch((e) => { if (e instanceof LinkClosed) onClosed(); }); }, 400);
    return () => clearTimeout(t);
  }, [step, answers, onClosed]);

  const set = useCallback((patch: Partial<Answers>) => {
    setAnswers((a) => ({ ...a, ...patch }));
    setServerErrors((errs) => errs.filter((e) => !(Object.keys(patch) as string[]).includes(e.field)));
  }, []);
  const go = (s: Step) => { setStep(s); window.scrollTo(0, 0); };
  const errorFor = (field: string) => serverErrors.find((e) => e.field === field)?.message ?? "";

  const common = { answers, set, errorFor };
  let body: ReactNode;
  switch (step) {
    case "welcome":
      body = <Welcome onStart={() => go("where")} />;
      break;
    case "where":
      body = <WhereStep {...common} facts={facts} onNext={() => go("front_door")} />;
      break;
    case "front_door":
      body = <FrontDoorStep {...common} facts={facts} onBack={() => go("where")} onNext={() => go("domain")} />;
      break;
    case "domain":
      body = <DomainStep {...common} onBack={() => go("front_door")} onNext={() => go("you")} />;
      break;
    case "you":
      body = (
        <YouStep {...common} onBack={() => go("domain")} onClosed={onClosed}
          onRefused={(errs) => { setServerErrors(errs); go(errs[0]?.step ?? "you"); }}
          onAccepted={() => go("checked")} />
      );
      break;
    case "checked":
      body = <Checked answers={answers} facts={facts} />;
      break;
  }
  return <Frame step={step} onStep={go}>{body}</Frame>;
}

function Title({ children, lead }: { children: ReactNode; lead?: ReactNode }) {
  return (
    <div className="mb-6">
      <h1 className="font-display text-xl font-semibold">{children}</h1>
      {lead && <p className="mt-1 text-sm text-muted-foreground">{lead}</p>}
    </div>
  );
}

function FieldError({ message }: { message: string }) {
  if (!message) return null;
  return (
    <p role="alert" className="flex items-start gap-2 text-sm font-medium">
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      {message}
    </p>
  );
}

function Nav({ onBack, next = "Next", busy = false, disabled = false }: { onBack?: () => void; next?: string; busy?: boolean; disabled?: boolean }) {
  return (
    <div className="mt-8 flex flex-wrap items-center justify-end gap-3">
      {onBack && <Button type="button" variant="outline" onClick={onBack} disabled={busy}>Back</Button>}
      <Button type="submit" disabled={busy || disabled}>
        {busy && <LoaderCircle aria-hidden="true" className="animate-spin" />}
        {next}
      </Button>
    </div>
  );
}

type StepProps = { answers: Answers; set: (p: Partial<Answers>) => void; errorFor: (f: string) => string };

function submit(fn: () => void) {
  return (e: FormEvent) => { e.preventDefault(); fn(); };
}

function Welcome({ onStart }: { onStart: () => void }) {
  return (
    <form onSubmit={submit(onStart)}>
      <Title lead="About ten minutes. You'll need:">Let's set up Linx</Title>
      <ul className="list-disc space-y-1 ps-5 text-sm">
        <li>a domain you own (like example.com)</li>
        <li>access to its DNS settings</li>
        <li>your phone, for a passkey or an authenticator app</li>
      </ul>
      <Nav next="Start" />
    </form>
  );
}

function Choice({ id, value, title, hint, badge, disabled, children }: {
  id: string; value: string; title: ReactNode; hint?: ReactNode; badge?: string; disabled?: boolean; children?: ReactNode;
}) {
  return (
    <div className={cn("rounded-md border p-4 has-[[data-state=checked]]:border-primary", disabled && "opacity-60")}>
      <label htmlFor={id} className={cn("flex items-start gap-3", disabled ? "cursor-not-allowed" : "cursor-pointer")}>
        <RadioGroupItem id={id} value={value} className="mt-1" disabled={disabled} />
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="text-sm font-medium">
            {title}
            {badge && <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">{badge}</span>}
          </span>
          {hint && <span className="text-sm text-muted-foreground">{hint}</span>}
        </span>
      </label>
      {children && <div className="mt-3 ps-7">{children}</div>}
    </div>
  );
}

function WhereStep({ answers, set, errorFor, facts, onNext }: StepProps & { facts: Facts; onNext: () => void }) {
  const found = facts.lan_address
    ? `We found: this server is ${facts.lan_address} on a home or office network${facts.public_address ? `, which reaches the internet from ${facts.public_address}` : ""}.`
    : `We found: public address ${facts.public_address ?? "unknown"}, no home network.`;
  const mismatch = answers.where && answers.where !== facts.where
    ? answers.where === "home"
      ? "We didn't find a home network on this server. Desk phones won't be able to reach it."
      : "This server is on a home or office network. That's fine for a server in a data centre behind its provider's network too."
    : "";
  return (
    <form onSubmit={submit(onNext)}>
      <Title>Where is this server?</Title>
      <RadioGroup value={answers.where} onValueChange={(v) => set({ where: v as Answers["where"], front_door: "" })}
        className="grid gap-3 sm:grid-cols-2" aria-label="Where is this server?">
        {([
          ["rented", "Rented server (VPS)", "In a data centre, with its own public address.", Building2],
          ["home", "At home or at the office", "Behind your router, next to your desk phones.", House],
        ] as const).map(([value, title, hint, Icon]) => (
          <label key={value} htmlFor={`where-${value}`}
            className="flex cursor-pointer flex-col gap-2 rounded-md border p-4 has-[[data-state=checked]]:border-primary">
            <span className="flex items-center justify-between gap-2">
              <Icon aria-hidden="true" className="size-5 text-link" />
              <RadioGroupItem id={`where-${value}`} value={value} />
            </span>
            <span className="text-sm font-medium">
              {title}
              {facts.where === value && <span className="ms-2 text-xs font-normal text-muted-foreground">(What we found)</span>}
            </span>
            <span className="text-sm text-muted-foreground">{hint}</span>
          </label>
        ))}
      </RadioGroup>
      <p className="mt-4 text-sm text-muted-foreground">{found}</p>
      {mismatch && <p className="mt-2 text-sm">{mismatch}</p>}
      <div className="mt-2"><FieldError message={errorFor("where")} /></div>
      <Nav disabled={!answers.where} />
    </form>
  );
}

const doorText: Record<FrontDoor, { title: string; hint: string }> = {
  "linx-443": { title: "Directly — Linx answers on port 443 itself", hint: "Your server provider sends port 443 here. Nothing else on this server uses it." },
  pangolin: { title: "Pangolin", hint: "Your router sends port 443 to Pangolin." },
  nginx: { title: "nginx or HAProxy, already using port 443", hint: "It passes Linx's names through to it." },
  "http-proxy": { title: "Caddy or Nginx Proxy Manager", hint: "Needs your DNS company's token on this unencrypted page." },
  "home-only": { title: "Nothing — only at home", hint: "Linx works on this network only. No calls from outside. Needs your DNS company's token on this unencrypted page." },
};

function FrontDoorStep({ answers, set, errorFor, facts, onBack, onNext }: StepProps & { facts: Facts; onBack: () => void; onNext: () => void }) {
  const rented = answers.where === "rented";
  const noLAN = !facts.lan_address;
  const isProxy = proxyKinds.includes(answers.front_door as FrontDoor);
  const [more, setMore] = useState(rented && isProxy);
  const [portOpen, setPortOpen] = useState(!!answers.turn_udp_port && answers.turn_udp_port !== 443);
  const [touched, setTouched] = useState(false);
  const port = answers.turn_udp_port ?? 443;
  const addrProblem = isProxy ? proxyAddressProblem(answers.proxy_address ?? "") : "";
  const portErr = isProxy ? portProblem(port) : "";
  const needsLAN = (d: FrontDoor) => proxyKinds.includes(d) || d === "home-only";
  const text = (d: FrontDoor) => d === "linx-443" && !rented
    ? { title: "Nothing — Linx takes port 443 itself", hint: "Your router sends TCP and UDP port 443 straight to this server." }
    : doorText[d];
  const choice = (d: FrontDoor, badge?: string) => (
    <Choice key={d} id={`door-${d}`} value={d} title={text(d).title} badge={badge} disabled={noLAN && needsLAN(d)}
      hint={noLAN && needsLAN(d) ? "Needs this server on a home network, and it isn't on one." : text(d).hint} />
  );
  const home: FrontDoor[] = ["pangolin", "nginx", "http-proxy", "linx-443", "home-only"];

  return (
    <form onSubmit={submit(() => { setTouched(true); if (answers.front_door && !addrProblem && !portErr) onNext(); })}>
      <Title>{rented ? "How do people reach this server from the internet?" : "What's in front of Linx on the internet?"}</Title>
      <RadioGroup value={answers.front_door} aria-label="What's in front of this server"
        onValueChange={(v) => set({ front_door: v as FrontDoor, ...(proxyKinds.includes(v as FrontDoor) ? {} : { proxy_address: undefined, turn_udp_port: undefined }) })}>
        {rented ? (
          <>
            {choice("linx-443", "Recommended")}
            {facts.port_443 && <p className="text-sm text-muted-foreground">Port 443 is used by {facts.port_443} on this server right now.</p>}
            <button type="button" aria-expanded={more} onClick={() => setMore(!more)}
              className="flex items-center gap-1 justify-self-start text-sm text-link hover:underline">
              <ChevronDown aria-hidden="true" className={cn("size-4 transition-transform", !more && "-rotate-90")} />
              Something else already uses port 443 here
            </button>
            {more && (["nginx", "http-proxy"] as FrontDoor[]).map((d) => choice(d))}
          </>
        ) : (
          home.map((d) => choice(d, d === "pangolin" ? "Recommended if you use it" : undefined))
        )}
      </RadioGroup>
      {isProxy && (
        <div className="mt-4 flex flex-col gap-2 rounded-md border p-4">
          <Label htmlFor="proxy-address">Address of the machine {answers.front_door === "pangolin" ? "Pangolin" : "it"} runs on</Label>
          <Input id="proxy-address" inputMode="decimal" autoComplete="off" placeholder="192.168.1.20" className="max-w-56"
            value={answers.proxy_address ?? ""} onChange={(e) => set({ proxy_address: e.target.value })} aria-invalid={touched && !!addrProblem} />
          {facts.lan_address && <p className="text-sm text-muted-foreground">{facts.lan_address} if it's this server.</p>}
          <FieldError message={(touched && addrProblem) || errorFor("proxy_address")} />
          <button type="button" aria-expanded={portOpen} onClick={() => setPortOpen(!portOpen)}
            className="mt-2 flex items-center gap-1 justify-self-start text-start text-sm text-link hover:underline">
            <ChevronDown aria-hidden="true" className={cn("size-4 shrink-0 transition-transform", !portOpen && "-rotate-90")} />
            Call audio port: {port} (change to 3478 for UniFi routers)
          </button>
          {portOpen && (
            <div className="flex flex-col gap-2 ps-5">
              <Label htmlFor="udp-port">UDP port your router forwards to Linx for call audio</Label>
              <Input id="udp-port" inputMode="numeric" className="max-w-28" value={String(port)}
                onChange={(e) => set({ turn_udp_port: Number(e.target.value.replace(/\D/g, "")) || 0 })} aria-invalid={!!portErr} />
              <p className="text-sm text-muted-foreground">
                443 works when your router can send UDP 443 to Linx while TCP 443 goes to {answers.front_door === "pangolin" ? "Pangolin" : "the proxy"}. Some (UniFi) can't: use 3478 then.
              </p>
              <FieldError message={portErr || errorFor("turn_udp_port")} />
            </div>
          )}
        </div>
      )}
      <div className="mt-3"><FieldError message={errorFor("front_door")} /></div>
      <p className="mt-4 text-sm text-muted-foreground">
        Want no web address at all? Use <code className="font-mono">sudo linx setup --config</code> instead.
      </p>
      <Nav onBack={onBack} disabled={!answers.front_door} />
    </form>
  );
}

function DomainStep({ answers, set, errorFor, onBack, onNext }: StepProps & { onBack: () => void; onNext: () => void }) {
  const [touched, setTouched] = useState(false);
  const problem = domainProblem(answers.domain);
  const d = answers.domain.trim().toLowerCase() || "example.com";
  const names: [string, string][] = [
    [`meet.${d}`, "the web app and calls from outside"],
    [`api.${d}`, "for other apps"],
    [`turn.${d}`, "call audio through firewalls"],
  ];
  if (answers.where === "home") names.push([`sip.${d}`, "desk phones at home"]);
  return (
    <form onSubmit={submit(() => { setTouched(true); if (!problem) onNext(); })}>
      <Title lead="You need access to this domain's DNS settings, where you'll add one record in a minute.">What's your domain?</Title>
      <div className="flex flex-col gap-2">
        <Label htmlFor="domain">Domain</Label>
        <Input id="domain" autoComplete="off" autoCapitalize="none" spellCheck={false} placeholder="example.com"
          value={answers.domain} onChange={(e) => set({ domain: e.target.value })} aria-invalid={touched && !!problem} />
        <FieldError message={(touched && problem) || errorFor("domain")} />
      </div>
      <p className="mt-6 text-sm text-muted-foreground">Linx will use these names under it:</p>
      <dl className="mt-2 grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
        {names.map(([n, what]) => (
          <div key={n} className="contents">
            <dt className="font-mono break-all">{n}</dt>
            <dd className="mb-2 text-muted-foreground sm:mb-0">{what}</dd>
          </div>
        ))}
      </dl>
      <Nav onBack={onBack} />
    </form>
  );
}

function YouStep({ answers, set, errorFor, onBack, onRefused, onAccepted, onClosed }: StepProps & {
  onBack: () => void; onRefused: (e: FieldError[]) => void; onAccepted: () => void; onClosed: () => void;
}) {
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const nameErr = nameProblem(answers.name);
  const emailErr = emailProblem(answers.email);
  const termsErr = answers.agreed_to_terms ? "" : "Tick the box to agree. Linx can't get a certificate without it.";

  const send = async () => {
    setTouched(true);
    setProblem("");
    if (nameErr || emailErr || termsErr) return;
    setBusy(true);
    try {
      const r = await checkAnswers({
        ...answers, domain: answers.domain.trim().toLowerCase(), name: answers.name.trim(), email: answers.email.trim(),
        turn_udp_port: answers.turn_udp_port === 443 ? undefined : answers.turn_udp_port,
      });
      if (r.ok) onAccepted();
      else if ("errors" in r) onRefused(r.errors);
      else setProblem(r.problem);
    } catch (e) {
      if (e instanceof LinkClosed) onClosed();
      else setProblem("Can't reach this server. Check your connection and try again.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit(() => void send())}>
      <Title>Who's setting this up?</Title>
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Label htmlFor="name">Your name</Label>
          <Input id="name" autoComplete="name" value={answers.name} onChange={(e) => set({ name: e.target.value })} aria-invalid={touched && !!nameErr} />
          <FieldError message={(touched && nameErr) || errorFor("name")} />
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="email">Your email</Label>
          <Input id="email" type="email" autoComplete="email" value={answers.email} onChange={(e) => set({ email: e.target.value })} aria-invalid={touched && !!emailErr} />
          <FieldError message={(touched && emailErr) || errorFor("email")} />
        </div>
      </div>
      <p className="mt-6 text-sm text-muted-foreground">
        You'll be the first admin, with full control. Let's Encrypt also uses this email for certificate notices.
      </p>
      <div className="mt-4 flex items-start gap-3">
        <Checkbox id="terms" checked={answers.agreed_to_terms} onCheckedChange={(c) => set({ agreed_to_terms: c === true })} className="mt-0.5" />
        <Label htmlFor="terms" className="font-normal leading-snug">
          <span>
            I agree to{" "}
            <a href={LE_AGREEMENT} target="_blank" rel="noreferrer noopener" className="text-link underline-offset-4 hover:underline">
              Let's Encrypt's Subscriber Agreement
            </a>
          </span>
        </Label>
      </div>
      <div className="mt-2"><FieldError message={(touched && termsErr) || errorFor("agreed_to_terms")} /></div>
      {problem && <div className="mt-4"><FieldError message={problem} /></div>}
      <Nav onBack={onBack} next="Check and get a certificate" busy={busy} />
    </form>
  );
}

/**
 * The answers are saved on the server. What comes next — the record to
 * add, then the certificate — is the waiting page
 * (docs/ui/INSTALL_SCREENS.md §2.7).
 */
function Checked({ answers, facts }: { answers: Answers; facts: Facts }) {
  const d = answers.domain.trim().toLowerCase();
  const value = answers.front_door === "home-only" ? facts.lan_address : facts.public_address;
  return (
    <div>
      <Title lead="Linx checked them and saved them on the server.">
        <span className="flex items-center gap-2"><Check aria-hidden="true" className="size-5 text-status-available" />Your answers are saved</span>
      </Title>
      <p className="text-sm">Next, Linx gets a certificate for <span className="font-mono break-all">meet.{d}</span>. It needs this record at your DNS company:</p>
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 rounded-md border p-4 text-sm">
        <dt className="text-muted-foreground">Type</dt><dd className="font-mono">A</dd>
        <dt className="text-muted-foreground">Name</dt><dd className="font-mono break-all">meet.{d}</dd>
        <dt className="text-muted-foreground">Value</dt><dd className="font-mono">{value ?? "this server's public address"}</dd>
        <dt className="text-muted-foreground">Proxy</dt><dd>off (grey cloud, on Cloudflare)</dd>
      </dl>
    </div>
  );
}
