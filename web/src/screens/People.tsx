// People (docs/ui/ADMIN_SCREENS_PHASE1E.md §4): list, add (guided or quick),
// and the person detail sheet.
import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { ChevronDown, Search } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { AddChooserDialog, useAlwaysQuickAdd } from "@/components/AddChooser";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { DataTable } from "@/components/DataTable";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { navigate } from "@/hooks/useRoute";
import { isReadOnlyAdmin } from "@/lib/roles";

type User = components["schemas"]["User"];
type Role = components["schemas"]["Role"];

const roleLabel: Record<Role, string> = { system_admin: "System admin", admin: "Admin", user: "Person", reporter: "Read-only admin" };
// The same three choices, in the same words, as the setup wizard's People
// step: Person, Admin, Phone (owner, Phase 1E demo). The API's read-only
// "reporter" role isn't offered; anyone who has it shows as a read-only admin.
type Kind = "user" | "admin" | "phone";
const kindLabel: Record<Kind, string> = { user: "Person", admin: "Admin", phone: "Phone" };
const roleHint: Record<Kind, string> = {
  user: "Someone who signs in to Linx to make and take calls. Gets an invite link by email address.",
  admin: "A person who can also manage Linx (people, extensions, phone lines).",
  phone: "An extension for a phone that isn't someone's, like a door phone or a meeting room. No email.",
};

type Status = "invited" | "active" | "locked" | "disabled";
const statusWords: Record<Status, string> = { invited: "Invited", active: "Active", locked: "Locked", disabled: "Disabled" };
const statusTone: Record<Status, "good" | "bad" | "neutral" | "warn"> = { invited: "neutral", active: "good", locked: "bad", disabled: "neutral" };

function statusOf(u: User): Status {
  if (u.disabled) return "disabled";
  if (u.locked) return "locked";
  const setUp = u.has_password || (u.passkeys ?? 0) > 0 || u.password_only || (u.company_sign_in?.length ?? 0) > 0;
  if (!setUp) return "invited";
  return "active";
}

function signInWords(u: User): string {
  if (u.company_sign_in?.length) return u.company_sign_in[0]!;
  if ((u.passkeys ?? 0) > 0) return "Passkey";
  if (u.mfa_enabled) return "Password + app";
  if (u.password_only) return "Password only";
  if (u.has_password) return "Password";
  return "—";
}

/** Can beyondCaller ever refuse acting on target, from what the browser already knows. */
function canAct(me: Me, target: User): boolean {
  if (me.role === "system_admin") return true;
  if (me.role === "admin") return target.role !== "system_admin";
  return false;
}

function Field({ label, htmlFor, children }: { label: string; htmlFor: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}

function FormError({ message }: { message: string }) {
  if (!message) return null;
  return <p role="alert" className="text-sm font-medium text-destructive">{message}</p>;
}

// --- Add a person ---

type NewPerson = { name: string; email: string; role: "user" | "reporter" | "admin"; number: string; giveExtension: boolean };

// Re-fetches whenever `trigger` changes (each time an Add flow opens): the
// next free number moves as people are added, so a value fetched once at
// mount would go stale across repeated adds in the same visit.
function useNextNumber(trigger: unknown) {
  const [digits, setDigits] = useState(3);
  const [number, setNumber] = useState("");
  useEffect(() => {
    void Promise.all([api.GET("/api/v1/settings"), api.GET("/api/v1/numbering/next")]).then(([s, n]) => {
      if (s.data) setDigits(s.data.extension_digits);
      if (n.data) setNumber(n.data.number);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [trigger]);
  return { digits, number };
}

async function createPerson(p: NewPerson, confirmRun: (a: () => Promise<{ confirm: boolean }>) => Promise<void>, onDone: (u: User, token: string) => void, onError: (m: string) => void) {
  let extensionId: string | undefined;
  if (p.giveExtension && p.number.trim()) {
    const ext = await api.POST("/api/v1/extensions", { body: { number: p.number.trim(), display_name: p.name.trim() } });
    if (!ext.data) { onError(problemMessage(ext.error)); return; }
    extensionId = ext.data.id;
  }
  await confirmRun(async () => {
    const { data, error: err } = await api.POST("/api/v1/users", {
      body: { name: p.name.trim(), email: p.email.trim(), role: p.role, extension_id: extensionId },
    });
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { onError(problemMessage(err)); return { confirm: false }; }
    onDone(data.user, data.setup_link_token);
    return { confirm: false };
  });
}

function QuickAddPerson({ me, open, onOpenChange, onGuideInstead, always, onCreated }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; onGuideInstead: () => void; always: boolean;
  onCreated: (u: User, token: string) => void;
}) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const next = useNextNumber(open);
  const confirm = useConfirmIdentity(me);

  useEffect(() => { if (open) { setName(""); setEmail(""); setError(""); } }, [open]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    await createPerson({ name, email, role: "user", number: next.number, giveExtension: true },
      confirm.run, (u, token) => { onOpenChange(false); onCreated(u, token); }, setError);
    setBusy(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>Add a person</DialogTitle></DialogHeader>
        {always && (
          <button type="button" className="-mt-2 self-start text-sm text-link underline-offset-4 hover:underline" onClick={() => { onOpenChange(false); onGuideInstead(); }}>
            Guide me instead
          </button>
        )}
        <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
          <Field label="Name" htmlFor="quick-person-name"><Input id="quick-person-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus /></Field>
          <Field label="Email" htmlFor="quick-person-email"><Input id="quick-person-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
          <p className="text-sm text-muted-foreground">Everything else uses safe defaults. You can change it any time.</p>
          <FormError message={error} />
          <DialogFooter>
            <Button type="submit" disabled={busy || !name.trim() || !email.trim()} aria-busy={busy}>Add</Button>
          </DialogFooter>
        </form>
        {confirm.dialog}
      </DialogContent>
    </Dialog>
  );
}

const STEPS = ["Name", "Type", "Extension", "Done"];

function GuidedAddPerson({ me, open, onOpenChange, onCreated }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; onCreated: (u: User, token: string) => void;
}) {
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Kind>("user");
  const [extChoice, setExtChoice] = useState<"next" | "other" | "none">("next");
  const [number, setNumber] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ user: User; link: string } | null>(null);
  const [phoneDone, setPhoneDone] = useState("");
  const next = useNextNumber(open);
  const confirm = useConfirmIdentity(me);

  useEffect(() => {
    if (!open) return;
    setStep(1); setName(""); setEmail(""); setRole("user"); setExtChoice("next"); setError(""); setResult(null); setPhoneDone("");
  }, [open]);
  useEffect(() => { if (extChoice === "next") setNumber(next.number); }, [extChoice, next.number]);

  const phone = role === "phone";
  const finish = async () => {
    setBusy(true);
    setError("");
    if (phone) {
      // An extension, no person (as the setup wizard's Phone rows).
      const { data, error: err } = await api.POST("/api/v1/extensions", { body: { number: number.trim(), display_name: name.trim() } });
      setBusy(false);
      if (!data) { setError(problemMessage(err)); return; }
      setPhoneDone(data.number);
      return;
    }
    await createPerson(
      { name, email, role: role === "admin" ? "admin" : "user", number, giveExtension: extChoice !== "none" },
      confirm.run,
      (u, token) => setResult({ user: u, link: `${window.location.origin}/setup/${token}` }),
      setError,
    );
    setBusy(false);
  };

  const discard = () => {
    if (step > 1 && step < 4 && !window.confirm("Discard this?")) return;
    onOpenChange(false);
  };

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) discard(); else onOpenChange(o); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add a person</SheetTitle>
          <SheetDescription>
            {STEPS.map((s, i) => `${i + 1} ${s}`).join(" · ")}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4">
          {step === 1 && (
            <>
              <Field label="Name" htmlFor="guided-person-name"><Input id="guided-person-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus /></Field>
              <Field label="Email (not needed for a phone that isn't someone's)" htmlFor="guided-person-email"><Input id="guided-person-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
            </>
          )}
          {step === 2 && (
            <RadioGroup value={role} onValueChange={(v) => setRole(v as typeof role)} aria-label="Type">
              {(["user", "admin", "phone"] as const).map((r) => (
                <label key={r} htmlFor={`role-${r}`} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                  <RadioGroupItem id={`role-${r}`} value={r} className="mt-0.5" />
                  <span className="flex flex-col gap-0.5">
                    <span className="text-sm font-medium">
                      {kindLabel[r]}{r === "user" && <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>}
                    </span>
                    <span className="text-sm text-muted-foreground">{roleHint[r]}</span>
                  </span>
                </label>
              ))}
            </RadioGroup>
          )}
          {step === 3 && (
            <RadioGroup value={extChoice} onValueChange={(v) => setExtChoice(v as typeof extChoice)} aria-label="Extension">
              <label htmlFor="ext-next" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                <RadioGroupItem id="ext-next" value="next" className="mt-0.5" />
                <span className="flex flex-col gap-0.5">
                  <span className="text-sm font-medium">Give them extension {next.number} <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span></span>
                  <span className="text-sm text-muted-foreground">The next free number in the people range.</span>
                </span>
              </label>
              <label htmlFor="ext-other" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                <RadioGroupItem id="ext-other" value="other" className="mt-0.5" />
                <span className="flex flex-1 flex-col gap-1.5">
                  <span className="text-sm font-medium">Pick another number</span>
                  {extChoice === "other" && (
                    <Input className="font-mono" value={number} onChange={(e) => setNumber(e.target.value)} placeholder={next.number} />
                  )}
                </span>
              </label>
              {!phone && (
                <label htmlFor="ext-none" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                  <RadioGroupItem id="ext-none" value="none" className="mt-0.5" />
                  <span className="text-sm font-medium">No phone</span>
                </label>
              )}
            </RadioGroup>
          )}
          {step === 2 && !phone && !email.trim() && (
            <p className="text-sm text-status-away">A person needs an email for their invite link: go back and add it. For a door phone or a room, choose Phone.</p>
          )}
          {step === 4 && (
            phoneDone ? (
              <p className="text-sm">Extension {phoneDone} ({name}) is ready. Add the phone itself (and get its login) under Extensions.</p>
            ) : result ? (
              <InviteResult name={name} link={result.link} />
            ) : (
              <p className="text-sm text-muted-foreground">Ready to create {name || (phone ? "this extension" : "this person")}.</p>
            )
          )}
          <FormError message={error} />
        </div>
        <div className="mt-auto flex items-center justify-between gap-2 border-t p-4">
          <Button variant="outline" disabled={step === 1 || busy || !!result || !!phoneDone} onClick={() => setStep(step - 1)}>Back</Button>
          {step < 4 && (
            <Button disabled={(step === 1 && !name.trim()) || (step === 2 && !phone && !email.trim())
              || (step === 3 && extChoice === "other" && !number.trim())}
              onClick={() => { if (step === 2 && phone && extChoice === "none") setExtChoice("next"); setStep(step + 1); }}>
              Next
            </Button>
          )}
          {step === 4 && !result && !phoneDone && (
            <Button disabled={busy} aria-busy={busy} onClick={() => void finish()}>Create</Button>
          )}
          {step === 4 && phoneDone && (
            <Button onClick={() => { onOpenChange(false); navigate("/admin/extensions"); }}>Go to Extensions</Button>
          )}
          {step === 4 && result && (
            <Button onClick={() => { onCreated(result.user, ""); onOpenChange(false); }}>Done</Button>
          )}
        </div>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

function InviteResult({ name, link }: { name: string; link: string }) {
  const [qr, setQr] = useState("");
  useEffect(() => { void (async () => setQr(await (await import("qrcode")).default.toDataURL(link, { margin: 1, width: 160 })))(); }, [link]);
  return (
    <div className="flex flex-col items-center gap-3 rounded-md border bg-card p-4 text-center">
      <p className="text-sm">Send this to {name}. It works once, for 24 hours.</p>
      {qr && <img src={qr} alt={`QR code to invite ${name}`} width={160} height={160} className="rounded-sm border" />}
      <p className="w-full truncate rounded-md bg-background p-2 font-mono text-xs">{link}</p>
      <Button type="button" variant="outline" onClick={() => void navigator.clipboard?.writeText(link)}>Copy link</Button>
    </div>
  );
}

// --- Person detail sheet ---

function PersonSheet({ me, user, onClose, onChanged, onDisabled }: {
  me: Me; user: User; onClose: () => void; onChanged: (u: User) => void; onDisabled: (id: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(user.name);
  const [email, setEmail] = useState(user.email);
  const [role, setRole] = useState<Role>(user.role);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [invite, setInvite] = useState<{ link: string } | null>(null);
  const [confirmDisable, setConfirmDisable] = useState(false);
  const confirm = useConfirmIdentity(me);
  const allowed = canAct(me, user);
  const status = statusOf(user);

  const save = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const patch: Record<string, unknown> = {};
    if (name.trim() !== user.name) patch.name = name.trim();
    if (email.trim().toLowerCase() !== user.email) patch.email = email.trim();
    if (role !== user.role) patch.role = role;
    const { data, error: err } = await api.PATCH("/api/v1/users/{id}", {
      params: { path: { id: user.id }, header: { "If-Match": user.etag } }, body: patch,
    });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    onChanged(data);
    setEditing(false);
    return { confirm: false };
  });

  const newInviteLink = async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/users/{id}/setup-link", { params: { path: { id: user.id } } });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setInvite({ link: `${window.location.origin}/setup/${data.setup_link_token}` });
  };

  const resetAuthenticator = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/users/{id}/reset-mfa", { params: { path: { id: user.id } } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    onChanged(data);
    return { confirm: false };
  });

  const unlock = async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/users/{id}/unlock", { params: { path: { id: user.id } } });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    onChanged(data);
  };

  const disable = async () => {
    setBusy(true);
    setError("");
    const { response, error: err } = await api.PATCH("/api/v1/users/{id}", {
      params: { path: { id: user.id }, header: { "If-Match": user.etag } }, body: { disabled: true },
    });
    setBusy(false);
    setConfirmDisable(false);
    if (!response.ok) { setError(problemMessage(err)); return; }
    onDisabled(user.id);
    onClose();
  };

  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{user.name}</SheetTitle>
          <SheetDescription>
            {roleLabel[user.role]} · {statusWords[status]}
            {status === "invited" && " · Hasn't used their invite yet"}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 overflow-y-auto px-4 pb-4">
          <section>
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-semibold">Details</h3>
              {allowed && !editing && <Button size="sm" variant="outline" onClick={() => setEditing(true)}>Edit</Button>}
              {!allowed && <span className="text-xs text-muted-foreground" title="Only a system admin can change this">Only a system admin can change this</span>}
            </div>
            {!editing ? (
              <dl className="mt-2 grid grid-cols-[6rem_1fr] gap-y-1 text-sm">
                <dt className="text-muted-foreground">Name</dt><dd>{user.name}</dd>
                <dt className="text-muted-foreground">Email</dt><dd>{user.email}</dd>
                <dt className="text-muted-foreground">Role</dt><dd>{roleLabel[user.role]}</dd>
              </dl>
            ) : (
              <div className="mt-2 flex flex-col gap-3">
                <Field label="Name" htmlFor="edit-person-name"><Input id="edit-person-name" value={name} onChange={(e) => setName(e.target.value)} /></Field>
                <Field label="Email" htmlFor="edit-person-email">
                  <Input id="edit-person-email" type="email" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
                </Field>
                {email.trim().toLowerCase() !== user.email && (
                  <p className="text-sm text-muted-foreground">
                    They'll sign in with the new email from now on.
                    {(user.company_sign_in?.length ?? 0) > 0 && " Their company account is unlinked; they can link it again from My account."}
                  </p>
                )}
                <Field label="Role" htmlFor="edit-person-role">
                  <Select value={role} onValueChange={(v) => setRole(v as Role)} disabled={user.role === "system_admin"}>
                    <SelectTrigger id="edit-person-role"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="user">Person</SelectItem>
                      {user.role === "reporter" && <SelectItem value="reporter">Read-only admin</SelectItem>}
                      <SelectItem value="admin">Admin</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                <div className="flex gap-2">
                  <Button size="sm" disabled={busy} onClick={() => void save()}>Save</Button>
                  <Button size="sm" variant="outline" onClick={() => { setEditing(false); setName(user.name); setEmail(user.email); setRole(user.role); }}>Cancel</Button>
                </div>
              </div>
            )}
          </section>

          <section>
            <h3 className="text-sm font-semibold">Sign-in</h3>
            <p className="mt-2 text-sm text-muted-foreground">
              Passkeys: {user.passkeys ?? 0} · Authenticator: {user.mfa_enabled ? "on" : "off"}
              {user.password_only && <span className="ms-2 text-status-away">Password only ⚠</span>}
            </p>
            {(user.company_sign_in?.length ?? 0) > 0 && (
              <p className="mt-1 text-sm text-muted-foreground">Company account: {user.company_sign_in!.join(", ")}</p>
            )}
            {allowed && (
              <Button size="sm" variant="outline" className="mt-2" disabled={busy} onClick={() => void newInviteLink()}>
                {status === "invited" ? "Copy new invite link" : "New invite link"}
              </Button>
            )}
            {invite && (
              <div className="mt-2 flex flex-col gap-2 rounded-md border bg-card p-3">
                <p className="text-sm text-muted-foreground">Works once, for 24 hours. You won't see this again.</p>
                <p className="truncate rounded-md bg-background p-2 font-mono text-xs">{invite.link}</p>
                <div className="flex gap-2">
                  <Button size="sm" variant="outline" onClick={() => void navigator.clipboard?.writeText(invite.link)}>Copy</Button>
                  <Button size="sm" variant="outline" onClick={() => setInvite(null)}>I've saved it</Button>
                </div>
              </div>
            )}
          </section>

          <FormError message={error} />

          {allowed && (
            <section className="rounded-md border border-destructive/30 p-3">
              <h3 className="text-sm font-semibold text-destructive">Danger zone</h3>
              <div className="mt-2 flex flex-col items-start gap-2">
                <Button size="sm" variant="outline" disabled={busy} onClick={() => void resetAuthenticator()}>Reset authenticator</Button>
                {status === "locked" && <Button size="sm" variant="outline" disabled={busy} onClick={() => void unlock()}>Unlock</Button>}
                {!user.disabled && (
                  <Button size="sm" variant="outline" disabled={busy} onClick={() => setConfirmDisable(true)}>Disable</Button>
                )}
              </div>
            </section>
          )}
        </div>
        <Dialog open={confirmDisable} onOpenChange={setConfirmDisable}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Disable {user.name}?</DialogTitle>
              <DialogDescription>They'll be signed out everywhere and their browser phone stops working.</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirmDisable(false)}>Cancel</Button>
              <Button variant="destructive" disabled={busy} onClick={() => void disable()}>Disable</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

// --- List ---

export function PeopleScreen({ me }: { me: Me }) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [query, setQuery] = useState("");
  const [roleFilter, setRoleFilter] = useState<Set<Role>>(new Set());
  const [statusFilter, setStatusFilter] = useState<Set<Status>>(new Set());
  const [chooserOpen, setChooserOpen] = useState(false);
  const [guidedOpen, setGuidedOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [always, setAlways] = useAlwaysQuickAdd("people");
  const [selected, setSelected] = useState<User | null>(null);
  const [justInvited, setJustInvited] = useState<{ user: User; link: string } | null>(null);
  const readOnly = isReadOnlyAdmin(me);

  const load = useCallback(async () => {
    const { data } = await api.GET("/api/v1/users", { params: { query: { limit: 200 } } });
    if (data) setUsers(data.items);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const q = query.trim().toLowerCase();
  const rows = useMemo(() => (users ?? []).filter((u) => {
    if (q && !u.name.toLowerCase().includes(q) && !u.email.toLowerCase().includes(q)) return false;
    if (roleFilter.size && !roleFilter.has(u.role)) return false;
    if (statusFilter.size && !statusFilter.has(statusOf(u))) return false;
    return true;
  }), [users, q, roleFilter, statusFilter]);

  const columns: ColumnDef<User>[] = [
    { accessorKey: "name", header: "Name" },
    { id: "role", header: "Role", accessorFn: (u) => roleLabel[u.role] },
    { id: "signin", header: "Sign-in", accessorFn: signInWords },
    {
      id: "status", header: "Status", accessorFn: (u) => statusWords[statusOf(u)],
      cell: ({ row }) => {
        const s = statusOf(row.original);
        return <span className="flex items-center gap-2"><Dot tone={statusTone[s]} />{statusWords[s]}</span>;
      },
    },
  ];

  if (users === null) return <div className="p-6" aria-busy="true" />;

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-semibold tracking-tight">People</h1>
          <p className="mt-1 text-sm text-muted-foreground">{users.length} {users.length === 1 ? "person" : "people"}</p>
        </div>
        {!readOnly && <Button onClick={() => (always ? setQuickOpen(true) : setChooserOpen(true))}>+ Add</Button>}
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <div className="relative w-full max-w-xs">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-9" aria-label="Search people" />
        </div>
        <FilterChip label="Role" options={[["system_admin", "System admin"], ["admin", "Admin"], ["user", "Person"],
          ...((users ?? []).some((u) => u.role === "reporter") ? [["reporter", "Read-only admin"] as ["reporter", string]] : [])]}
          selected={roleFilter} onChange={setRoleFilter} />
        <FilterChip label="Status" options={[["invited", "Invited"], ["active", "Active"], ["locked", "Locked"], ["disabled", "Disabled"]]}
          selected={statusFilter} onChange={setStatusFilter} />
      </div>

      <div className="mt-4">
        {users.length === 0 ? (
          <EmptyState onGuide={() => setGuidedOpen(true)} onQuick={() => setQuickOpen(true)} readOnly={readOnly} />
        ) : (
          <DataTable columns={columns} data={rows} onRowClick={setSelected} />
        )}
      </div>

      {justInvited && (
        <div className="fixed inset-x-0 bottom-4 z-40 mx-auto w-full max-w-sm rounded-md border bg-card p-3 shadow-md">
          <InviteResult name={justInvited.user.name} link={justInvited.link} />
          <Button size="sm" variant="outline" className="mt-2 w-full" onClick={() => setJustInvited(null)}>Close</Button>
        </div>
      )}

      <AddChooserDialog open={chooserOpen} onOpenChange={setChooserOpen} title="Add a person"
        guideHint="A few short steps, each explained, with our pick." quickHint="Just a name and email. Change the rest later."
        always={always} onAlwaysChange={setAlways} onGuide={() => setGuidedOpen(true)} onQuick={() => setQuickOpen(true)} />
      <GuidedAddPerson me={me} open={guidedOpen} onOpenChange={setGuidedOpen}
        onCreated={() => { void load(); setGuidedOpen(false); }} />
      <QuickAddPerson me={me} open={quickOpen} onOpenChange={setQuickOpen} always={always} onGuideInstead={() => setGuidedOpen(true)}
        onCreated={(u, token) => { void load(); setJustInvited(token ? { user: u, link: `${window.location.origin}/setup/${token}` } : null); }} />
      {selected && (
        <PersonSheet me={me} user={selected} onClose={() => setSelected(null)}
          onChanged={(u) => { setSelected(u); setUsers((list) => list?.map((x) => (x.id === u.id ? u : x)) ?? list); }}
          onDisabled={() => void load()} />
      )}
    </div>
  );
}

function FilterChip<T extends string>({ label, options, selected, onChange }: {
  label: string; options: [T, string][]; selected: Set<T>; onChange: (s: Set<T>) => void;
}) {
  const toggle = (v: T) => {
    const next = new Set(selected);
    if (next.has(v)) next.delete(v); else next.add(v);
    onChange(next);
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm">
          {label}{selected.size > 0 && ` (${selected.size})`}
          <ChevronDown aria-hidden="true" className="size-3.5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {options.map(([value, text]) => (
          <DropdownMenuCheckboxItem key={value} checked={selected.has(value)} onCheckedChange={() => toggle(value)}>
            {text}
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function EmptyState({ onGuide, onQuick, readOnly }: { onGuide: () => void; onQuick: () => void; readOnly: boolean }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
      <p className="text-sm text-muted-foreground">People are who signs in to Linx: to make calls, or to help run it.</p>
      {!readOnly && (
        <div className="flex gap-2">
          <Button variant="outline" onClick={onGuide}>Guide me</Button>
          <Button onClick={onQuick}>Quick add</Button>
        </div>
      )}
    </div>
  );
}
