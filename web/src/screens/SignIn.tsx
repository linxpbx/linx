// Sign-in (docs/ui/WEB_SCREENS_PHASE1C.md §1, ADMIN_SCREENS_PHASE1E.md
// §3.1 and §12.1): a passkey, or email + password then the second step
// (authenticator code, passkey or recovery code) when the account has one.
// On first use (a set-password link) the person chooses how to sign in:
// passkey (recommended), password + authenticator app, or password only
// (a warning for admins). "Forgot your password?" (ADR-067) emails a reset
// link, whose page asks for the new password and then the second step
// before anything changes. One step shown at a time.
import { createContext, useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Building2, CircleAlert, CircleCheck, KeyRound, TriangleAlert } from "lucide-react";
import { api, problemCode, problemMessage } from "@/api/client";
import {
  autofillSupported, cancelled, createPasskey, deviceName, PasskeyError, passkeysSupported, savePasskey, answerWithPasskey,
} from "@/lib/passkey";
import { navigate } from "@/hooks/useRoute";
import { companyErrorMessage, goToCompany, takeCompanyResult, type CompanyButton } from "@/lib/company";
import { Wordmark } from "@/components/brand";
import { ThemeCorner } from "@/components/ThemeMenu";
import { CODE_LENGTH, CodeBoxes } from "@/components/CodeBoxes";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { HELP_PATH } from "@/lib/help";
import { onRepairPage, signInHome } from "@/lib/repair";

export type SignInStep = "password" | "code" | "enroll" | "choose-password" | "forgot" | "reset";

type SessionStatus = "signed_in" | "mfa_verify_required" | "mfa_setup_required";
export type SecondStepMethod = "authenticator" | "passkey" | "recovery_code";
type StatusBody = { status: SessionStatus; methods?: SecondStepMethod[]; recovery_codes?: string[] };

const passkeyMessage = (err: unknown) =>
  err instanceof PasskeyError && err.code === "passkey_invalid" ? "That passkey isn't registered here."
    : err instanceof PasskeyError ? err.message : "Your device couldn't make or use a passkey. Try again.";

const MIN_PASSWORD = 12;

const TIMED_OUT = "Your sign-in timed out. Enter your password again.";

// A half-finished sign-in lasts 15 minutes (docs/WEB.md §4); after that the
// code and authenticator-setup steps get one of these back.
const expired = (err: unknown) => ["session_expired", "session_invalid", "auth_required"].includes(problemCode(err));

function StartOver({ onStartOver }: { onStartOver: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <button type="button" disabled={busy} className="text-sm text-link underline-offset-4 hover:underline"
      onClick={async () => {
        setBusy(true);
        // Ends the half-finished session; if it has already timed out
        // there's nothing to end.
        await api.DELETE("/api/v1/session").catch(() => undefined);
        onStartOver();
      }}>
      Start over
    </button>
  );
}

/** What the page puts under every sign-in card (the repair page's words). */
const Aside = createContext<ReactNode>(null);

function Card({ title, lead, children }: { title: string; lead?: ReactNode; children: ReactNode }) {
  const aside = useContext(Aside);
  return (
    <main className="flex min-h-dvh items-center justify-center px-4 py-10">
      <ThemeCorner />
      <div className="w-full max-w-sm">
        <div className="mb-8 flex justify-center">
          <Wordmark className="text-5xl" />
        </div>
        <section className="rounded-lg border bg-card p-6 shadow-xs" aria-labelledby="signin-title">
          <h1 id="signin-title" className="font-display text-xl font-semibold">{title}</h1>
          {lead && <p className="mt-1 text-sm text-muted-foreground">{lead}</p>}
          <div className="mt-6">{children}</div>
        </section>
        {aside}
        {!onRepairPage() && (
          <p className="mt-6 text-center text-sm">
            <a href={HELP_PATH} className="text-link underline-offset-4 hover:underline"
              onClick={(e) => { e.preventDefault(); navigate(HELP_PATH); }}>Help signing in</a>
          </p>
        )}
      </div>
    </main>
  );
}

function FormError({ message, children }: { message: string; children?: ReactNode }) {
  if (!message) return null;
  return (
    <p role="alert" className="flex items-start gap-2 text-sm font-medium">
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      <span>{message}{children}</span>
    </p>
  );
}

function TextLink({ onClick, children, disabled }: { onClick: () => void; children: ReactNode; disabled?: boolean }) {
  return (
    <button type="button" disabled={disabled} className="text-sm text-link underline-offset-4 hover:underline" onClick={onClick}>
      {children}
    </button>
  );
}

function Submit({ busy, children, disabled, variant = "default" }:
  { busy: boolean; children: ReactNode; disabled?: boolean; variant?: "default" | "outline" }) {
  return (
    <Button type="submit" variant={variant} className="h-11 w-full text-base" disabled={busy || disabled} aria-busy={busy}>
      {busy && <span aria-hidden="true" className="size-4 animate-spin rounded-full border-2 border-current border-t-transparent" />}
      {children}
    </Button>
  );
}

export function SignInScreen({ aside, ...props }: Parameters<typeof SignInSteps>[0] & { aside?: ReactNode }) {
  return <Aside.Provider value={aside}><SignInSteps {...props} /></Aside.Provider>;
}

function SignInSteps({ initialStep = "password", initialMethods = [], setupToken, resetToken, onSignedIn, onSetupNeeded }: {
  initialStep?: SignInStep; initialMethods?: SecondStepMethod[]; setupToken?: string; resetToken?: string; onSignedIn: () => void;
  /** Instead of setting up a second sign-in step here (the repair page can't). */
  onSetupNeeded?: () => void;
}) {
  const [step, setStep] = useState<SignInStep>(
    setupToken ? "choose-password" : resetToken ? "reset"
      : initialStep === "choose-password" || initialStep === "reset" ? "password" : initialStep);
  const [methods, setMethods] = useState<SecondStepMethod[]>(initialMethods);
  const [notice, setNotice] = useState("");
  const next = (body: StatusBody) => {
    if (body.status === "signed_in") onSignedIn();
    else if (body.status === "mfa_verify_required") {
      setMethods(body.methods ?? ["authenticator", "recovery_code"]);
      setStep("code");
    } else if (onSetupNeeded) onSetupNeeded();
    else setStep("enroll");
  };
  const restart = (why = "") => {
    window.history.replaceState(null, "", signInHome());
    setNotice(why);
    setStep("password");
  };
  const timedOut = () => restart(TIMED_OUT);
  switch (step) {
    case "password":
      return <PasswordStep notice={notice} onDone={next} onForgot={() => setStep("forgot")} />;
    case "forgot":
      return <ForgotStep onBack={() => restart()} />;
    case "reset":
      return <ResetLinkStep token={resetToken ?? ""} onDone={next} onSignedIn={onSignedIn}
        onForgot={() => { window.history.replaceState(null, "", signInHome()); setStep("forgot"); }} />;
    case "code":
      return <CodeStep methods={methods} onDone={onSignedIn} onTimedOut={timedOut} onStartOver={restart} />;
    case "enroll":
      return <SecondStepSetup onDone={onSignedIn} onTimedOut={timedOut} onStartOver={restart} />;
    case "choose-password":
      return <SetupLinkStep token={setupToken ?? ""} onDone={next} onSignedIn={onSignedIn} />;
  }
}

function Divider() {
  return (
    <div className="my-5 flex items-center gap-3 text-sm text-muted-foreground" aria-hidden="true">
      <span className="h-px flex-1 bg-border" />or<span className="h-px flex-1 bg-border" />
    </div>
  );
}

/** "Sign in with a passkey", plus the email box's autofill where supported. */
function usePasskeySignIn(onDone: (b: StatusBody) => void, setError: (m: string) => void) {
  const [busy, setBusy] = useState(false);
  const [autofill, setAutofill] = useState(0);
  const abort = useRef<AbortController | null>(null);
  const done = useRef(onDone);
  done.current = onDone;

  useEffect(() => {
    let live = true;
    const ctl = new AbortController();
    abort.current = ctl;
    void (async () => {
      if (!(await autofillSupported()) || !live) return;
      try {
        const body = await answerWithPasskey("/api/v1/session/passkey", { conditional: true, signal: ctl.signal });
        if (live) done.current(body as StatusBody);
      } catch (err) {
        if (live && !cancelled(err)) setError(passkeyMessage(err));
      }
    })();
    return () => { live = false; ctl.abort(); };
  }, [autofill, setError]);

  const signIn = async () => {
    abort.current?.abort();
    setBusy(true);
    setError("");
    try {
      onDone((await answerWithPasskey("/api/v1/session/passkey")) as StatusBody);
    } catch (err) {
      if (!cancelled(err)) setError(passkeyMessage(err));
      setAutofill((n) => n + 1);
    } finally {
      setBusy(false);
    }
  };
  return { busy, signIn };
}

/** The sign-in page's company buttons and whether passwords are off (ADR-052). */
function useSignInOptions() {
  const [company, setCompany] = useState<CompanyButton[]>([]);
  const [required, setRequired] = useState(false);
  // Restored from a backup made at another domain (docs/INSTALL.md §8).
  const [passkeysMoved, setPasskeysMoved] = useState(false);
  // Email is set up: "Forgot your password?" (ADR-067).
  const [passwordReset, setPasswordReset] = useState(false);
  useEffect(() => {
    // Company sign-in comes back to the domain: not on the repair page.
    if (onRepairPage()) return;
    void api.GET("/api/v1/sign-in-options").then(({ data }) => {
      if (!data) return;
      setCompany(data.company);
      setRequired(data.company_sign_in_required);
      setPasskeysMoved(!!data.passkeys_moved);
      setPasswordReset(data.password_reset);
    });
  }, []);
  return { company, required, passkeysMoved, passwordReset };
}

function CompanyButtons({ buttons, disabled, onError }:
  { buttons: CompanyButton[]; disabled: boolean; onError: (m: string) => void }) {
  const [busy, setBusy] = useState("");
  if (buttons.length === 0) return null;
  return (
    <div className="flex flex-col gap-3">
      {buttons.map((b) => (
        <Button key={b.id} type="button" variant="outline" className="h-11 w-full text-base" disabled={disabled || !!busy}
          aria-busy={busy === b.id} onClick={async () => {
            setBusy(b.id);
            onError("");
            try {
              await goToCompany("/api/v1/session/company", b.id);
            } catch (err) {
              onError(err instanceof Error ? err.message : companyErrorMessage(""));
              setBusy("");
            }
          }}>
          <Building2 aria-hidden="true" className="size-4" />
          Continue with {b.name}
        </Button>
      ))}
    </div>
  );
}

function PasswordStep({ notice, onDone, onForgot }: { notice: string; onDone: (s: StatusBody) => void; onForgot: () => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  // Coming back from a company sign-in that was refused: its reason.
  const [error, setError] = useState(() => {
    const { error } = takeCompanyResult();
    return error ? companyErrorMessage(error) : "";
  });
  const [wrong, setWrong] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const passkey = usePasskeySignIn(onDone, setError);
  const offerPasskey = passkeysSupported();
  const options = useSignInOptions();
  // "People must use company sign-in": the password form is only for
  // system admins, so it folds away behind a small link.
  const [showPassword, setShowPassword] = useState(false);
  const passwordForm = !options.required || showPassword;
  const primaryElsewhere = offerPasskey || options.company.length > 0;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setWrong(false);
    const { data, error: err, response } = await api.POST("/api/v1/session", { body: { email, password } });
    setBusy(false);
    if (data) {
      onDone(data as StatusBody);
      return;
    }
    // Never say whether the email exists or the account is waiting out a
    // lockout (docs/WEB.md §4): the same words for every refusal.
    if (response.status === 401 || response.status === 429) {
      setError("Wrong email or password.");
      setWrong(true);
      if (response.status === 429) {
        setWaiting(true);
        window.setTimeout(() => setWaiting(false), 5000);
      }
      return;
    }
    setError(problemMessage(err, "Linx can't sign you in right now. Try again in a moment."));
  };

  return (
    <Card title="Sign in" lead={notice ? <span role="status">{notice}</span> : options.passkeysMoved
      ? <span role="status">Linx moved to this address. Passkeys from the old address don't work here: sign in with your password and authenticator app.</span>
      : undefined}>
      {offerPasskey && (
        <Button type="button" className="h-11 w-full text-base" disabled={passkey.busy || busy} aria-busy={passkey.busy}
          onClick={() => void passkey.signIn()}>
          <KeyRound aria-hidden="true" className="size-4" />
          Sign in with a passkey
        </Button>
      )}
      {offerPasskey && options.company.length > 0 && <div className="h-3" />}
      <CompanyButtons buttons={options.company} disabled={passkey.busy || busy} onError={setError} />
      {!passwordForm && (
        <div className="mt-5 flex flex-col items-center gap-4">
          <FormError message={error} />
          <button type="button" className="text-sm text-link underline-offset-4 hover:underline" onClick={() => setShowPassword(true)}>
            Sign in as a system admin
          </button>
        </div>
      )}
      {passwordForm && primaryElsewhere && <Divider />}
      {passwordForm && <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <fieldset disabled={busy || waiting} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="email">Email</Label>
            <Input id="email" type="email" autoComplete="username webauthn" required value={email}
              onChange={(e) => setEmail(e.target.value)} className="h-11" autoFocus />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="password">Password</Label>
            <Input id="password" type="password" autoComplete="current-password" required value={password}
              onChange={(e) => setPassword(e.target.value)} className="h-11" aria-invalid={error ? true : undefined} />
            {options.passwordReset && !wrong && <span><TextLink onClick={onForgot}>Forgot your password?</TextLink></span>}
          </div>
        </fieldset>
        <FormError message={error}>
          {wrong && options.passwordReset && <> <TextLink onClick={onForgot}>Forgot your password?</TextLink></>}
        </FormError>
        <Submit busy={busy} disabled={waiting || !email || !password} variant={primaryElsewhere ? "outline" : "default"}>Sign in</Submit>
      </form>}
    </Card>
  );
}

type CodeAnswer = { data?: unknown; error?: unknown };

function CodeStep({ methods, onDone, onTimedOut, onStartOver, lead, sendCode, passkeyAnswer, onRefused, footer }: {
  methods: SecondStepMethod[]; onDone: (body?: StatusBody) => void; onTimedOut: () => void; onStartOver: () => void;
  // A password reset's second step (ADR-067) instead of a sign-in's: its
  // own lead, requests, refusals it handles itself (true: handled) and a
  // note under the form.
  lead?: string; sendCode?: (code: string) => Promise<CodeAnswer>; passkeyAnswer?: () => Promise<unknown>;
  onRefused?: (err: unknown) => boolean; footer?: ReactNode;
}) {
  const hasApp = methods.includes("authenticator");
  const hasPasskey = methods.includes("passkey") && passkeysSupported();
  const hasRecovery = methods.includes("recovery_code");
  const [recovery, setRecovery] = useState(!hasApp && !hasPasskey);
  const [code, setCode] = useState("");
  const [retry, setRetry] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [keyBusy, setKeyBusy] = useState(false);

  const withPasskey = async () => {
    setKeyBusy(true);
    setError("");
    try {
      onDone((passkeyAnswer ? await passkeyAnswer() : await answerWithPasskey("/api/v1/session/mfa/passkey")) as StatusBody);
    } catch (err) {
      if (onRefused?.(err)) return;
      if (err instanceof PasskeyError && ["session_expired", "session_invalid", "auth_required"].includes(err.code)) onTimedOut();
      else if (!cancelled(err)) setError(passkeyMessage(err));
    } finally {
      setKeyBusy(false);
    }
  };

  if (!hasApp && !hasPasskey && !hasRecovery) {
    // Only a passkey, on the repair page: nothing here can finish it.
    return (
      <Card title="Your passkey can't be used here" lead="Passkeys only work at your Linx's own address.">
        <div className="flex flex-col gap-4 text-sm">
          <p>Run this on the server instead, for a link that doesn't ask you to sign in:</p>
          <code className="block w-fit max-w-full rounded-md bg-muted px-3 py-2 font-mono break-all">sudo linx setup --new-link --no-sign-in</code>
          <StartOver onStartOver={onStartOver} />
        </div>
      </Card>
    );
  }

  if (!hasApp && hasPasskey && !recovery) {
    return (
      <Card title="Use your passkey" lead={lead ?? "Unlock your passkey to finish signing in."}>
        <div className="flex flex-col gap-4">
          <Button className="h-11 w-full text-base" disabled={keyBusy} aria-busy={keyBusy} onClick={() => void withPasskey()} autoFocus>
            <KeyRound aria-hidden="true" className="size-4" />
            Use my passkey
          </Button>
          <FormError message={error} />
          {hasRecovery && (
            <button type="button" className="text-sm text-link underline-offset-4 hover:underline" onClick={() => { setRecovery(true); setError(""); }}>
              Use a recovery code instead
            </button>
          )}
          <StartOver onStartOver={onStartOver} />
          {footer}
        </div>
      </Card>
    );
  }

  const send = async (value: string) => {
    setBusy(true);
    setError("");
    const { data, error: err }: CodeAnswer = sendCode ? await sendCode(value.trim())
      : await api.POST("/api/v1/session/mfa", { body: { code: value.trim() } });
    setBusy(false);
    if (data) return onDone(data as StatusBody);
    if (onRefused?.(err)) return;
    if (expired(err)) return onTimedOut();
    if (problemCode(err) === "mfa_code_invalid") setError("That code isn't right.");
    else if (problemCode(err) === "mfa_code_used") setError("That code was already used. Wait for the next one in your app.");
    else setError(problemMessage(err));
    if (!recovery) { setCode(""); setRetry((n) => n + 1); }
  };
  const submit = (e: FormEvent) => { e.preventDefault(); void send(code); };

  return (
    <Card title="Enter your code"
      lead={(lead ? lead + " " : "") + (recovery ? "Type one of the recovery codes you saved. Each works once." : "Open your authenticator app and type the 6-digit code for Linx.")}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="code">{recovery ? "Recovery code" : "6-digit code"}</Label>
          {recovery
            ? <Input id="code" value={code} onChange={(e) => setCode(e.target.value)} autoFocus disabled={busy}
                className="h-11 font-mono text-lg tracking-widest" autoComplete="off" />
            : <CodeBoxes id="code" value={code} onChange={setCode} onComplete={(v) => void send(v)} autoFocus
                disabled={busy} retry={retry} invalid={!!error} />}
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={recovery ? !code.trim() : code.length !== CODE_LENGTH}>Continue</Submit>
        {hasPasskey && (
          <Button type="button" variant="outline" className="h-11 w-full text-base" disabled={keyBusy} aria-busy={keyBusy}
            onClick={() => void withPasskey()}>
            <KeyRound aria-hidden="true" className="size-4" />
            Use a passkey instead
          </Button>
        )}
        {(hasApp || hasPasskey) && hasRecovery && (
          <button type="button" className="text-sm text-link underline-offset-4 hover:underline"
            onClick={() => { setRecovery(!recovery); setCode(""); setError(""); }}>
            {!recovery ? "Use a recovery code instead" : hasApp ? "Use my authenticator app instead" : "Use my passkey instead"}
          </button>
        )}
        <StartOver onStartOver={onStartOver} />
        {footer}
      </form>
    </Card>
  );
}

const LINK_UNUSABLE = "This link has expired or was already used. Ask your admin for a new one.";

type LinkInfo = { email: string; name: string; role: string; has_second_step: boolean; passkeys_available: boolean };
type Way = "passkey" | "app" | "password";
const isAdmin = (role: string) => role === "admin" || role === "system_admin";

function SetupLinkStep({ token, onDone, onSignedIn }:
  { token: string; onDone: (s: StatusBody) => void; onSignedIn: () => void }) {
  // The link is checked before asking anything, so a used or expired one
  // says so at once.
  const [link, setLink] = useState<"checking" | LinkInfo | { problem: string }>("checking");
  const [way, setWay] = useState<Way | null>(null);
  const [after, setAfter] = useState<"app" | { codes?: string[] } | null>(null);
  useEffect(() => {
    let stale = false;
    void (async () => {
      const { data, error: err } = await api.GET("/api/v1/setup-links/{token}", { params: { path: { token } } });
      if (stale) return;
      if (data) setLink(data);
      else setLink({ problem: problemCode(err) === "setup_link_invalid" ? LINK_UNUSABLE : problemMessage(err) });
    })();
    return () => { stale = true; };
  }, [token]);
  if (link === "checking") return <main className="min-h-dvh" aria-busy="true" />;
  if ("problem" in link) {
    return (
      <Card title="This link can't be used">
        <div className="flex flex-col gap-4">
          <FormError message={link.problem} />
          <Button className="h-11 w-full text-base" onClick={() => navigate("/", true)}>Go to sign in</Button>
        </div>
      </Card>
    );
  }
  // Someone who already has a passkey or authenticator only gets a new
  // password here: the second step is still asked for (a link never skips it).
  if (link.has_second_step) return <ChoosePasswordForm token={token} onDone={onDone} />;

  if (after === "app") return <EnrollStep onDone={onSignedIn} onTimedOut={() => navigate("/", true)} />;
  if (after) {
    return <PasskeyDone codes={after.codes} onDone={onSignedIn} offerPassword />;
  }
  if (way === "passkey") {
    return (
      <Card title="Create your passkey" lead={`For ${link.email}. Your device will ask for Face ID, your fingerprint or its PIN.`}>
        <NewPasskey path={`/api/v1/setup-links/${encodeURIComponent(token)}/passkey`} onBack={() => setWay(null)}
          onSaved={(body) => {
            window.history.replaceState(null, "", "/");
            setAfter({ codes: body.recovery_codes as string[] | undefined });
          }} />
      </Card>
    );
  }
  if (way === "app" || way === "password") {
    return (
      <ChoosePasswordForm token={token} passwordOnly={way === "password"} onBack={() => setWay(null)}
        onDone={(body) => {
          if (way === "app" && body.status !== "mfa_verify_required") setAfter("app");
          else onDone(body);
        }} />
    );
  }
  return (
    <ChooseWay title={link.role === "system_admin" ? "Welcome to Linx" : "Set up your account"}
      lead={<>Signing in as <span className="font-medium text-foreground">{link.email}</span>. How do you want to sign in?</>}
      admin={isAdmin(link.role)} passkeys={link.passkeys_available && passkeysSupported()} onChoose={setWay} />
  );
}

/** The three ways to sign in, passkey recommended (ADMIN_SCREENS §3.1). */
function ChooseWay({ title, lead, admin, passkeys, onChoose, busy, footer }: {
  title: string; lead: ReactNode; admin: boolean; passkeys: boolean; onChoose: (w: Way) => void; busy?: boolean; footer?: ReactNode;
}) {
  const [way, setWay] = useState<Way>(passkeys ? "passkey" : "app");
  const [understood, setUnderstood] = useState(false);
  const warn = way === "password" && admin;
  return (
    <Card title={title} lead={lead}>
      <form className="flex flex-col gap-5" onSubmit={(e) => { e.preventDefault(); onChoose(way); }}>
        <RadioGroup value={way} onValueChange={(v) => { setWay(v as Way); setUnderstood(false); }} aria-label="How to sign in">
          {passkeys && (
            <WayOption value="passkey" title="Passkey" badge="Recommended"
              hint="Face ID, Touch ID, Windows Hello or your phone. Nothing to remember or type." />
          )}
          <WayOption value="app" title="Password + authenticator app" hint="A password, then a 6-digit code from an app on your phone." />
          <WayOption value="password" title="Password only" hint={admin ? "Not recommended." : "Simplest, but a leaked password is enough to get in."} />
        </RadioGroup>
        {warn && (
          <div role="alert" className="rounded-md border-2 border-status-away p-4 text-sm">
            <p className="flex items-start gap-2 font-medium">
              <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
              Not recommended for an admin
            </p>
            <p className="mt-2 text-muted-foreground">
              Anyone who learns or guesses your password can control your whole phone system: add people, make
              expensive calls abroad, change settings. A passkey or authenticator app stops that. You can add one later in My account.
            </p>
            <label className="mt-3 flex items-center gap-3">
              <Checkbox checked={understood} onCheckedChange={(v) => setUnderstood(v === true)} />
              I understand, use a password only
            </label>
          </div>
        )}
        <Submit busy={busy ?? false} disabled={warn && !understood}>Continue</Submit>
        {footer}
      </form>
    </Card>
  );
}

function WayOption({ value, title, hint, badge }: { value: Way; title: string; hint: string; badge?: string }) {
  const id = `way-${value}`;
  return (
    <label htmlFor={id} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
      <RadioGroupItem id={id} value={value} className="mt-0.5" />
      <span className="flex flex-col gap-0.5">
        <span className="text-sm font-medium">
          {title}
          {badge && <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">{badge}</span>}
        </span>
        <span className="text-sm text-muted-foreground">{hint}</span>
      </span>
    </label>
  );
}

/**
 * Makes a passkey at `path` (the browser's prompt), then asks for its name
 * (pre-filled from the device) and saves it.
 */
export function NewPasskey({ path, onSaved, onBack, onConfirmNeeded }: {
  path: string; onSaved: (body: Record<string, unknown>) => void; onBack?: () => void;
  // "Confirm it's you" is needed first (My account): the page opens its
  // dialog, then the person presses Create again (the browser's prompt
  // needs a fresh click).
  onConfirmNeeded?: () => void;
}) {
  const [credential, setCredential] = useState<Record<string, unknown> | null>(null);
  const [name, setName] = useState(deviceName());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const create = async () => {
    setBusy(true);
    setError("");
    try {
      setCredential(await createPasskey(path));
    } catch (err) {
      if (onConfirmNeeded && err instanceof PasskeyError && err.code === "confirm_required") onConfirmNeeded();
      else if (!cancelled(err)) setError(passkeyMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!credential) return;
    setBusy(true);
    setError("");
    try {
      onSaved(await savePasskey(path, credential, name.trim()));
    } catch (err) {
      setError(passkeyMessage(err));
      if (err instanceof PasskeyError && err.code === "passkey_expired") setCredential(null);
    } finally {
      setBusy(false);
    }
  };

  if (!credential) {
    return (
      <div className="flex flex-col gap-4">
        <Button className="h-11 w-full text-base" disabled={busy} aria-busy={busy} onClick={() => void create()} autoFocus>
          <KeyRound aria-hidden="true" className="size-4" />
          Create passkey
        </Button>
        <FormError message={error} />
        {onBack && (
          <button type="button" className="text-sm text-link underline-offset-4 hover:underline" onClick={onBack}>
            Choose another way
          </button>
        )}
      </div>
    );
  }
  return (
    <form onSubmit={save} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-2">
        <Label htmlFor="passkey-name">Name this passkey</Label>
        <Input id="passkey-name" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} className="h-11"
          autoFocus disabled={busy} />
        <p className="text-sm text-muted-foreground">So you can tell your passkeys apart later.</p>
      </div>
      <FormError message={error} />
      <Submit busy={busy} disabled={!name.trim()}>Save passkey</Submit>
    </form>
  );
}

/** After a first passkey: its recovery codes, then (from a setup link) the offer of a password. */
function PasskeyDone({ codes, onDone, offerPassword }: { codes?: string[]; onDone: () => void; offerPassword?: boolean }) {
  const [stage, setStage] = useState<"codes" | "offer" | "password">(codes?.length ? "codes" : "offer");
  const finished = !offerPassword && stage !== "codes";
  useEffect(() => { if (finished) onDone(); }, [finished, onDone]);
  if (stage === "codes") {
    return <RecoveryCodes codes={codes ?? []} lead="If you lose your passkey, each of these works once with your password instead. Keep them somewhere safe. They won't be shown again."
      onDone={() => (offerPassword ? setStage("offer") : onDone())} />;
  }
  if (!offerPassword) return null;
  if (stage === "offer") {
    return (
      <Card title="Also add a password?" lead="You can sign in with your passkey alone. A password lets you sign in from a device that doesn't have it. You can do this later in My account.">
        <div className="flex gap-2">
          <Button variant="outline" className="h-11 flex-1" onClick={onDone}>Skip</Button>
          <Button className="h-11 flex-1" onClick={() => setStage("password")}>Add a password</Button>
        </div>
      </Card>
    );
  }
  return <AddPasswordForm onDone={onDone} />;
}

function AddPasswordForm({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const short = password.length < MIN_PASSWORD;
  const mismatch = again.length > 0 && again !== password;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (short || again !== password) return;
    setBusy(true);
    setError("");
    const { response, error: err } = await api.POST("/api/v1/me/password", { body: { new_password: password } });
    setBusy(false);
    if (response.ok) onDone();
    else setError(problemMessage(err));
  };
  return (
    <Card title="Add a password">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="add-password">Password</Label>
          <Input id="add-password" type="password" autoComplete="new-password" value={password}
            onChange={(e) => setPassword(e.target.value)} className="h-11" autoFocus disabled={busy} />
          <p className="text-sm text-muted-foreground">At least {MIN_PASSWORD} characters.</p>
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="add-password-again">Type it again</Label>
          <Input id="add-password-again" type="password" autoComplete="new-password" value={again}
            onChange={(e) => setAgain(e.target.value)} className="h-11" disabled={busy} aria-invalid={mismatch || undefined} />
          {mismatch && <p className="text-sm text-muted-foreground">The two passwords don't match yet.</p>}
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={short || again !== password}>Save password</Submit>
        <button type="button" className="text-sm text-link underline-offset-4 hover:underline" onClick={onDone}>Skip for now</button>
      </form>
    </Card>
  );
}

/**
 * An admin whose sign-in waits on a second step's setup: passkey
 * (recommended), authenticator app, or password only with the warning.
 */
function SecondStepSetup({ onDone, onTimedOut, onStartOver }: { onDone: () => void; onTimedOut: () => void; onStartOver: () => void }) {
  const [way, setWay] = useState<Way | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (codes) return <PasskeyDone codes={codes} onDone={onDone} />;
  if (way === "app") return <EnrollStep onDone={onDone} onTimedOut={onTimedOut} onStartOver={onStartOver} />;
  if (way === "passkey") {
    return (
      <Card title="Create your passkey" lead="Your device will ask for Face ID, your fingerprint or its PIN.">
        <NewPasskey path="/api/v1/me/passkeys" onBack={() => setWay(null)}
          onSaved={(body) => {
            const c = body.recovery_codes as string[] | undefined;
            if (c?.length) setCodes(c);
            else onDone();
          }} />
      </Card>
    );
  }
  const choose = async (w: Way) => {
    if (w !== "password") {
      setWay(w);
      return;
    }
    setBusy(true);
    setError("");
    const { response, error: err } = await api.POST("/api/v1/me/password-only", { body: { accept: true } });
    setBusy(false);
    if (response.ok) onDone();
    else if (expired(err)) onTimedOut();
    else setError(problemMessage(err));
  };
  return (
    <ChooseWay title="Add a second way to sign in" admin passkeys={passkeysSupported()} busy={busy} onChoose={(w) => void choose(w)}
      lead="Admins are asked for more than a password. Choose how."
      footer={<><FormError message={error} /><StartOver onStartOver={onStartOver} /></>} />
  );
}

function ChoosePasswordForm({ token, onDone, passwordOnly, onBack }:
  { token: string; onDone: (s: StatusBody) => void; passwordOnly?: boolean; onBack?: () => void }) {
  return (
    <NewPasswordCard title="Choose a password" lead="You'll use it with your email to sign in to Linx." submitLabel="Save password"
      onSubmit={async (password) => {
        const { data, error: err } = await api.POST("/api/v1/setup-links/{token}", {
          params: { path: { token } }, body: { password, ...(passwordOnly ? { password_only: true } : {}) },
        });
        if (data) {
          window.history.replaceState(null, "", "/");
          onDone(data as StatusBody);
          return "";
        }
        return problemCode(err) === "setup_link_invalid" ? LINK_UNUSABLE : problemMessage(err);
      }}
      footer={onBack && <TextLink onClick={onBack}>Choose another way</TextLink>} />
  );
}

/**
 * A new password, typed twice. onSubmit returns why it failed, or "" once
 * it's done.
 */
function NewPasswordCard({ title, lead, submitLabel, onSubmit, footer, initialError = "" }: {
  title: string; lead: ReactNode; submitLabel: string; onSubmit: (password: string) => Promise<string>; footer?: ReactNode;
  initialError?: string;
}) {
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(initialError);
  const short = password.length < MIN_PASSWORD;
  const mismatch = again.length > 0 && again !== password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (short || mismatch) return;
    setBusy(true);
    setError("");
    const why = await onSubmit(password);
    setBusy(false);
    setError(why);
  };

  return (
    <Card title={title} lead={lead}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="new-password">New password</Label>
          <Input id="new-password" type="password" autoComplete="new-password" value={password}
            onChange={(e) => setPassword(e.target.value)} className="h-11" autoFocus disabled={busy} aria-describedby="pw-hint" />
          <p id="pw-hint" className="text-sm text-muted-foreground" aria-live="polite">
            {password.length === 0 ? `At least ${MIN_PASSWORD} characters. A few unrelated words work well.`
              : short ? `${MIN_PASSWORD - password.length} more character${MIN_PASSWORD - password.length === 1 ? "" : "s"} to go.`
                : password.length < 16 ? "Long enough. Longer is stronger." : "Strong length."}
          </p>
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="again">Type it again</Label>
          <Input id="again" type="password" autoComplete="new-password" value={again}
            onChange={(e) => setAgain(e.target.value)} className="h-11" disabled={busy} aria-invalid={mismatch || undefined} />
          {mismatch && <p className="text-sm text-muted-foreground">The two passwords don't match yet.</p>}
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={short || again !== password}>{submitLabel}</Submit>
        {footer}
      </form>
    </Card>
  );
}

/** "Forgot your password?": the email, then always the same answer. */
function ForgotStep({ onBack }: { onBack: () => void }) {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sent, setSent] = useState("");
  const back = <TextLink onClick={onBack}>← Back to sign-in</TextLink>;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { response, error: err } = await api.POST("/api/v1/password-reset", { body: { email: email.trim() } });
    setBusy(false);
    if (response.ok) setSent(email.trim());
    else setError(problemMessage(err, "Linx can't send the link right now. Try again in a moment."));
  };

  if (sent) {
    return (
      <Card title="Check your email"
        lead={<span role="status">If <span className="font-medium text-foreground break-all">{sent}</span> is an account here, an email is on its way.</span>}>
        <div className="flex flex-col items-start gap-4 text-sm">
          <p>The link works once, for 30 minutes.</p>
          <p className="text-muted-foreground">Nothing arrived? Check spam, or ask your admin.</p>
          {back}
        </div>
      </Card>
    );
  }
  return (
    <Card title="Forgot your password?" lead="Type your email. If it belongs to an account here, we'll email you a link to choose a new one.">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="forgot-email">Email</Label>
          <Input id="forgot-email" type="email" autoComplete="username" required value={email}
            onChange={(e) => setEmail(e.target.value)} className="h-11" autoFocus disabled={busy} />
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={!email.trim()}>Send the link</Submit>
        <span>{back}</span>
      </form>
    </Card>
  );
}

const RESET_UNUSABLE = "This link has already been used or is older than 30 minutes.";

type ResetInfo = { email: string; methods: SecondStepMethod[] };

/**
 * The emailed reset link: a new password, then the second step (if the
 * account has one), sent together so nothing changes until both pass.
 */
function ResetLinkStep({ token, onDone, onSignedIn, onForgot }:
  { token: string; onDone: (s: StatusBody) => void; onSignedIn: () => void; onForgot: () => void }) {
  const [link, setLink] = useState<"checking" | ResetInfo | { problem: string }>("checking");
  // The new password, chosen and waiting for the second step.
  const [password, setPassword] = useState("");
  const [passwordError, setPasswordError] = useState("");
  const [done, setDone] = useState(false);
  useEffect(() => {
    let stale = false;
    void (async () => {
      const { data, error: err } = await api.GET("/api/v1/reset-links/{token}", { params: { path: { token } } });
      if (stale) return;
      if (data) setLink(data);
      else setLink({ problem: problemCode(err) === "reset_link_invalid" ? RESET_UNUSABLE : problemMessage(err) });
    })();
    return () => { stale = true; };
  }, [token]);

  const finished = (body: StatusBody) => {
    window.history.replaceState(null, "", "/");
    if (body.status === "signed_in") setDone(true);
    else onDone(body);
  };
  // What a refusal means for this page: the link is gone, or the password
  // itself was refused (back to choosing one); "" when it's the second
  // step's own problem.
  const refused = (err: unknown) => {
    const code = problemCode(err) || (err instanceof PasskeyError ? err.code : "");
    if (code === "reset_link_invalid") {
      setLink({ problem: RESET_UNUSABLE });
      return true;
    }
    if (code === "password_invalid") {
      setPasswordError(problemMessage(err, err instanceof Error ? err.message : ""));
      setPassword("");
      return true;
    }
    return false;
  };

  if (link === "checking") return <main className="min-h-dvh" aria-busy="true" />;
  if ("problem" in link) {
    return (
      <Card title="This link can't be used">
        <div className="flex flex-col items-start gap-4">
          <FormError message={link.problem} />
          <Button className="h-11 w-full text-base" onClick={onForgot}>Send a new one</Button>
          <TextLink onClick={() => navigate("/", true)}>← Back to sign-in</TextLink>
        </div>
      </Card>
    );
  }
  if (done) {
    return (
      <Card title="Your password is changed" lead={<span role="status" className="flex items-center gap-2">
        <CircleCheck aria-hidden="true" className="size-4 shrink-0 text-status-available" />
        You've been signed out everywhere else.
      </span>}>
        <Button className="h-11 w-full text-base" onClick={onSignedIn} autoFocus>Continue to Linx</Button>
      </Card>
    );
  }
  const path = `/api/v1/reset-links/${encodeURIComponent(token)}`;
  if (!password) {
    return (
      <NewPasswordCard key={passwordError} title="Choose a new password" initialError={passwordError}
        lead={<>For <span className="font-medium text-foreground break-all">{link.email}</span>.</>} submitLabel="Continue"
        onSubmit={async (pw) => {
          if (link.methods.length > 0) {
            setPasswordError("");
            setPassword(pw);
            return "";
          }
          const { data, error: err } = await api.POST("/api/v1/reset-links/{token}", { params: { path: { token } }, body: { password: pw } });
          if (data) {
            finished(data as StatusBody);
            return "";
          }
          if (problemCode(err) === "reset_link_invalid") {
            setLink({ problem: RESET_UNUSABLE });
            return "";
          }
          return problemMessage(err);
        }} />
    );
  }
  return (
    <CodeStep methods={link.methods} lead="To finish, confirm with your authenticator app or passkey."
      onDone={(body) => finished(body ?? { status: "signed_in" })}
      onTimedOut={() => setLink({ problem: RESET_UNUSABLE })}
      onStartOver={() => setPassword("")}
      sendCode={(code) => api.POST("/api/v1/reset-links/{token}", { params: { path: { token } }, body: { password, code } })}
      passkeyAnswer={() => answerWithPasskey(`${path}/passkey`, { body: { password } })}
      onRefused={refused}
      footer={<AskAdmin token={token} />} />
  );
}

/**
 * "Ask my admin to reset it": someone who has lost every second step asks
 * the admins from the reset link (owner, 2026-10-02). Nothing changes until
 * an admin has checked it's them and reset it in People.
 */
function AskAdmin({ token }: { token: string }) {
  const [state, setState] = useState<"idle" | "busy" | "asked">("idle");
  const [error, setError] = useState("");
  if (state === "asked") {
    return (
      <div role="status" className="rounded-md border p-3 text-sm">
        <p className="flex items-start gap-2 font-medium">
          <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-available" />
          Your admin has been asked.
        </p>
        <p className="mt-1 text-muted-foreground">
          They'll check it's really you, then reset your second step. After that, use <span className="font-medium text-foreground">Forgot your password?</span> again
          to choose your new password and set up a new second step.
        </p>
      </div>
    );
  }
  return (
    <div className="flex flex-col items-start gap-2 border-t pt-4 text-sm">
      <p className="text-muted-foreground">Lost your authenticator app, passkey and recovery codes?</p>
      <Button type="button" variant="outline" size="sm" disabled={state === "busy"} aria-busy={state === "busy"}
        onClick={async () => {
          setState("busy");
          setError("");
          const { response, error: err } = await api.POST("/api/v1/reset-links/{token}/ask-admin", { params: { path: { token } } });
          if (response.ok) setState("asked");
          else {
            setState("idle");
            setError(problemMessage(err));
          }
        }}>
        Ask my admin to reset it
      </Button>
      <FormError message={error} />
    </div>
  );
}

function EnrollStep({ onDone, onTimedOut, onStartOver }: { onDone: () => void; onTimedOut: () => void; onStartOver?: () => void }) {
  const [secret, setSecret] = useState("");
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [retry, setRetry] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const { data, error: err } = await api.POST("/api/v1/me/mfa");
      if (cancelled) return;
      if (!data) {
        if (expired(err)) onTimedOut();
        else setError(problemMessage(err));
        return;
      }
      setSecret(data.secret);
      const QRCode = (await import("qrcode")).default;
      setQr(await QRCode.toDataURL(data.otpauth_url, { margin: 1, width: 192 }));
    })();
    return () => { cancelled = true; };
    // Runs once: a new enrollment secret on every render would be wrong.
  }, []);

  const send = async (value: string) => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/me/mfa/confirm", { body: { code: value.trim() } });
    setBusy(false);
    if (data) return setCodes(data.recovery_codes);
    if (expired(err)) return onTimedOut();
    setError(problemCode(err) === "mfa_code_invalid" ? "That code isn't right. Check the time on your phone and try the next code." : problemMessage(err));
    setCode("");
    setRetry((n) => n + 1);
  };
  const confirm = (e: FormEvent) => { e.preventDefault(); void send(code); };

  if (codes) {
    return <RecoveryCodes codes={codes} onDone={onDone}
      lead="If you lose your phone, each of these lets you sign in once instead of a code. Keep them somewhere safe. They won't be shown again." />;
  }

  return (
    <Card title="Set up your authenticator app"
      lead="Scan this with an app like Google Authenticator, 1Password or Authy.">
      <div className="flex flex-col items-center gap-3">
        {qr ? <img src={qr} alt="QR code for your authenticator app" width={192} height={192} className="rounded-md border" />
          : <div className="size-48 animate-pulse rounded-md bg-background" aria-label="Loading" />}
        {secret && (
          <p className="text-center text-sm text-muted-foreground">
            Can't scan? Type this key instead:
            <span className="mt-1 block font-mono text-foreground break-all select-all">{secret}</span>
          </p>
        )}
      </div>
      <form onSubmit={confirm} className="mt-6 flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="enroll-code">6-digit code from the app</Label>
          <CodeBoxes id="enroll-code" value={code} onChange={setCode} onComplete={(v) => void send(v)}
            disabled={busy || !secret} retry={retry} invalid={!!error} />
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={code.length !== CODE_LENGTH}>Turn on</Submit>
        {onStartOver && <StartOver onStartOver={onStartOver} />}
      </form>
    </Card>
  );
}

function RecoveryCodes({ codes, lead, onDone }: { codes: string[]; lead: string; onDone: () => void }) {
  const [saved, setSaved] = useState(false);
  const text = codes.join("\n");
  const download = () => {
    const url = URL.createObjectURL(new Blob([`Linx recovery codes\n\n${text}\n`], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "linx-recovery-codes.txt";
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <Card title="Save your recovery codes" lead={lead}>
      <ul className="grid grid-cols-2 gap-2 rounded-md border bg-background p-4 font-mono text-sm" aria-label="Recovery codes">
        {codes.map((c) => <li key={c}>{c}</li>)}
      </ul>
      <div className="mt-4 flex gap-2">
        <Button type="button" variant="outline" className="flex-1" onClick={() => void navigator.clipboard?.writeText(text)}>Copy</Button>
        <Button type="button" variant="outline" className="flex-1" onClick={download}>Download</Button>
      </div>
      <label className="mt-6 flex items-center gap-3 text-sm">
        <Checkbox checked={saved} onCheckedChange={(v) => setSaved(v === true)} />
        I've saved these codes
      </label>
      <Button className="mt-6 h-11 w-full text-base" disabled={!saved} onClick={onDone}>Continue</Button>
    </Card>
  );
}
