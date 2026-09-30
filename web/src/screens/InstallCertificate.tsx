// The web install's certificate page (docs/ui/INSTALL_SCREENS.md §2.6–2.7)
// and the secure page's arrival (§3.1). The server does the work (linx
// setup on the host, docs/INSTALL.md §4); this page only shows where it's
// got to, looking every five seconds, and asks for the few things only a
// person can do: set up the front door, add the DNS record, or give the
// DNS token when port 443 can't reach Linx.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { LoaderCircle, LockKeyhole, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup } from "@/components/ui/radio-group";
import { FrontDoorCard } from "@/components/FrontDoorCard";
import {
  Choice, CopyBlock, Countdown, Detail, Disclosure, FieldMessage, Frame, LinkUnusable, RecordBox, Row, submit, Title, useSecondsLeft, type Mark,
} from "@/components/InstallFrame";
import { SecureFinish } from "@/screens/InstallFinish";
import {
  canOpen, frontDoorReady, getState, LinkClosed, newHandoff, Problem, redeemHandoff, retryCertificate, sendToken,
  type Answers, type CertView, type DNSState, type Facts, type InstallState, type Stage,
} from "@/lib/install";

const POLL_MS = 5000;

/** The plain page after "Check and get a certificate". */
export function CertificateStep({ initial, answers, facts, onClosed }: {
  initial: InstallState; answers: Answers; facts: Facts; onClosed: () => void;
}) {
  const [cert, setCert] = useState<CertView | undefined>(initial.cert);
  const [problem, setProblem] = useState("");
  const refresh = useRef(async () => {
    try {
      const s = await getState();
      setCert(s.cert);
    } catch (e) {
      if (e instanceof LinkClosed) onClosed();
    }
  });
  useEffect(() => {
    const r = refresh.current;
    void r();
    const t = setInterval(() => void r(), POLL_MS);
    return () => clearInterval(t);
  }, []);
  const act = async (fn: () => Promise<void>) => {
    setProblem("");
    try {
      await fn();
      await refresh.current();
    } catch (e) {
      if (e instanceof LinkClosed) onClosed();
      else setProblem(e instanceof Problem ? e.message : "Can't reach this server. Check your connection and try again.");
    }
  };

  const domain = answers.domain.trim().toLowerCase();
  const [tokenFirst, setTokenFirst] = useState(false);
  if (!cert) {
    return (
      <div>
        <Title lead="Linx checked them and saved them on the server.">Your answers are saved</Title>
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Getting ready for the certificate…
        </p>
      </div>
    );
  }
  const ready = cert.certificate.state === "ok";
  return (
    <div>
      <Title lead="A certificate is what makes the padlock appear in your browser.">
        <span className="break-words">Getting a certificate for {domain}</span>
      </Title>
      {cert.prepare.state === "failed" && (
        <Failure onRetry={() => void act(retryCertificate)}>
          Linx couldn't start its web port on this server.{" "}
          {cert.prepare.detail?.includes("443") && "Something else may be using port 443. "}
          <Detail text={cert.prepare.detail} />
        </Failure>
      )}
      {cert.mode === "port443" && cert.reach.state !== "running" && cert.reach.state !== "ok" && !cert.certificate.state && (
        <WayChooser tokenFirst={tokenFirst} onChange={setTokenFirst} />
      )}
      {cert.mode === "token"
        ? <TokenRows cert={cert} answers={answers} facts={facts} act={act} />
        : tokenFirst
          ? <TokenForm domain={domain} act={act} chosen />
          : <Port443Rows cert={cert} answers={answers} facts={facts} act={act} />}
      {problem && <div className="mt-4"><FieldMessage message={problem} /></div>}
      {ready && cert.secure_url && <MoveToSecure url={cert.secure_url} />}
      <Details cert={cert} />
    </div>
  );
}

type RowsProps = { cert: CertView; answers: Answers; facts: Facts; act: (fn: () => Promise<void>) => Promise<void> };

function Port443Rows({ cert, answers, facts, act }: RowsProps) {
  const setupFirst = !!cert.setup && !cert.setup.done;
  const dnsOK = cert.dns.state === "ok";
  let n = 0;
  return (
    <ol className="flex flex-col gap-5">
      {cert.setup && (
        <Row n={++n} state={cert.setup.done ? "ok" : "todo"} title={setupTitle(cert, facts)}>
          <SetupSteps cert={cert} onDone={() => void act(frontDoorReady)} />
        </Row>
      )}
      <Row n={++n} state={dnsOK ? "ok" : "waiting"} title={`Add these ${cert.add_records?.length ?? 2} records at your DNS company`}>
        <div className="flex flex-col gap-3">
          {cert.add_records?.map((r) => {
            const seen = cert.dns.names?.find((x) => x.name === r.name);
            return (
              <div key={r.name}>
                <RecordBox record={r} />
                <p className="mt-1 text-sm" aria-live="polite">{dnsWords(seen)}</p>
              </div>
            );
          })}
        </div>
        {cert.dns.checked_at && <p className="mt-2 text-sm text-muted-foreground">Checked {new Date(cert.dns.checked_at).toLocaleTimeString()}. Changes can take a few minutes to show.</p>}
        {answers.where === "home" && (
          <p className="mt-1 text-sm text-muted-foreground">
            If your home address changes, Linx will keep this record up to date once you've added your token on the next page.
          </p>
        )}
      </Row>
      <Row n={++n} state={stageMark(cert.reach, !dnsOK || setupFirst)} title="Let's Encrypt reaches this server on port 443">
        {cert.reach.state === "running" && <p className="text-sm text-muted-foreground">Checking… this takes about a minute. <SoFar since={cert.reach.at} /></p>}
        {cert.reach.state === "failed" && (
          <Failure onRetry={() => void act(retryCertificate)} title="Let's Encrypt couldn't reach this server on port 443">
            {problemWords(cert.reach, cert, facts)} <Detail text={cert.reach.detail} />
          </Failure>
        )}
      </Row>
      <Row n={++n} state={stageMark(cert.certificate, cert.reach.state !== "ok")} title="Certificate ready">
        <CertificateStage cert={cert} facts={facts} act={act} />
      </Row>
    </ol>
  );
}

function TokenRows({ cert, answers, facts, act }: RowsProps) {
  let n = 0;
  return (
    <ol className="flex flex-col gap-5">
      {cert.setup && (
        <Row n={++n} state={cert.setup.done ? "ok" : "todo"} title={setupTitle(cert, facts)}>
          <SetupSteps cert={cert} onDone={() => void act(frontDoorReady)} />
        </Row>
      )}
      <Row n={++n} state={cert.token_saved ? "ok" : "todo"} title="Your DNS company's token">
        {cert.token_saved
          ? <p className="text-sm text-muted-foreground">Saved on the server.</p>
          : <TokenForm domain={answers.domain.trim().toLowerCase()} act={act} />}
      </Row>
      <Row n={++n} state={stageMark(cert.records, !cert.token_saved)} title="Linx points your names at this server">
        {cert.records.state === "failed" && (
          <Failure onRetry={() => void act(retryCertificate)} title="Linx couldn't change your DNS records">
            Check the token can edit DNS for {cert.domain}. <Detail text={cert.records.detail} />
          </Failure>
        )}
      </Row>
      <Row n={++n} state={stageMark(cert.certificate, cert.records.state !== "ok")} title="Certificate ready">
        <CertificateStage cert={cert} facts={facts} act={act} />
      </Row>
    </ol>
  );
}

/** "1:12 so far", ticking every second, so a long wait never looks stuck. */
function SoFar({ since }: { since?: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const start = since ? new Date(since).getTime() : NaN;
  if (!Number.isFinite(start)) return null;
  const secs = Math.max(0, Math.floor((now - start) / 1000));
  return <span className="tabular-nums">({Math.floor(secs / 60)}:{String(secs % 60).padStart(2, "0")} so far)</span>;
}

function CertificateStage({ cert, facts, act }: Pick<RowsProps, "cert" | "facts" | "act">) {
  const s = cert.certificate;
  if (s.state === "running") {
    return (
      <p className="text-sm text-muted-foreground">
        {cert.mode === "token"
          ? "Getting it… usually two or three minutes: Linx waits for its DNS record to reach the whole internet before Let's Encrypt looks."
          : "Getting it… usually under a minute."}{" "}
        <SoFar since={s.at} />
      </p>
    );
  }
  if (s.state === "failed") {
    return (
      <Failure onRetry={() => void act(retryCertificate)} title="Linx couldn't get the certificate">
        {problemWords(s, cert, facts)} <Detail text={s.detail} />
      </Failure>
    );
  }
  return null;
}

function stageMark(s: Stage, later: boolean): Mark {
  if (s.state === "ok" || s.state === "running" || s.state === "failed") return s.state;
  return later ? "later" : "waiting";
}

function setupTitle(cert: CertView, facts: Facts): string {
  switch (cert.front_door) {
    case "proxy": case "pangolin": case "nginx": return "Set up your front door";
    case "http-proxy": return "Set up your proxy";
    default: return facts.lan_address ? "Set up your router" : "Set up port 443";
  }
}

function SetupSteps({ cert, onDone }: { cert: CertView; onDone: () => void }) {
  const s = cert.setup!;
  if (s.card) {
    const card = (
      <FrontDoorCard card={s.card} router={s.steps}>
        <div className="flex items-start gap-3">
          <Checkbox id="door-done" checked={!!s.done} disabled={!!s.done} onCheckedChange={(c) => { if (c === true) onDone(); }} className="mt-0.5" />
          <Label htmlFor="door-done" className="font-normal leading-snug">I've done these steps</Label>
        </div>
      </FrontDoorCard>
    );
    return s.done ? <Disclosure label="Show the steps again">{card}</Disclosure> : card;
  }
  return (
    <div className="flex flex-col gap-3">
      <ul className="list-disc space-y-1 ps-5 text-sm">
        {s.steps?.map((line) => <li key={line}>{line}</li>)}
      </ul>
      {s.files?.map((f) => <Disclosure key={f.title} label={`Show ${f.title}`}>
        {f.path && <p className="mb-2 text-sm text-muted-foreground">Goes in {f.path}.</p>}
        <CopyBlock text={f.text} label={f.title} />
      </Disclosure>)}
      <div className="flex items-start gap-3">
        <Checkbox id="door-done" checked={!!s.done} disabled={!!s.done} onCheckedChange={(c) => { if (c === true) onDone(); }} className="mt-0.5" />
        <Label htmlFor="door-done" className="font-normal leading-snug">I've done this</Label>
      </div>
    </div>
  );
}

function dnsWords(seen?: { state: DNSState; seen?: string[] }): string {
  switch (seen?.state) {
    case "ok": return "✓ Points here.";
    case "wrong": return `Still points at ${seen.seen?.join(", ")}.`;
    case "missing": return "Not found yet.";
    case "error": return "Linx couldn't ask your DNS company's servers just now; it keeps trying.";
  }
  return "Checking…";
}

/** What went wrong, in words tied to the front door (§2.7 row 3). */
function problemWords(s: Stage, cert: CertView, facts: Facts): string {
  const name = cert.domain;
  const route = (() => {
    switch (cert.front_door) {
      case "proxy": case "pangolin": case "nginx":
        return "Check that your router sends TCP port 443 to your front door, and that it does the three things in step 1.";
      case "linx-443":
        return facts.lan_address
          ? `Check that your router sends TCP port 443 to ${facts.lan_address}.`
          : "Check that your server provider's firewall allows TCP port 443 to this server.";
      default: return "";
    }
  })();
  switch (s.kind) {
    case "connection": return `It couldn't connect. ${route}`;
    case "wrong_answer": return `Something other than Linx answered on port 443 for ${name}. ${route}`;
    case "dns": return `It couldn't find ${name} in DNS yet. Changes can take a few minutes; try again shortly.`;
    case "rate_limited": return "Let's Encrypt has had too many tries for this domain. Wait an hour, then try again.";
  }
  return "Something went wrong.";
}

function Failure({ title, onRetry, children }: { title?: string; onRetry: () => void; children: ReactNode }) {
  return (
    <div role="alert" className="flex flex-col items-start gap-2 rounded-md border border-destructive/40 p-3 text-sm">
      {title && <p className="font-medium">{title}</p>}
      <p>{children}</p>
      <Button type="button" variant="outline" size="sm" onClick={onRetry}>Try again</Button>
    </div>
  );
}

function Details({ cert }: { cert: CertView }) {
  return (
    <div className="mt-6 border-t pt-4">
      <Disclosure label="Details">
        <ul className="list-disc space-y-1 ps-5 text-sm text-muted-foreground">
          {cert.mode === "port443"
            ? <li>Let's Encrypt checks the names on port 443 (TLS-ALPN-01): first its test service, then the real one.</li>
            : <li>Linx proves the domain is yours through your DNS company (DNS-01), then gets a certificate for every name under it.</li>}
          {[cert.prepare, cert.reach, cert.records, cert.certificate].filter((s) => s.detail).map((s, i) => (
            <li key={i} className="break-words font-mono text-xs">{s.detail}</li>
          ))}
          <li>Check it yourself: <code className="font-mono">dig {cert.domain}</code> and <code className="font-mono">dig turn.{cert.domain}</code></li>
        </ul>
      </Disclosure>
    </div>
  );
}

/**
 * The token, on this unencrypted page (§2.6): only after the warning is
 * ticked, never remembered by the browser.
 */
function TokenForm({ domain, act, chosen = false }: { domain: string; act: RowsProps["act"]; chosen?: boolean }) {
  const [trusted, setTrusted] = useState(false);
  const [token, setToken] = useState("");
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const duck = domain.endsWith(".duckdns.org");
  return (
    <form className="flex flex-col gap-4" onSubmit={submit(() => {
      setBusy(true);
      void act(() => sendToken(token.trim())).finally(() => { setBusy(false); setToken(""); });
    })}>
      <div className="rounded-md border border-status-away p-4 text-sm">
        <p className="flex items-center gap-2 font-medium">
          <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-status-away" />This page's certificate is temporary
        </p>
        <p className="mt-2">
          {chosen
            ? "The token goes to this server encrypted. "
            : "With what's in front of this server, Linx can't get its certificate through port 443, so it needs your DNS company's token here, before there's a secure page. It goes to this server encrypted. "}
          But your browser couldn't check this page's certificate: if you compared its fingerprint with the one setup printed on the server,
          only this server can read the token. If you didn't, someone in the middle of your connection could. Make a token that can only
          change {domain}'s DNS.
        </p>
        <div className="mt-3 flex items-start gap-3">
          <Checkbox id="trusted" checked={trusted} onCheckedChange={(c) => setTrusted(c === true)} className="mt-0.5" />
          <Label htmlFor="trusted" className="font-normal leading-snug">I checked the fingerprint, or I trust this network</Label>
        </div>
      </div>
      <p className="text-sm">DNS company: <span className="font-medium">{duck ? "DuckDNS" : "Cloudflare"}</span></p>
      <div className="flex flex-col gap-2">
        <Label htmlFor="token">Token</Label>
        <div className="flex gap-2">
          <Input id="token" type={show ? "text" : "password"} autoComplete="off" spellCheck={false} disabled={!trusted}
            value={token} onChange={(e) => setToken(e.target.value)} className="min-w-0 flex-1" />
          <Button type="button" variant="outline" disabled={!trusted} onClick={() => setShow(!show)}>{show ? "Hide" : "Show"}</Button>
        </div>
        {duck
          ? <p className="text-sm text-muted-foreground">Your token is at the top of duckdns.org once you've signed in.</p>
          : <Disclosure label="How to make a Cloudflare token">
            <p className="text-sm text-muted-foreground">
              In Cloudflare: My Profile → API Tokens → Create Token → “Edit zone DNS”. Under Zone Resources choose only {domain}. Create it
              and copy it here.
            </p>
          </Disclosure>}
      </div>
      <div className="flex justify-end">
        <Button type="submit" disabled={!trusted || !token.trim() || busy}>
          {busy && <LoaderCircle aria-hidden="true" className="animate-spin" />}Get the certificate
        </Button>
      </div>
    </form>
  );
}

/**
 * The two ways to the certificate on the port 443 page (docs/INSTALL.md §14
 * item 1): add the two records by hand (the token comes later, on the
 * secure page), or give the token now and Linx does the rest.
 */
function WayChooser({ tokenFirst, onChange }: { tokenFirst: boolean; onChange: (b: boolean) => void }) {
  return (
    <fieldset className="mb-6">
      <legend className="mb-3 text-sm font-medium">How should Linx get it?</legend>
      <RadioGroup value={tokenFirst ? "token" : "records"} onValueChange={(v) => onChange(v === "token")}
        aria-label="How should Linx get the certificate" className="gap-3">
        <Choice id="way-token" value="token" title="With my DNS company's token" badge="Fastest"
          hint="Linx adds every DNS record itself and gets the full certificate at once. Nothing to add by hand." />
        <Choice id="way-records" value="records" title="Not now: I'll add 2 records"
          hint="Let's Encrypt checks this server on port 443. You can give the token later, on the secure page." />
      </RadioGroup>
    </fieldset>
  );
}

/**
 * Once the certificate is ready: check this browser can open the secure
 * address (its DNS may still be catching up), then move there with a
 * one-time handoff, made fresh each time.
 */
function MoveToSecure({ url }: { url: string }) {
  const [phase, setPhase] = useState<"checking" | "blocked" | "moving">("checking");
  const [problem, setProblem] = useState("");
  const tries = useRef(0);
  const go = useRef(async () => {
    setProblem("");
    setPhase("checking");
    if (!(await canOpen(url))) {
      // The page may be older than the certificate: its security policy,
      // fixed when it loaded, then doesn't allow the check yet (the server
      // adds the secure address once the certificate is ready). Load the
      // page again, once, to get the current one (found on the VPS demo).
      if (reloadOnce(url)) return;
      setPhase("blocked");
      return;
    }
    try {
      const handoff = await newHandoff();
      setPhase("moving");
      window.location.assign(`${url}/install/continue#${handoff}`);
    } catch (e) {
      setPhase("blocked");
      setProblem(e instanceof Problem ? e.message : "");
    }
  });
  useEffect(() => {
    const g = go.current;
    void g();
    // A home DNS server that hasn't caught up: look again every so often.
    const t = setInterval(() => { if (tries.current++ < 24) void g(); }, POLL_MS * 2);
    return () => clearInterval(t);
  }, [url]);
  return (
    <div className="mt-6 rounded-md border p-4 text-sm" aria-live="polite">
      {phase !== "blocked"
        ? <p className="flex items-center gap-2"><LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Moving you to <span className="font-mono break-all">{url}</span>…</p>
        : (
          <div className="flex flex-col items-start gap-3">
            <p>Your browser can't open <span className="font-mono break-all">{url}</span> yet. Wait a minute and try again.</p>
            {problem && <FieldMessage message={problem} />}
            <Button type="button" onClick={() => void go.current()}>Open it</Button>
          </div>
        )}
    </div>
  );
}

/** Reloads the page once per secure address; false when it already did. */
function reloadOnce(url: string): boolean {
  const key = "linx-install-reloaded:" + url;
  try {
    if (sessionStorage.getItem(key)) return false;
    sessionStorage.setItem(key, "1");
  } catch {
    return false; // no storage: don't risk reloading in a loop
  }
  window.location.reload();
  return true;
}

/** https://<domain>: /install/continue#<handoff>, then /install. */
export function SecureInstall() {
  const [state, setState] = useState<InstallState | "loading" | "closed" | "bad-handoff">("loading");
  useEffect(() => {
    const handoff = window.location.hash.slice(1);
    const start = async () => {
      if (window.location.pathname === "/install/continue") {
        // Out of the address bar and the history at once, used or not.
        history.replaceState(null, "", "/install/continue");
        if (!handoff) return setState("bad-handoff");
        try {
          await redeemHandoff(handoff);
        } catch {
          return setState("bad-handoff");
        }
        history.replaceState(null, "", "/install");
      }
      try {
        setState(await getState());
      } catch {
        setState("closed");
      }
    };
    void start();
  }, []);
  if (state === "loading") return <main className="min-h-dvh bg-background" aria-busy="true" />;
  if (state === "closed") return <LinkUnusable />;
  if (state === "bad-handoff") {
    return (
      <Frame strip={false}>
        <Title>This link can't be used</Title>
        <p className="text-sm text-muted-foreground">
          Links to the secure page work once, for two minutes. Go back to the first page and press <strong>Open it</strong> again.
        </p>
      </Frame>
    );
  }
  return <SecureArrive state={state} onClosed={() => setState("closed")} />;
}

function SecureArrive({ state, onClosed }: { state: InstallState; onClosed: () => void }) {
  // Back on a page that's past arriving (a reload): straight to where it was.
  const past = !!state.finish && (!!state.finish.token || !!state.finish.install.state);
  const [next, setNext] = useState(past);
  const left = useSecondsLeft(state.expires_in);
  useEffect(() => { if (left === 0 && !next) onClosed(); }, [left, next, onClosed]);
  if (next) return <SecureFinish initial={state} />;
  return (
    <Frame at={4} strip={false} footer={<Countdown left={left} secure />}>
      <form onSubmit={submit(() => setNext(true))}>
        <Title lead="From here on, everything you type is encrypted.">
          <span className="flex items-center gap-2"><LockKeyhole aria-hidden="true" className="size-5 text-status-available" />You're on the secure page now</span>
        </Title>
        <div className="flex justify-end"><Button type="submit">Continue</Button></div>
      </form>
    </Frame>
  );
}
