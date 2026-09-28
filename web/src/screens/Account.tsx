// My account (ADMIN_SCREENS_PHASE1E.md §11), the parts built with passkeys
// and company sign-in (Phase 1E steps 3-4): passkeys (add, rename, remove),
// the authenticator app (set up or replace, with new recovery codes),
// adding a password to a passkey-only account, and linking company
// accounts. The rest of the page comes with step 8.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Building2, KeyRound, Smartphone, TriangleAlert } from "lucide-react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import { needsConfirm, useConfirmIdentity, type Outcome } from "@/components/ConfirmIdentity";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { CODE_LENGTH, CodeBoxes } from "@/components/CodeBoxes";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { companyErrorMessage, goToCompany, takeCompanyResult } from "@/lib/company";
import { passkeysSupported } from "@/lib/passkey";
import { NewPasskey } from "./SignIn";
import type { components } from "@/api/schema";

type Passkey = components["schemas"]["Passkey"];
type CompanyAccounts = components["schemas"]["MyCompanyAccounts"];

const MAX_PASSKEYS = 10;

function lastUsed(p: Passkey): string {
  if (!p.last_used_at) return "Not used yet";
  const days = Math.floor((Date.now() - new Date(p.last_used_at).getTime()) / 86_400_000);
  return days <= 0 ? "Last used today" : days === 1 ? "Last used yesterday" : `Last used ${days} days ago`;
}

export function AccountScreen() {
  const [me, setMe] = useState<Me | null>(null);
  const [keys, setKeys] = useState<Passkey[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<Passkey | null>(null);
  const [removing, setRemoving] = useState<Passkey | null>(null);
  const [addingPassword, setAddingPassword] = useState(false);
  const [company, setCompany] = useState<CompanyAccounts | null>(null);
  const [enrolling, setEnrolling] = useState<Enrollment | null>(null);
  const [enrollError, setEnrollError] = useState("");
  const confirm = useConfirmIdentity(me);

  // A new authenticator is a new way in: the server asks "confirm it's
  // you" first (docs/ADMIN.md §7). The old one keeps working until the new
  // one's first code is accepted.
  const startEnroll = async (): Promise<Outcome> => {
    setEnrollError("");
    const { data, error: err } = await api.POST("/api/v1/me/mfa");
    if (needsConfirm(err)) return { confirm: true };
    if (data) setEnrolling(data);
    else setEnrollError(problemMessage(err));
    return { confirm: false };
  };

  const load = useCallback(async () => {
    const [m, list, links] = await Promise.all([
      api.GET("/api/v1/me"), api.GET("/api/v1/me/passkeys"), api.GET("/api/v1/me/sso-links"),
    ]);
    if (m.data) setMe(m.data);
    if (list.data) setKeys(list.data.items);
    else setError(problemMessage(list.error));
    if (links.data) setCompany(links.data);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const full = (keys?.length ?? 0) >= MAX_PASSKEYS;
  const supported = passkeysSupported();

  return (
    <div className="w-full max-w-3xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">My account</h1>
      {me && (
        <p className="mt-1 text-sm text-muted-foreground">
          {[me.name, me.email, me.extension ? `Ext ${me.extension}` : ""].filter(Boolean).join(" · ")}
        </p>
      )}

      <section aria-labelledby="passkeys-title" className="mt-6 rounded-lg border bg-card p-5 md:p-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 id="passkeys-title" className="font-display text-lg font-semibold">Passkeys</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              Sign in with Face ID, Touch ID, Windows Hello or your phone. Nothing to type.
            </p>
          </div>
          <Button onClick={() => setAdding(true)} disabled={!supported || full || keys === null}
            title={full ? `You have ${MAX_PASSKEYS} passkeys, the most allowed.` : !supported ? "This browser can't make passkeys." : undefined}>
            <KeyRound aria-hidden="true" className="size-4" />
            Add
          </Button>
        </div>
        {full && <p className="mt-3 text-sm text-muted-foreground">You have {MAX_PASSKEYS} passkeys, the most allowed. Remove one to add another.</p>}
        {!supported && <p className="mt-3 text-sm text-muted-foreground">This browser can't make passkeys. Try a current Safari, Chrome, Edge or Firefox.</p>}
        {error && <p role="alert" className="mt-3 text-sm font-medium">{error}</p>}
        {keys && keys.length === 0 && (
          <p className="mt-4 rounded-md bg-background p-3 text-sm">No passkeys yet. A passkey is the safest way to sign in.</p>
        )}
        {keys && keys.length > 0 && (
          <ul className="mt-4 divide-y rounded-md border" aria-label="Your passkeys">
            {keys.map((k) => (
              <li key={k.id} className="flex flex-wrap items-center gap-3 px-3 py-3">
                <KeyRound aria-hidden="true" className="size-4 text-muted-foreground" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{k.name}</span>
                  <span className="block text-xs text-muted-foreground">{lastUsed(k)}{k.synced ? " · Synced" : ""}</span>
                </span>
                <Button variant="outline" size="sm" onClick={() => setRenaming(k)}>Rename</Button>
                <Button variant="outline" size="sm" onClick={() => setRemoving(k)}>Remove</Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {me && (
        <section aria-labelledby="authenticator-title" className="mt-6 flex flex-wrap items-start justify-between gap-4 rounded-lg border bg-card p-5 md:p-6">
          <div className="min-w-0 flex-1">
            <h2 id="authenticator-title" className="font-display text-lg font-semibold">Authenticator app</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              {me.mfa_enabled
                ? `On. ${me.recovery_codes_left ?? 0} recovery ${me.recovery_codes_left === 1 ? "code" : "codes"} left. `
                  + "Got a new phone, or used up or lost your recovery codes? Replace it: you get new codes too."
                : "Off. A 6-digit code from an app like Google Authenticator, 1Password or Authy, for when you sign in with your password."}
            </p>
            {enrollError && <p role="alert" className="mt-2 text-sm font-medium text-destructive">{enrollError}</p>}
          </div>
          <Button variant="outline" onClick={() => void confirm.run(startEnroll)}>
            <Smartphone aria-hidden="true" className="size-4" />
            {me.mfa_enabled ? "Replace" : "Set up"}
          </Button>
        </section>
      )}

      {me && me.has_password === false && (
        <section aria-labelledby="password-title" className="mt-6 flex flex-wrap items-center justify-between gap-4 rounded-lg border bg-card p-5 md:p-6">
          <div>
            <h2 id="password-title" className="font-display text-lg font-semibold">Password</h2>
            <p className="mt-1 text-sm text-muted-foreground">You don't have one. A password lets you sign in from a device without your passkey.</p>
          </div>
          <Button variant="outline" onClick={() => setAddingPassword(true)}>Add a password</Button>
        </section>
      )}

      {company && (company.items.length > 0 || company.available.length > 0) && (
        <CompanySection accounts={company} onChanged={() => void load()} />
      )}

      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add a passkey</DialogTitle>
            <DialogDescription>Your device will ask for Face ID, your fingerprint or its PIN.</DialogDescription>
          </DialogHeader>
          {adding && (
            <NewPasskey path="/api/v1/me/passkeys" onConfirmNeeded={confirm.ask}
              onSaved={() => { setAdding(false); void load(); }} />
          )}
        </DialogContent>
      </Dialog>

      {renaming && (
        <RenameDialog passkey={renaming} onClose={() => setRenaming(null)} onDone={() => { setRenaming(null); void load(); }} />
      )}
      {removing && me && (
        <RemoveDialog passkey={removing} me={me} last={keys?.length === 1} run={confirm.run}
          onClose={() => setRemoving(null)} onDone={() => { setRemoving(null); void load(); }} />
      )}
      {addingPassword && (
        <AddPasswordDialog run={confirm.run} onClose={() => setAddingPassword(false)}
          onDone={() => { setAddingPassword(false); void load(); }} />
      )}
      {enrolling && (
        <AuthenticatorDialog enrollment={enrolling} replacing={!!me?.mfa_enabled}
          onClose={() => { setEnrolling(null); void load(); }} />
      )}
      {confirm.dialog}
    </div>
  );
}

type Enrollment = components["schemas"]["MfaEnrollment"];

/** Scan, type the first code, then save the new recovery codes. */
function AuthenticatorDialog({ enrollment, replacing, onClose }: { enrollment: Enrollment; replacing: boolean; onClose: () => void }) {
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [retry, setRetry] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const QRCode = (await import("qrcode")).default;
      const url = await QRCode.toDataURL(enrollment.otpauth_url, { margin: 1, width: 192 });
      if (!cancelled) setQr(url);
    })();
    return () => { cancelled = true; };
  }, [enrollment.otpauth_url]);

  const send = async (value: string) => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/me/mfa/confirm", { body: { code: value.trim() } });
    setBusy(false);
    if (data) { setCodes(data.recovery_codes); return; }
    setError(problemCode(err) === "mfa_code_invalid" ? "That code isn't right. Check the time on your phone and try the next code." : problemMessage(err));
    setCode("");
    setRetry((n) => n + 1);
  };
  const submit = (e: FormEvent) => { e.preventDefault(); void send(code); };
  const text = codes?.join("\n") ?? "";
  const download = () => {
    const url = URL.createObjectURL(new Blob([`Linx recovery codes\n\n${text}\n`], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "linx-recovery-codes.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open && (!codes || saved)) onClose(); }}>
      <DialogContent>
        {codes ? (
          <>
            <DialogHeader>
              <DialogTitle>Save your new recovery codes</DialogTitle>
              <DialogDescription>
                {replacing ? "Your old authenticator and old recovery codes no longer work. " : ""}
                If you lose your phone, each of these lets you sign in once instead of a code. They won't be shown again.
              </DialogDescription>
            </DialogHeader>
            <ul className="grid grid-cols-2 gap-2 rounded-md border bg-background p-4 font-mono text-sm" aria-label="Recovery codes">
              {codes.map((c) => <li key={c}>{c}</li>)}
            </ul>
            <div className="flex gap-2">
              <Button type="button" variant="outline" className="flex-1" onClick={() => void navigator.clipboard?.writeText(text)}>Copy</Button>
              <Button type="button" variant="outline" className="flex-1" onClick={download}>Download</Button>
            </div>
            <label className="flex items-center gap-3 text-sm">
              <Checkbox checked={saved} onCheckedChange={(v) => setSaved(v === true)} />
              I've saved these codes
            </label>
            <DialogFooter>
              <Button disabled={!saved} onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{replacing ? "Replace your authenticator app" : "Set up an authenticator app"}</DialogTitle>
              <DialogDescription>
                Scan this with an app like Google Authenticator, 1Password or Authy.
                {replacing && " Your current one keeps working until this one's first code is accepted."}
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col items-center gap-3">
              {qr ? <img src={qr} alt="QR code for your authenticator app" width={192} height={192} className="rounded-md border" />
                : <div className="size-48 animate-pulse rounded-md bg-background" aria-label="Loading" />}
              <p className="text-center text-sm text-muted-foreground">
                Can't scan? Type this key instead:
                <span className="mt-1 block font-mono text-foreground break-all select-all">{enrollment.secret}</span>
              </p>
            </div>
            <form onSubmit={submit} className="flex flex-col gap-3" noValidate>
              <Label htmlFor="replace-code">6-digit code from the app</Label>
              <CodeBoxes id="replace-code" value={code} onChange={setCode} onComplete={(v) => void send(v)}
                disabled={busy} retry={retry} invalid={!!error} />
              {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
              <DialogFooter>
                <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
                <Button type="submit" disabled={busy || code.length !== CODE_LENGTH}>{replacing ? "Replace" : "Turn on"}</Button>
              </DialogFooter>
            </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

function RenameDialog({ passkey, onClose, onDone }: { passkey: Passkey; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(passkey.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    const { response, error: err } = await api.PATCH("/api/v1/me/passkeys/{id}", {
      params: { path: { id: passkey.id } }, body: { name: name.trim() },
    });
    setBusy(false);
    if (response.ok) onDone();
    else setError(problemMessage(err));
  };
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent>
        <DialogHeader><DialogTitle>Rename passkey</DialogTitle></DialogHeader>
        <form id="rename-passkey" onSubmit={submit} className="flex flex-col gap-2">
          <Label htmlFor="rename">Name</Label>
          <Input id="rename" value={name} maxLength={60} onChange={(e) => setName(e.target.value)} autoFocus disabled={busy} />
          {error && <p role="alert" className="text-sm font-medium">{error}</p>}
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="rename-passkey" disabled={busy || !name.trim()}>Save</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RemoveDialog({ passkey, me, last, run, onClose, onDone }: {
  passkey: Passkey; me: Me; last: boolean; run: (a: () => Promise<Outcome>) => Promise<void>; onClose: () => void; onDone: () => void;
}) {
  const admin = me.role === "admin" || me.role === "system_admin";
  // Removing an admin's last way past the password needs the same warning
  // as choosing a password only (ADMIN_SCREENS_PHASE1E.md §11).
  const warn = last && admin && !me.mfa_enabled;
  const [understood, setUnderstood] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const remove = () => run(async () => {
    setBusy(true);
    setError("");
    const { response, error: err } = await api.DELETE("/api/v1/me/passkeys/{id}", {
      params: { path: { id: passkey.id }, query: warn ? { accept_password_only: true } : {} },
    });
    setBusy(false);
    if (response.ok) {
      onDone();
      return { confirm: false };
    }
    if (needsConfirm(err)) return { confirm: true };
    setError(problemCode(err) === "last_sign_in_method"
      ? "This passkey is your only way to sign in. Add a password, another passkey or a company account first." : problemMessage(err));
    return { confirm: false };
  });
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Remove “{passkey.name}”?</DialogTitle>
          <DialogDescription>It won't sign in to Linx any more. The device keeps it until you delete it there too.</DialogDescription>
        </DialogHeader>
        {warn && (
          <div role="alert" className="rounded-md border-2 border-status-away p-4 text-sm">
            <p className="flex items-start gap-2 font-medium">
              <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
              Not recommended for an admin
            </p>
            <p className="mt-2 text-muted-foreground">
              After this you'd sign in with a password only. Anyone who learns or guesses it can control your whole phone
              system: add people, make expensive calls abroad, change settings.
            </p>
            <label className="mt-3 flex items-center gap-3">
              <Checkbox checked={understood} onCheckedChange={(v) => setUnderstood(v === true)} />
              I understand, use a password only
            </label>
          </div>
        )}
        {error && <p role="alert" className="text-sm font-medium">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button variant="destructive" disabled={busy || (warn && !understood)} onClick={() => void remove()}>Remove</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AddPasswordDialog({ run, onClose, onDone }:
  { run: (a: () => Promise<Outcome>) => Promise<void>; onClose: () => void; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const short = password.length < 12;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run(async () => {
      setBusy(true);
      setError("");
      const { response, error: err } = await api.POST("/api/v1/me/password", { body: { new_password: password } });
      setBusy(false);
      if (response.ok) {
        onDone();
        return { confirm: false };
      }
      if (needsConfirm(err)) return { confirm: true };
      setError(problemMessage(err));
      return { confirm: false };
    });
  };
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent>
        <DialogHeader><DialogTitle>Add a password</DialogTitle></DialogHeader>
        <form id="add-password" onSubmit={submit} className="flex flex-col gap-2">
          <Label htmlFor="new-pw">Password</Label>
          <Input id="new-pw" type="password" autoComplete="new-password" value={password}
            onChange={(e) => setPassword(e.target.value)} autoFocus disabled={busy} />
          <p className="text-sm text-muted-foreground">At least 12 characters. A few unrelated words work well.</p>
          {error && <p role="alert" className="text-sm font-medium">{error}</p>}
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="add-password" disabled={busy || short}>Save password</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Company account (ADMIN_SCREENS_PHASE1E.md §11): linked ones, and link. */
function CompanySection({ accounts, onChanged }: { accounts: CompanyAccounts; onChanged: () => void }) {
  // Coming back from linking: "Linked", or the plain reason it wasn't.
  const [status, setStatus] = useState(() => {
    const { error, result } = takeCompanyResult();
    return error ? companyErrorMessage(error) : result === "linked" ? "Linked." : "";
  });
  const [busy, setBusy] = useState("");
  const linked = new Set(accounts.items.map((l) => l.provider_id));
  const unlink = async (id: string) => {
    setBusy(id);
    setStatus("");
    const { response, error } = await api.DELETE("/api/v1/me/sso-links/{id}", { params: { path: { id } } });
    setBusy("");
    if (response.ok) onChanged();
    else setStatus(problemCode(error) === "last_sign_in_method"
      ? "This is your only way to sign in. Add a passkey or a password first." : problemMessage(error));
  };
  const link = async (providerId: string) => {
    setBusy(providerId);
    setStatus("");
    try {
      await goToCompany("/api/v1/me/sso-links", providerId);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : companyErrorMessage(""));
      setBusy("");
    }
  };
  return (
    <section aria-labelledby="company-title" className="mt-6 rounded-lg border bg-card p-5 md:p-6">
      <h2 id="company-title" className="font-display text-lg font-semibold">Company account</h2>
      <p className="mt-1 text-sm text-muted-foreground">Sign in with your work account instead of a password.</p>
      {status && <p role="status" className="mt-3 text-sm font-medium">{status}</p>}
      <ul className="mt-4 divide-y rounded-md border" aria-label="Company accounts">
        {accounts.items.map((l) => (
          <li key={l.id} className="flex flex-wrap items-center gap-3 px-3 py-3">
            <Building2 aria-hidden="true" className="size-4 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate text-sm">
              <span className="font-medium">{l.provider_name}</span>
              {l.email && <span className="text-muted-foreground">: {l.email}</span>}
            </span>
            <Button variant="outline" size="sm" disabled={!!busy} aria-busy={busy === l.id} onClick={() => void unlink(l.id)}>
              Unlink
            </Button>
          </li>
        ))}
        {accounts.available.filter((p) => !linked.has(p.id)).map((p) => (
          <li key={p.id} className="flex flex-wrap items-center gap-3 px-3 py-3">
            <Building2 aria-hidden="true" className="size-4 text-muted-foreground" />
            <span className="min-w-0 flex-1 text-sm text-muted-foreground">{p.name}: not linked</span>
            <Button variant="outline" size="sm" disabled={!!busy} aria-busy={busy === p.id} onClick={() => void link(p.id)}>
              Link {p.name}
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}
