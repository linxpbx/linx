// My account (ADMIN_SCREENS_PHASE1E.md §11), the part built with passkeys
// (Phase 1E step 3): passkeys (add, rename, remove) and adding a password
// to a passkey-only account. The rest of the page comes with step 8.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { KeyRound, TriangleAlert } from "lucide-react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import { needsConfirm, useConfirmIdentity, type Outcome } from "@/components/ConfirmIdentity";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { passkeysSupported } from "@/lib/passkey";
import { NewPasskey } from "./SignIn";
import type { components } from "@/api/schema";

type Passkey = components["schemas"]["Passkey"];

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
  const confirm = useConfirmIdentity(me);

  const load = useCallback(async () => {
    const [m, list] = await Promise.all([api.GET("/api/v1/me"), api.GET("/api/v1/me/passkeys")]);
    if (m.data) setMe(m.data);
    if (list.data) setKeys(list.data.items);
    else setError(problemMessage(list.error));
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

      {me && me.has_password === false && (
        <section aria-labelledby="password-title" className="mt-6 flex flex-wrap items-center justify-between gap-4 rounded-lg border bg-card p-5 md:p-6">
          <div>
            <h2 id="password-title" className="font-display text-lg font-semibold">Password</h2>
            <p className="mt-1 text-sm text-muted-foreground">You don't have one. A password lets you sign in from a device without your passkey.</p>
          </div>
          <Button variant="outline" onClick={() => setAddingPassword(true)}>Add a password</Button>
        </section>
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
      {confirm.dialog}
    </div>
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
      ? "This passkey is your only way to sign in. Add a password or another passkey first." : problemMessage(err));
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
