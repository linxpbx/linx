// The secure page's steps (docs/ui/INSTALL_SCREENS.md §3.2, §3.4, §3.5;
// docs/INSTALL.md §5 and §14): the DNS company's token, a few extras, then
// Install with its progress list. Its last steps replace this page's
// server with the full Linx, so the page then waits for Linx to answer at
// the same address and moves to the first sign-in (the set-password link
// the server made before the switch).
import { useEffect, useRef, useState } from "react";
import { CircleCheck, LoaderCircle, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { RadioGroup } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { DnsKeyFields } from "@/components/DnsKeyForm";
import { companyById, dnsKeyBody, dnsKeyFilled, startingCompany, type DnsKeyValue } from "@/lib/dnsCompanies";
import {
  Choice, CopyButton, Countdown, FieldMessage, Frame, LinkUnusable, Nav, PortainerNote, Row, submit, Title, useSecondsLeft, type Mark,
} from "@/components/InstallFrame";
import {
  getState, LinkClosed, linxAnswers, Problem, saveExtras, sendToken, setupLinkReady, skipToken, startInstall,
  type FinishView, type InstallState, type InstallStep,
} from "@/lib/install";

const POLL_MS = 3000;

/** Where the progress line is: DNS, then Install (Sign-in comes after, on the full Linx). */
const AT_DNS = 4;
const AT_INSTALL = 5;

export function SecureFinish({ initial }: { initial: InstallState }) {
  const [state, setState] = useState<InstallState>(initial);
  const [closed, setClosed] = useState(false);
  const [editing, setEditing] = useState<"token" | "extras" | null>(null);
  const [problem, setProblem] = useState("");
  const left = useSecondsLeft(initial.expires_in);
  const f: FinishView = state.finish ?? { install: {} };
  const switching = !!f.switching;
  const installing = f.install.state === "running" || f.install.state === "ok" || switching;

  const running = useRef(false);
  running.current = f.install.state === "running";
  const refresh = useRef(async () => {
    try {
      setState(await getState());
    } catch (e) {
      if (!(e instanceof LinkClosed)) return;
      // While installing, the installer's page going away is the switch to
      // the full Linx (its "switching" news can be missed by a moment), not
      // the link closing (found in the install demo: "This link can't be
      // used" flashed up during the switch).
      if (running.current) setState((s) => ({ ...s, finish: { ...(s.finish ?? { install: {} }), switching: true } }));
      else setClosed(true);
    }
  });
  useEffect(() => {
    if (switching) return; // this page's server is going away
    const r = refresh.current;
    const t = setInterval(() => void r(), POLL_MS);
    return () => clearInterval(t);
  }, [switching]);
  useEffect(() => { if (left === 0 && !installing) setClosed(true); }, [left, installing]);

  const act = async (fn: () => Promise<void>): Promise<boolean> => {
    setProblem("");
    try {
      await fn();
      await refresh.current();
      return true;
    } catch (e) {
      if (e instanceof LinkClosed) setClosed(true);
      else setProblem(e instanceof Problem ? e.message : "Can't reach this server. Check your connection and try again.");
      return false;
    }
  };

  if (closed && !switching) return <LinkUnusable />;
  const domain = state.cert?.domain ?? "";
  const home = state.facts.where === "home" || state.accepted?.where === "home";
  const failed = f.install.state === "failed";
  const step = editing ?? (installing || failed ? "progress" : !f.token ? "token" : "extras");
  return (
    <Frame at={step === "token" ? AT_DNS : AT_INSTALL} strip={false} footer={switching ? undefined : <Countdown left={left} secure />}>
      {step === "token" && (
        <TokenStep finish={f} domain={domain} home={home} problem={problem} act={act}
          detected={state.cert?.dns.company} address={state.facts.public_address}
          onNext={() => { setProblem(""); setEditing(null); }} />
      )}
      {step === "extras" && (
        <ExtrasStep finish={f} problem={problem} act={act} onBack={() => { setProblem(""); setEditing("token"); }}
          onInstalling={() => setEditing(null)} />
      )}
      {step === "progress" && (
        <ProgressStep finish={f} domain={domain} problem={problem}
          onRetry={() => void act(startInstall)}
          onChange={(what) => { setProblem(""); setEditing(what); }} />
      )}
    </Frame>
  );
}

type Act = (fn: () => Promise<void>) => Promise<boolean>;

/** §3.2: the DNS company's token, or Skip on a rented server where Linx takes 443. */
function TokenStep({ finish, domain, home, problem, act, onNext, detected, address }: {
  finish: FinishView; domain: string; home: boolean; problem: string; act: Act; onNext: () => void; detected?: string; address?: string;
}) {
  const duck = domain.endsWith(".duckdns.org");
  const company = companyById(finish.provider)?.name ?? "your DNS company";
  const [replace, setReplace] = useState(false);
  const [key, setKey] = useState<DnsKeyValue>({ provider: startingCompany(domain, finish.token === "saved" ? finish.provider : undefined, detected), key: {} });
  const [busy, setBusy] = useState(false);

  const lead = (
    <ul className="mt-2 list-disc space-y-1 ps-5">
      <li>keeps {domain} and turn.{domain} pointing at this server, even when its address changes</li>
      {home && !duck && <li>adds sip.{domain} for your desk phones, at this server's home address</li>}
      <li>gets a certificate that covers every name under {domain}</li>
    </ul>
  );
  if (finish.token && !replace) {
    return (
      <form onSubmit={submit(onNext)}>
        <Title lead={finish.token === "saved" ? undefined : "The certificate still renews by itself. You'll add and change DNS records yourself."}>
          {finish.token === "saved" ? "Let Linx look after your DNS" : "No DNS token"}
        </Title>
        {finish.token === "saved"
          ? <p className="flex items-center gap-2 text-sm"><CircleCheck aria-hidden="true" className="size-4 text-status-available" />{company} key added</p>
          : <p className="text-sm text-muted-foreground">You chose to skip it.</p>}
        <div className="mt-8 flex flex-wrap items-center justify-end gap-3">
          <Button type="button" variant="outline" onClick={() => setReplace(true)}>{finish.token === "saved" ? "Replace" : "Add a key"}</Button>
          <Button type="submit">Next</Button>
        </div>
      </form>
    );
  }
  const save = () => {
    setBusy(true);
    void act(() => sendToken(dnsKeyBody(key))).then((ok) => {
      setBusy(false);
      setKey({ provider: key.provider, key: {} });
      if (ok) { setReplace(false); onNext(); }
    });
  };
  return (
    <form onSubmit={submit(save)}>
      <Title lead={<>With a key from your DNS company, Linx:{lead}</>}>Let Linx look after your DNS</Title>
      <DnsKeyFields idPrefix="dns" domain={domain} address={address} detected={detected} value={key} onChange={setKey} disabled={busy} />
      <p className="mt-3 text-sm text-muted-foreground">Linx checks the key can see {domain} before saving it.</p>
      {problem && <div className="mt-4"><FieldMessage message={problem} /></div>}
      <div className="mt-8 flex flex-wrap items-center justify-end gap-3">
        {finish.token && <Button type="button" variant="outline" onClick={() => setReplace(false)} disabled={busy}>Cancel</Button>}
        {!finish.token && finish.skip_allowed && (
          <Button type="button" variant="outline" disabled={busy}
            onClick={() => void act(skipToken).then((ok) => { if (ok) onNext(); })}>Skip</Button>
        )}
        <Button type="submit" disabled={busy || !dnsKeyFilled(key)}>
          {busy && <LoaderCircle aria-hidden="true" className="animate-spin" />}Check and save
        </Button>
      </div>
      {!finish.token && !finish.skip_allowed && (
        <p className="mt-3 text-end text-sm text-muted-foreground">
          {home
            ? duck
              ? "At home, Linx needs it to keep your address up to date."
              : `At home your desk phones need sip.${domain}, which only Linx can keep up to date.`
            : "With what's in front of this server, Linx renews its certificate through your DNS company."}
        </p>
      )}
      {!finish.token && finish.skip_allowed && (
        <p className="mt-3 text-end text-sm text-muted-foreground">Skip: the certificate still renews by itself. You'll add and change DNS records yourself.</p>
      )}
    </form>
  );
}

const SIZE_NAMES: Record<string, string> = { lite: "Lite", standard: "Standard", performance: "Performance" };

/** §3.4: the size of server and Portainer, then Install. */
function ExtrasStep({ finish, problem, act, onBack, onInstalling }: {
  finish: FinishView; problem: string; act: Act; onBack: () => void; onInstalling: () => void;
}) {
  const pick = finish.profile_pick ?? "standard";
  const [profile, setProfile] = useState(finish.extras?.profile || pick);
  const [portainer, setPortainer] = useState(!!finish.extras?.portainer && !!finish.portainer_allowed);
  const [busy, setBusy] = useState(false);
  const install = () => {
    setBusy(true);
    void act(async () => {
      await saveExtras({ profile: profile === pick ? "" : profile, portainer });
      await startInstall();
    }).then((ok) => {
      setBusy(false);
      if (ok) onInstalling();
    });
  };
  return (
    <form onSubmit={submit(install)}>
      <Title lead="You can change these later.">A few extras</Title>
      <fieldset>
        <legend className="mb-3 text-sm font-medium">Size of this server</legend>
        <RadioGroup value={profile} onValueChange={setProfile} aria-label="Size of this server" className="gap-3">
          {(finish.profiles ?? []).map((p) => (
            <Choice key={p.name} id={`size-${p.name}`} value={p.name} title={SIZE_NAMES[p.name] ?? p.name}
              badge={p.name === pick ? "Recommended" : undefined}
              hint={<>{p.description.charAt(0).toUpperCase() + p.description.slice(1)}.{p.name === pick && finish.profile_reason && <> Picked for this server: {finish.profile_reason}.</>}</>} />
          ))}
        </RadioGroup>
      </fieldset>
      {finish.portainer_allowed && (
        <div className="mt-6 flex items-start justify-between gap-4 rounded-md border p-4">
          <div className="min-w-0">
            <Label htmlFor="portainer" className="text-sm font-medium">Portainer</Label>
            <p className="mt-0.5 text-sm text-muted-foreground">
              A web page to look at this server's Docker containers, on your home network only. It can control everything on the server.
            </p>
          </div>
          <Switch id="portainer" checked={portainer} onCheckedChange={setPortainer} />
        </div>
      )}
      {!finish.portainer_allowed && <PortainerNote className="mt-6" />}
      {problem && <div className="mt-4"><FieldMessage message={problem} /></div>}
      <Nav onBack={onBack} next="Install" busy={busy} />
    </form>
  );
}

function rowMark(s: InstallStep, after: boolean): Mark {
  if (s.state === "ok" || s.state === "running" || s.state === "failed") return s.state;
  return after ? "later" : "waiting";
}

/** §3.5: the install's rows as the server does them, and what to write down. */
function ProgressStep({ finish, domain, problem, onRetry, onChange }: {
  finish: FinishView; domain: string; problem: string; onRetry: () => void; onChange: (what: "token" | "extras") => void;
}) {
  const steps = finish.steps ?? [];
  const failed = finish.install.state === "failed";
  // What to write down, as first shown: kept on this page until it's ticked,
  // whatever later updates say (found on the VPS demo: the server wipes it
  // when the install finishes, the next update came without it, and the
  // page moved on to the sign-in with the CA's passphrase unticked).
  const [keep, setKeep] = useState<NonNullable<FinishView["keep"]>>([]);
  const latest = finish.keep;
  useEffect(() => {
    if (!latest?.length) return;
    setKeep((seen) => [...seen, ...latest.filter((k) => !seen.some((s) => s.title === k.title))]);
  }, [latest]);
  const [wrote, setWrote] = useState(false);
  const firstPending = steps.findIndex((s) => s.state !== "ok");
  return (
    <div>
      <Title lead={failed ? undefined : "This takes a few minutes. Keep this page open: when Linx has started, it takes you to your first sign-in."}>
        {failed ? "The install stopped" : "Installing Linx"}
      </Title>
      <ol className="flex flex-col gap-3" aria-label="Install steps">
        {steps.map((s, i) => (
          <Row key={s.title} n={i + 1} state={rowMark(s, i > firstPending && firstPending >= 0)} title={s.title}>
            {s.state === "failed" && s.detail && <p className="text-sm break-words text-muted-foreground">{s.detail}</p>}
          </Row>
        ))}
      </ol>
      {failed && (
        <div role="alert" className="mt-6 flex flex-col items-start gap-3 rounded-md border border-destructive/40 p-4 text-sm">
          <p>Nothing that finished is undone, and every step is safe to repeat.</p>
          <div className="flex flex-wrap gap-3">
            <Button type="button" onClick={onRetry}>Try again</Button>
            <Button type="button" variant="outline" onClick={() => onChange("token")}>Change the DNS token</Button>
            <Button type="button" variant="outline" onClick={() => onChange("extras")}>Change the extras</Button>
          </div>
        </div>
      )}
      {problem && <div className="mt-4"><FieldMessage message={problem} /></div>}
      {keep.length > 0 && <KeepBox keep={keep} wrote={wrote} setWrote={setWrote} />}
      {finish.switching && finish.sign_in_path && (
        <MoveToSignIn path={finish.sign_in_path} domain={domain} ready={keep.length === 0 || wrote} />
      )}
    </div>
  );
}

function KeepBox({ keep, wrote, setWrote }: { keep: FinishView["keep"] & object; wrote: boolean; setWrote: (b: boolean) => void }) {
  return (
    <div className="mt-6 rounded-md border border-status-away p-4 text-sm">
      <p className="flex items-center gap-2 font-medium">
        <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-status-away" />Write these down now
      </p>
      <p className="mt-1 text-muted-foreground">They're shown only once and aren't kept anywhere Linx can show them again.</p>
      <dl className="mt-3 flex flex-col gap-4">
        {keep.map((k) => (
          <div key={k.title} className="min-w-0">
            <dt className="font-medium">{k.title}</dt>
            <dd className="mt-1 flex min-w-0 items-center gap-1">
              <code className="min-w-0 rounded-md bg-muted px-2 py-1 font-mono break-all">{k.value}</code>
              <CopyButton text={k.value} label={k.title} />
            </dd>
            {k.note && <dd className="mt-1 text-muted-foreground">{k.note}</dd>}
          </div>
        ))}
      </dl>
      <div className="mt-4 flex items-start gap-3">
        <Checkbox id="wrote" checked={wrote} onCheckedChange={(c) => setWrote(c === true)} className="mt-0.5" />
        <Label htmlFor="wrote" className="font-normal leading-snug">I've written these down</Label>
      </div>
    </div>
  );
}

/**
 * After the switch: wait for the full Linx to answer here, then open the
 * first sign-in. Once someone's already set an account up (a second try),
 * the ordinary sign-in instead.
 */
function MoveToSignIn({ path, domain, ready }: { path: string; domain: string; ready: boolean }) {
  const [phase, setPhase] = useState<"starting" | "up" | "slow">("starting");
  const [target, setTarget] = useState<string | null>(null);
  useEffect(() => {
    let stop = false;
    const started = Date.now();
    const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
    void (async () => {
      while (!stop && !(await linxAnswers())) {
        if (Date.now() - started > 10 * 60_000) { setPhase("slow"); }
        await wait(POLL_MS);
      }
      if (stop) return;
      setPhase("up");
      // The first admin is made just after Linx starts, so the link can be
      // "not there" for a while yet (found in the install demo: the page
      // gave up after four tries and went to the ordinary sign-in). Every 5
      // s for up to 3 minutes stays under the per-address limit on failed
      // tries (20 a minute); still nothing means someone already set up an
      // account (a second install), so the ordinary sign-in.
      const until = Date.now() + 3 * 60_000;
      while (!stop && Date.now() < until) {
        await wait(5000);
        if ((await setupLinkReady(path)) === true) return setTarget(path);
      }
      if (!stop) setTarget("/");
    })();
    return () => { stop = true; };
  }, [path]);
  useEffect(() => { if (target && ready) window.location.assign(target); }, [target, ready]);
  return (
    <div className="mt-6 rounded-md border p-4 text-sm" aria-live="polite">
      {phase === "slow" ? (
        <p>
          Linx is taking longer than usual to start. On the server, <code className="font-mono">sudo linx setup</code> shows where it's up to.
          This page keeps checking.
        </p>
      ) : target && !ready ? (
        <p>Linx is ready. Tick “I've written these down” to go to your first sign-in.</p>
      ) : (
        <p className="flex items-center gap-2">
          <LoaderCircle aria-hidden="true" className="size-4 shrink-0 animate-spin" />
          {phase === "starting"
            ? <span>Linx is starting at <span className="font-mono break-all">https://{domain}</span>. The installer's first page (port 6464) is now closed for good.</span>
            : <span>Linx is up. Opening your first sign-in…</span>}
        </p>
      )}
    </div>
  );
}
