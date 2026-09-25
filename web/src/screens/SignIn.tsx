// Sign-in (docs/ui/WEB_SCREENS_PHASE1C.md §1): email + password, then the
// authenticator code when the account has one, and on first use (a set-
// password link) a new password and, for admins, authenticator setup with
// recovery codes. One step shown at a time.
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { CircleAlert } from "lucide-react";
import { api, problemCode, problemMessage } from "@/api/client";
import { Wordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export type SignInStep = "password" | "code" | "enroll" | "choose-password";

type SessionStatus = "signed_in" | "mfa_verify_required" | "mfa_setup_required";

const MIN_PASSWORD = 12;

function Card({ title, lead, children }: { title: string; lead?: ReactNode; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex justify-center">
          <Wordmark className="text-5xl" />
        </div>
        <section className="rounded-lg border bg-card p-6 shadow-xs" aria-labelledby="signin-title">
          <h1 id="signin-title" className="font-display text-xl font-semibold">{title}</h1>
          {lead && <p className="mt-1 text-sm text-muted-foreground">{lead}</p>}
          <div className="mt-6">{children}</div>
        </section>
      </div>
    </main>
  );
}

function FormError({ message }: { message: string }) {
  if (!message) return null;
  return (
    <p role="alert" className="flex items-start gap-2 text-sm font-medium">
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      {message}
    </p>
  );
}

function Submit({ busy, children, disabled }: { busy: boolean; children: ReactNode; disabled?: boolean }) {
  return (
    <Button type="submit" className="h-11 w-full text-base" disabled={busy || disabled} aria-busy={busy}>
      {busy && <span aria-hidden="true" className="size-4 animate-spin rounded-full border-2 border-current border-t-transparent" />}
      {children}
    </Button>
  );
}

export function SignInScreen({ initialStep = "password", setupToken, onSignedIn }:
  { initialStep?: SignInStep; setupToken?: string; onSignedIn: () => void }) {
  const [step, setStep] = useState<SignInStep>(setupToken ? "choose-password" : initialStep);
  const next = (status: SessionStatus) => {
    if (status === "signed_in") onSignedIn();
    else setStep(status === "mfa_verify_required" ? "code" : "enroll");
  };
  switch (step) {
    case "password":
      return <PasswordStep onDone={next} />;
    case "code":
      return <CodeStep onDone={onSignedIn} />;
    case "enroll":
      return <EnrollStep onDone={onSignedIn} />;
    case "choose-password":
      return <ChoosePasswordStep token={setupToken ?? ""} onDone={next} />;
  }
}

function PasswordStep({ onDone }: { onDone: (s: SessionStatus) => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [waiting, setWaiting] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error: err, response } = await api.POST("/api/v1/session", { body: { email, password } });
    setBusy(false);
    if (data) {
      onDone(data.status);
      return;
    }
    // Never say whether the email exists or the account is waiting out a
    // lockout (docs/WEB.md §4): the same words for every refusal.
    if (response.status === 401 || response.status === 429) {
      setError("Wrong email or password.");
      if (response.status === 429) {
        setWaiting(true);
        window.setTimeout(() => setWaiting(false), 5000);
      }
      return;
    }
    setError(problemMessage(err, "Linx can't sign you in right now. Try again in a moment."));
  };

  return (
    <Card title="Sign in">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <fieldset disabled={busy || waiting} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="email">Email</Label>
            <Input id="email" type="email" autoComplete="username" required value={email}
              onChange={(e) => setEmail(e.target.value)} className="h-11" autoFocus />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="password">Password</Label>
            <Input id="password" type="password" autoComplete="current-password" required value={password}
              onChange={(e) => setPassword(e.target.value)} className="h-11" aria-invalid={error ? true : undefined} />
          </div>
        </fieldset>
        <FormError message={error} />
        <Submit busy={busy} disabled={waiting || !email || !password}>Sign in</Submit>
      </form>
    </Card>
  );
}

function CodeStep({ onDone }: { onDone: () => void }) {
  const [recovery, setRecovery] = useState(false);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/session/mfa", { body: { code: code.trim() } });
    setBusy(false);
    if (data) onDone();
    else setError(problemCode(err) === "mfa_code_invalid" ? "That code isn't right." : problemMessage(err));
  };

  return (
    <Card title="Enter your code"
      lead={recovery ? "Type one of the recovery codes you saved. Each works once." : "Open your authenticator app and type the 6-digit code for Linx."}>
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="code">{recovery ? "Recovery code" : "6-digit code"}</Label>
          <Input id="code" value={code} onChange={(e) => setCode(e.target.value)} autoFocus disabled={busy}
            className="h-11 font-mono text-lg tracking-widest"
            {...(recovery ? { autoComplete: "off" } : { inputMode: "numeric", autoComplete: "one-time-code", maxLength: 6, pattern: "[0-9]*" })} />
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={!code.trim()}>Continue</Submit>
        <button type="button" className="text-sm text-link underline-offset-4 hover:underline"
          onClick={() => { setRecovery(!recovery); setCode(""); setError(""); }}>
          {recovery ? "Use my authenticator app instead" : "Use a recovery code instead"}
        </button>
      </form>
    </Card>
  );
}

function ChoosePasswordStep({ token, onDone }: { token: string; onDone: (s: SessionStatus) => void }) {
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const short = password.length < MIN_PASSWORD;
  const mismatch = again.length > 0 && again !== password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (short || mismatch) return;
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/setup-links/{token}", {
      params: { path: { token } }, body: { password },
    });
    setBusy(false);
    if (data) {
      window.history.replaceState(null, "", "/");
      onDone(data.status);
    } else if (problemCode(err) === "setup_link_invalid") {
      setError("This link has expired or was already used. Ask your admin for a new one.");
    } else {
      setError(problemMessage(err));
    }
  };

  return (
    <Card title="Choose a password" lead="You'll use it with your email to sign in to Linx.">
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
        <Submit busy={busy} disabled={short || again !== password}>Save password</Submit>
      </form>
    </Card>
  );
}

function EnrollStep({ onDone }: { onDone: () => void }) {
  const [secret, setSecret] = useState("");
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const { data, error: err } = await api.POST("/api/v1/me/mfa");
      if (cancelled) return;
      if (!data) {
        setError(problemMessage(err));
        return;
      }
      setSecret(data.secret);
      const QRCode = (await import("qrcode")).default;
      setQr(await QRCode.toDataURL(data.otpauth_url, { margin: 1, width: 192 }));
    })();
    return () => { cancelled = true; };
  }, []);

  const confirm = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/me/mfa/confirm", { body: { code: code.trim() } });
    setBusy(false);
    if (data) setCodes(data.recovery_codes);
    else setError(problemCode(err) === "mfa_code_invalid" ? "That code isn't right. Check the time on your phone and try the next code." : problemMessage(err));
  };

  if (codes) {
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
      <Card title="Save your recovery codes"
        lead="If you lose your phone, each of these lets you sign in once instead of a code. Keep them somewhere safe. They won't be shown again.">
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

  return (
    <Card title="Set up your authenticator app"
      lead="Admins need one. Scan this with an app like Google Authenticator, 1Password or Authy.">
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
          <Input id="enroll-code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code}
            onChange={(e) => setCode(e.target.value)} className="h-11 font-mono text-lg tracking-widest" disabled={busy || !secret} />
        </div>
        <FormError message={error} />
        <Submit busy={busy} disabled={code.trim().length !== 6}>Turn on</Submit>
      </form>
    </Card>
  );
}
