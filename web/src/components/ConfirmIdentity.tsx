// "Confirm it's you" (ADMIN_SCREENS_PHASE1E.md §12.2, docs/ADMIN.md §7):
// opened when an action answers 403 confirm_required; on success the action
// runs again by itself. A passkey if the person has one, or the password
// (and the code, if they have an authenticator app).
import { useCallback, useRef, useState, type FormEvent, type ReactNode } from "react";
import { CircleAlert, KeyRound } from "lucide-react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { answerWithPasskey, cancelled, PasskeyError, passkeysSupported } from "@/lib/passkey";

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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const reset = () => { setPassword(""); setCode(""); setError(""); };
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
  const withPassword = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { response, error: err } = await api.POST("/api/v1/session/confirm", {
      body: { password, ...(code.trim() ? { code: code.trim() } : {}) },
    });
    setBusy(false);
    if (response.ok) {
      reset();
      onConfirmed();
    } else if (["password_invalid", "mfa_code_invalid", "mfa_code_used"].includes(problemCode(err))) {
      setError("That password or code isn't right.");
    } else {
      setError(problemMessage(err));
    }
  };

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) { reset(); onCancel(); } }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Confirm it's you</DialogTitle>
          <DialogDescription>This change needs you to sign in again. It lasts 10 minutes.</DialogDescription>
        </DialogHeader>
        {hasPasskey && (
          <Button className="h-11 w-full text-base" disabled={busy} onClick={() => void withPasskey()}>
            <KeyRound aria-hidden="true" className="size-4" />
            Use my passkey
          </Button>
        )}
        {hasPasskey && hasPassword && (
          <div className="flex items-center gap-3 text-sm text-muted-foreground" aria-hidden="true">
            <span className="h-px flex-1 bg-border" />or<span className="h-px flex-1 bg-border" />
          </div>
        )}
        {hasPassword && (
          <form id="confirm-password" onSubmit={withPassword} className="flex flex-col gap-4" noValidate>
            <div className="flex flex-col gap-2">
              <Label htmlFor="confirm-pw">Password</Label>
              <Input id="confirm-pw" type="password" autoComplete="current-password" value={password}
                onChange={(e) => setPassword(e.target.value)} disabled={busy} />
            </div>
            {needsCode && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="confirm-code">{me?.mfa_enabled ? "Code from your authenticator app" : "Recovery code"}</Label>
                <Input id="confirm-code" value={code} onChange={(e) => setCode(e.target.value)} disabled={busy}
                  autoComplete="one-time-code" className="font-mono" />
              </div>
            )}
          </form>
        )}
        {error && (
          <p role="alert" className="flex items-start gap-2 text-sm font-medium">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => { reset(); onCancel(); }} disabled={busy}>Cancel</Button>
          {hasPassword && (
            <Button type="submit" form="confirm-password" disabled={busy || !password || (needsCode && !code.trim())}>
              Confirm
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
