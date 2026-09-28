// The web install's plain page on port 6464 (docs/INSTALL.md,
// docs/ui/INSTALL_SCREENS.md §2): where the server is, what's in front of
// it, the domain and who's setting it up. Nothing secret is asked here, and
// nothing changes on the server until "Check and get a certificate": then
// linx setup, on the host, checks the answers with setup.yaml's own rules,
// and the certificate page follows (InstallCertificate.tsx). On
// https://<domain> the same address is the secure page.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Building2, ChevronDown, House } from "lucide-react";
import { Choice, Countdown, FieldMessage, Frame, LinkUnusable, Nav, submit, Title, useSecondsLeft } from "@/components/InstallFrame";
import { CertificateStep, SecureInstall } from "@/screens/InstallCertificate";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import {
  browserTimeZone, checkAnswers, domainProblem, timeZones, emailProblem, emptyAnswers, getState, LinkClosed, nameProblem, portProblem,
  proxyAddressProblem, proxyKinds, saveDraft, type Answers, type Facts, type FieldError, type FrontDoor,
  type InstallState, type Step,
} from "@/lib/install";

const progressIndex: Record<Step, number> = { welcome: 0, where: 0, front_door: 0, domain: 1, you: 2, checked: 3 };
const firstStepOf: Step[] = ["where", "domain", "you"];

const LE_AGREEMENT = "https://letsencrypt.org/repository/";

/** The first page's port: HTTPS too (self-signed), so the scheme alone can't tell the pages apart. */
const FIRST_PAGE_PORT = "6464";

export default function InstallScreen() {
  if (isSecurePage(window.location)) return <SecureInstall />;
  return <PlainInstall />;
}

/** The secure page is https://<domain> (port 443); the first page is port 6464, over HTTP or HTTPS. */
export function isSecurePage(l: Pick<Location, "protocol" | "port">): boolean {
  return l.protocol === "https:" && l.port !== FIRST_PAGE_PORT;
}

function PlainInstall() {
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

function Wizard({ initial, onClosed }: { initial: InstallState; onClosed: () => void }) {
  const facts = initial.facts;
  const [step, setStep] = useState<Step>(initial.accepted ? "checked" : initial.draft?.step ?? "welcome");
  const [answers, setAnswers] = useState<Answers>(() => {
    const a = initial.accepted ?? initial.draft?.answers ?? { ...emptyAnswers, where: facts.where };
    return { ...a, time_zone: a.time_zone || browserTimeZone() };
  });
  const left = useSecondsLeft(initial.expires_in);
  useEffect(() => { if (left === 0) onClosed(); }, [left, onClosed]);
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
        <YouStep {...common} facts={facts} onBack={() => go("domain")} onClosed={onClosed}
          onRefused={(errs) => { setServerErrors(errs); go(errs[0]?.step ?? "you"); }}
          onAccepted={() => go("checked")} />
      );
      break;
    case "checked":
      body = <CertificateStep initial={initial} answers={answers} facts={facts} onClosed={onClosed} />;
      break;
  }
  return (
    <Frame at={step === "welcome" ? undefined : progressIndex[step]} back={step === "checked" ? undefined : (i) => go(firstStepOf[i] ?? "where")}
      footer={<Countdown left={left} />}>
      {body}
    </Frame>
  );
}

type StepProps = { answers: Answers; set: (p: Partial<Answers>) => void; errorFor: (f: string) => string };

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
      <div className="mt-2"><FieldMessage message={errorFor("where")} /></div>
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
          <FieldMessage message={(touched && addrProblem) || errorFor("proxy_address")} />
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
              <FieldMessage message={portErr || errorFor("turn_udp_port")} />
            </div>
          )}
        </div>
      )}
      <div className="mt-3"><FieldMessage message={errorFor("front_door")} /></div>
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
  const d = answers.domain.trim().toLowerCase() || "pbx.example.com";
  const names: [string, string][] = [
    [d, "the web app, where people open Linx"],
    [`turn.${d}`, "call audio through firewalls"],
  ];
  if (answers.where === "home") names.push([`sip.${d}`, "desk phones at home"]);
  return (
    <form onSubmit={submit(() => { setTouched(true); if (!problem) onNext(); })}>
      <Title lead="You need access to this domain's DNS settings, where you'll add two records in a minute.">What's your domain?</Title>
      <div className="flex flex-col gap-2">
        <Label htmlFor="domain">Domain</Label>
        <Input id="domain" autoComplete="off" autoCapitalize="none" spellCheck={false} placeholder="pbx.example.com"
          value={answers.domain} onChange={(e) => set({ domain: e.target.value })} aria-invalid={touched && !!problem} />
        <p className="text-sm text-muted-foreground">
          People open Linx at this address, so use a name just for Linx (like pbx.example.com) if your domain already has a website.
        </p>
        <FieldMessage message={(touched && problem) || errorFor("domain")} />
      </div>
      <p className="mt-6 text-sm text-muted-foreground">Linx will use:</p>
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

function YouStep({ answers, set, errorFor, facts, onBack, onRefused, onAccepted, onClosed }: StepProps & {
  facts: Facts; onBack: () => void; onRefused: (e: FieldError[]) => void; onAccepted: () => void; onClosed: () => void;
}) {
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const nameErr = nameProblem(answers.name);
  const emailErr = emailProblem(answers.email);
  const termsErr = answers.agreed_to_terms ? "" : "Tick the box to agree. Linx can't get a certificate without it.";
  const zones = timeZones();
  // Said only when it differs: a rented server's clock is usually UTC.
  const serverZone = facts.time_zone && facts.time_zone !== answers.time_zone ? facts.time_zone : "";

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
          <FieldMessage message={(touched && nameErr) || errorFor("name")} />
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="email">Your email</Label>
          <Input id="email" type="email" autoComplete="email" value={answers.email} onChange={(e) => set({ email: e.target.value })} aria-invalid={touched && !!emailErr} />
          <FieldMessage message={(touched && emailErr) || errorFor("email")} />
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="time-zone">Your time zone</Label>
          <Select value={answers.time_zone} onValueChange={(v) => set({ time_zone: v })}>
            <SelectTrigger id="time-zone" className="w-full sm:w-72"><SelectValue /></SelectTrigger>
            <SelectContent className="max-h-72">
              {zones.map((z) => <SelectItem key={z} value={z}>{z.replaceAll("_", " ")}</SelectItem>)}
            </SelectContent>
          </Select>
          <p className="text-sm text-muted-foreground">
            For schedules, like backups at 03:00 your time.
            {serverZone && <> This server's own clock is set to {serverZone}; Linx doesn't change it.</>}
          </p>
          <FieldMessage message={errorFor("time_zone")} />
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
      <div className="mt-2"><FieldMessage message={(touched && termsErr) || errorFor("agreed_to_terms")} /></div>
      {problem && <div className="mt-4"><FieldMessage message={problem} /></div>}
      <Nav onBack={onBack} next="Check and get a certificate" busy={busy} />
    </form>
  );
}
