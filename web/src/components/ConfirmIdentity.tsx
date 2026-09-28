// "Confirm it's you" (ADMIN_SCREENS_PHASE1E.md §12.2, docs/ADMIN.md §7):
// opened when an action answers 403 confirm_required; on success the action
// runs again by itself. A passkey if the person has one, the password (and
// the code, if they have an authenticator app), or a linked company account
// (and the code).
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Building2, CircleAlert, KeyRound } from "lucide-react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import { CODE_LENGTH, CodeBoxes } from "@/components/CodeBoxes";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { companyErrorMessage, confirmWithCompany } from "@/lib/company";
import { answerWithPasskey, cancelled, PasskeyError, passkeysSupported } from "@/lib/passkey";
import { onRepairPage } from "@/lib/repair";
import type { components } from "@/api/schema";

type CompanyLink = components["schemas"]["CompanyLink"];

/** What an action reports: whether it needs a fresh confirmation first. */
export type Outcome = { confirm: true } | { confirm: false };

/**
 * Wraps actions that may need "confirm it's you". `run(action)` runs it; if
 * it answers confirm_required, the dialog opens and, once confirmed, the
 * action runs again. Render `dialog` somewhere in the page.
 */
export function useConfirmIdentity(me: Me | null) {
  const [open, setOpen] = useState(false);
  const pending = useRef<(() => Promise<Outcome>) | null>(null);
  const run = useCallback(async (action: () => Promise<Outcome>) => {
    const out = await action();
    if (out.confirm) {
      pending.current = action;
      setOpen(true);
    }
  }, []);
  // Just confirm, with nothing to run again afterwards (a passkey prompt
  // needs a fresh click, so the person presses its button again).
  const ask = useCallback(() => { pending.current = null; setOpen(true); }, []);
  const dialog: ReactNode = (
    <ConfirmIdentityDialog open={open} me={me} onCancel={() => { pending.current = null; setOpen(false); }}
      onConfirmed={() => {
        setOpen(false);
        const again = pending.current;
        pending.current = null;
        if (again) void again();
      }} />
  );
  return { run, ask, dialog };
}

/** True when an API error means "confirm it's you" first. */
export const needsConfirm = (err: unknown) => problemCode(err) === "confirm_required";

function ConfirmIdentityDialog({ open, me, onCancel, onConfirmed }:
  { open: boolean; me: Me | null; onCancel: () => void; onConfirmed: () => void }) {
  const hasPasskey = (me?.passkeys ?? 0) > 0 && passkeysSupported();
  const hasPassword = me?.has_password !== false;
  const needsCode = !!me?.mfa_enabled || (me?.passkeys ?? 0) > 0;
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [retry, setRetry] = useState(0);
  // An authenticator code gets the six boxes; a recovery code stays a text field.
  const appCode = !!me?.mfa_enabled;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Linked company accounts, and whether one just came back asking for the
  // second step's code (company sign-in replaces the password, not that).
  const [company, setCompany] = useState<CompanyLink[]>([]);
  const [companyCode, setCompanyCode] = useState(false);
  const waiting = useRef<AbortController | null>(null);
  // Company sign-in comes back to the domain: not on the repair page.
  const hasCompany = (me?.company_sign_in?.length ?? 0) > 0 && !onRepairPage();
  useEffect(() => {
    if (!open || !hasCompany) return;
    void api.GET("/api/v1/me/sso-links").then(({ data }) => setCompany(data?.items ?? []));
  }, [open, hasCompany]);

  const reset = () => {
    waiting.current?.abort();
    setPassword(""); setCode(""); setError(""); setCompanyCode(false); setRetry(0);
  };
  const withCompany = async (providerId: string) => {
    setBusy(true);
    setError("");
    const ctl = new AbortController();
    waiting.current = ctl;
    try {
      const out = await confirmWithCompany(providerId, ctl.signal);
      if ("error" in out) {
        if (out.error !== "company_cancelled") setError(companyErrorMessage(out.error));
      } else if (out.result === "code_required") {
        setCompanyCode(true);
      } else {
        reset();
        onConfirmed();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : companyErrorMessage(""));
    } finally {
      setBusy(false);
    }
  };
  const withPasskey = async () => {
    setBusy(true);
    setError("");
    try {
      await answerWithPasskey("/api/v1/session/confirm/passkey");
      reset();
      onConfirmed();
    } catch (err) {
      if (!cancelled(err)) setError(err instanceof PasskeyError ? err.message : "Your passkey couldn't be checked. Try again.");
    } finally {
      setBusy(false);
    }
  };
  const send = async (value: string) => {
    setBusy(true);
    setError("");
    const { response, error: err } = await api.POST("/api/v1/session/confirm", {
      body: { ...(companyCode ? {} : { password }), ...(value.trim() ? { code: value.trim() } : {}) },
    });
    setBusy(false);
    if (response.ok) {
      reset();
      return onConfirmed();
    }
    if (["password_invalid", "mfa_code_invalid", "mfa_code_used"].includes(problemCode(err))) {
      setError(companyCode ? "That code isn't right." : "That password or code isn't right.");
    } else {
      setError(problemMessage(err));
    }
    if (appCode) { setCode(""); setRetry((n) => n + 1); }
  };
  const withPassword = (e: FormEvent) => { e.preventDefault(); void send(code); };
  // The sixth digit sends the form once there's everything else it needs.
  const codeDone = (value: string) => { if (companyCode || password) void send(value); };
  const codeReady = appCode ? code.length === CODE_LENGTH : !!code.trim();

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) { reset(); onCancel(); } }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Confirm it's you</DialogTitle>
          <DialogDescription>This change needs you to sign in again. It lasts 10 minutes.</DialogDescription>
        </DialogHeader>
        {companyCode && (
          <form id="confirm-password" onSubmit={withPassword} className="flex flex-col gap-4" noValidate>
            <p className="text-sm">Your company account checked out. Now enter your second step.</p>
            <div className="flex flex-col gap-2">
              <Label htmlFor="confirm-code">{appCode ? "Code from your authenticator app" : "Recovery code"}</Label>
              {appCode
                ? <CodeBoxes id="confirm-code" value={code} onChange={setCode} onComplete={codeDone} disabled={busy}
                    autoFocus retry={retry} invalid={!!error} />
                : <Input id="confirm-code" value={code} onChange={(e) => setCode(e.target.value)} disabled={busy}
                    autoComplete="off" className="font-mono" autoFocus />}
            </div>
          </form>
        )}
        {!companyCode && hasPasskey && (
          <Button className="h-11 w-full text-base" disabled={busy} onClick={() => void withPasskey()}>
            <KeyRound aria-hidden="true" className="size-4" />
            Use my passkey
          </Button>
        )}
        {!companyCode && hasPasskey && hasPassword && (
          <div className="flex items-center gap-3 text-sm text-muted-foreground" aria-hidden="true">
            <span className="h-px flex-1 bg-border" />or<span className="h-px flex-1 bg-border" />
          </div>
        )}
        {!companyCode && hasPassword && (
          <form id="confirm-password" onSubmit={withPassword} className="flex flex-col gap-4" noValidate>
            <div className="flex flex-col gap-2">
              <Label htmlFor="confirm-pw">Password</Label>
              <Input id="confirm-pw" type="password" autoComplete="current-password" value={password}
                onChange={(e) => setPassword(e.target.value)} disabled={busy} />
            </div>
            {needsCode && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="confirm-code">{appCode ? "Code from your authenticator app" : "Recovery code"}</Label>
                {appCode
                  ? <CodeBoxes id="confirm-code" value={code} onChange={setCode} onComplete={codeDone} disabled={busy}
                      retry={retry} invalid={!!error} />
                  : <Input id="confirm-code" value={code} onChange={(e) => setCode(e.target.value)} disabled={busy}
                      autoComplete="off" className="font-mono" />}
              </div>
            )}
          </form>
        )}
        {!companyCode && company.length > 0 && (hasPasskey || hasPassword) && (
          <div className="flex items-center gap-3 text-sm text-muted-foreground" aria-hidden="true">
            <span className="h-px flex-1 bg-border" />or<span className="h-px flex-1 bg-border" />
          </div>
        )}
        {!companyCode && company.map((l) => (
          <Button key={l.id} variant="outline" className="h-11 w-full text-base" disabled={busy} onClick={() => void withCompany(l.provider_id)}>
            <Building2 aria-hidden="true" className="size-4" />
            Continue with {l.provider_name}
          </Button>
        ))}
        {error && (
          <p role="alert" className="flex items-start gap-2 text-sm font-medium">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => { reset(); onCancel(); }} disabled={busy}>Cancel</Button>
          {companyCode && (
            <Button type="submit" form="confirm-password" disabled={busy || !codeReady}>Confirm</Button>
          )}
          {!companyCode && hasPassword && (
            <Button type="submit" form="confirm-password" disabled={busy || !password || (needsCode && !codeReady)}>
              Confirm
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
