// Extensions and devices (docs/ui/ADMIN_SCREENS_PHASE1E.md §5): list, add
// (guided or quick), the extension detail sheet with its phones.
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { KeyRound, Search } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { AddChooserDialog, useAlwaysQuickAdd } from "@/components/AddChooser";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { DataTable } from "@/components/DataTable";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { navigate } from "@/hooks/useRoute";
import { isReadOnlyAdmin } from "@/lib/roles";

type Extension = components["schemas"]["Extension"];
type User = components["schemas"]["User"];
type Device = components["schemas"]["Device"];
type DeviceCredentials = components["schemas"]["DeviceCredentials"];

const kindLabel: Record<string, string> = { web: "Browser", ios: "iPhone", softphone: "Desk phone", desk: "Desk phone" };

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

// Re-fetches whenever `trigger` changes (each time an Add flow opens): the
// next free number moves as extensions are added.
function useNextNumber(trigger: unknown) {
  const [number, setNumber] = useState("");
  useEffect(() => {
    void api.GET("/api/v1/numbering/next").then(({ data }) => { if (data) setNumber(data.number); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [trigger]);
  return number;
}

// --- The device settings show-once box (docs/ui/ADMIN_SCREENS_PHASE1E.md §5.4) ---

function DeviceCredentialsBox({ creds, onClose }: { creds: DeviceCredentials; onClose: () => void }) {
  const [showPassword, setShowPassword] = useState(false);
  const [howTo, setHowTo] = useState(false);
  return (
    <div className="flex flex-col gap-3 rounded-md border bg-card p-4">
      <p className="font-medium">Settings for "{creds.device.name}"</p>
      <dl className="grid grid-cols-[6rem_1fr] gap-y-1.5 text-sm">
        <dt className="text-muted-foreground">Server</dt><dd className="font-mono">{creds.server}</dd>
        <dt className="text-muted-foreground">Port</dt><dd className="font-mono">{creds.port}</dd>
        <dt className="text-muted-foreground">Transport</dt><dd className="uppercase">{creds.transport}</dd>
        <dt className="text-muted-foreground">Username</dt>
        <dd className="flex items-center gap-2 font-mono">
          {creds.device.sip_username}
          <Button size="sm" variant="outline" onClick={() => void navigator.clipboard?.writeText(creds.device.sip_username)}>Copy</Button>
        </dd>
        <dt className="text-muted-foreground">Password</dt>
        <dd className="flex items-center gap-2 font-mono">
          {showPassword ? creds.password : "••••••••"}
          <Button size="sm" variant="outline" onClick={() => setShowPassword((v) => !v)}>{showPassword ? "Hide" : "Show"}</Button>
          <Button size="sm" variant="outline" onClick={() => void navigator.clipboard?.writeText(creds.password)}>Copy</Button>
        </dd>
        <dt className="text-muted-foreground">Audio</dt><dd>SRTP (required)</dd>
      </dl>
      <button type="button" className="self-start text-sm text-link underline-offset-4 hover:underline" onClick={() => setHowTo(!howTo)}>
        {howTo ? "Hide" : ""} How to enter this on common phones
      </button>
      {howTo && <p className="whitespace-pre-wrap rounded-md bg-background p-3 text-sm text-muted-foreground">{creds.settings_text}</p>}
      <p className="text-sm text-muted-foreground">You won't see the password again. Lost it? Use "New password".</p>
      <Button onClick={onClose}>I've saved it</Button>
    </div>
  );
}

// --- Add a desk phone or phone app (docs/ui/ADMIN_SCREENS_PHASE1E.md §5.4) ---

function AddDevice({ me, extensionId, personName, onDone, onSkip }: {
  me: Me; extensionId: string; personName: string; onDone: (creds: DeviceCredentials) => void; onSkip?: () => void;
}) {
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [what, setWhat] = useState<"desk" | "app">("desk");
  const [name, setName] = useState(`${personName ? `${personName}'s` : "New"} ${what === "desk" ? "desk" : "phone"}`);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [creds, setCreds] = useState<DeviceCredentials | null>(null);
  const confirm = useConfirmIdentity(me);

  const create = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/extensions/{id}/devices", { params: { path: { id: extensionId } }, body: { name: name.trim() } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setCreds(data);
    setStep(3);
    return { confirm: false };
  });

  if (step === 3 && creds) return <DeviceCredentialsBox creds={creds} onClose={() => onDone(creds)} />;

  return (
    <div className="flex flex-col gap-4">
      {step === 1 && (
        <RadioGroup value={what} onValueChange={(v) => { setWhat(v as typeof what); setName(`${personName ? `${personName}'s` : "New"} ${v === "desk" ? "desk" : "phone"}`); }}>
          <label htmlFor="what-desk" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
            <RadioGroupItem id="what-desk" value="desk" className="mt-0.5" />
            <span className="text-sm font-medium">Desk phone</span>
          </label>
          <label htmlFor="what-app" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
            <RadioGroupItem id="what-app" value="app" className="mt-0.5" />
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-medium">Phone app</span>
              <span className="text-sm text-muted-foreground">On a mobile or computer (Grandstream Wave, Zoiper, …).</span>
            </span>
          </label>
        </RadioGroup>
      )}
      {step === 2 && <Field label="Name" htmlFor="add-device-name"><Input id="add-device-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus /></Field>}
      <FormError message={error} />
      <div className="flex justify-between gap-2">
        {onSkip && <Button variant="outline" onClick={onSkip} disabled={busy}>Later</Button>}
        <div className="ms-auto flex gap-2">
          {step === 2 && <Button variant="outline" onClick={() => setStep(1)} disabled={busy}>Back</Button>}
          <Button disabled={busy || !name.trim()} aria-busy={busy}
            onClick={() => (step === 1 ? setStep(2) : void create())}>
            {step === 1 ? "Next" : "Create"}
          </Button>
        </div>
      </div>
      {confirm.dialog}
    </div>
  );
}

// --- Add an extension ---

type ExtChoice = "next" | "other";

function GuidedAddExtension({ me, open, onOpenChange, people, onDone }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; people: User[]; onDone: () => void;
}) {
  const [step, setStep] = useState(1);
  const next = useNextNumber(open);
  const [choice, setChoice] = useState<ExtChoice>("next");
  const [number, setNumber] = useState("");
  const [name, setName] = useState("");
  const [personId, setPersonId] = useState<string>("");
  const [addPhone, setAddPhone] = useState<"yes" | "later">("later");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<Extension | null>(null);
  const [creds, setCreds] = useState<DeviceCredentials | null>(null);
  const [deviceDone, setDeviceDone] = useState(false);

  useEffect(() => {
    if (!open) return;
    setStep(1); setChoice("next"); setName(""); setPersonId(""); setAddPhone("later"); setError(""); setCreated(null); setCreds(null); setDeviceDone(false);
  }, [open]);
  useEffect(() => { if (choice === "next") setNumber(next); }, [choice, next]);

  const person = people.find((p) => p.id === personId);
  const personName = () => person?.name ?? "";

  const create = async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/extensions", { body: { number: number.trim(), display_name: name.trim() } });
    if (!data) { setBusy(false); setError(problemMessage(err)); return; }
    if (personId) {
      const { error: perr } = await api.PATCH("/api/v1/users/{id}", { params: { path: { id: personId } }, body: { extension_id: data.id } });
      if (perr) { setBusy(false); setError(problemMessage(perr)); return; }
    }
    setBusy(false);
    setCreated(data);
    setStep(5);
  };

  const discard = () => {
    if (step > 1 && step < 5 && !window.confirm("Discard this?")) return;
    onOpenChange(false);
  };

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) discard(); else onOpenChange(o); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add an extension</SheetTitle>
          <SheetDescription>1 Number · 2 Name · 3 Person · 4 Phone · 5 Done</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4">
          {step === 1 && (
            <RadioGroup value={choice} onValueChange={(v) => setChoice(v as ExtChoice)}>
              <label htmlFor="num-next" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                <RadioGroupItem id="num-next" value="next" className="mt-0.5" />
                <span className="text-sm font-medium">Number {next} <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span></span>
              </label>
              <label htmlFor="num-other" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                <RadioGroupItem id="num-other" value="other" className="mt-0.5" />
                <span className="flex flex-1 flex-col gap-1.5">
                  <span className="text-sm font-medium">Pick another number</span>
                  {choice === "other" && <Input className="font-mono" value={number} onChange={(e) => setNumber(e.target.value)} />}
                </span>
              </label>
            </RadioGroup>
          )}
          {step === 2 && (
            <Field label="Name" htmlFor="add-ext-name">
              <Input id="add-ext-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Reception, Kitchen…" autoFocus />
            </Field>
          )}
          {step === 3 && (
            <Field label="Belongs to a person?" htmlFor="add-ext-person">
              <Select value={personId || "none"} onValueChange={(v) => setPersonId(v === "none" ? "" : v)}>
                <SelectTrigger id="add-ext-person"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">Nobody</SelectItem>
                  {people.filter((p) => !p.extension_id).map((p) => <SelectItem key={p.id} value={p.id}>{p.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          )}
          {step === 4 && (
            <RadioGroup value={addPhone} onValueChange={(v) => setAddPhone(v as typeof addPhone)}>
              <label htmlFor="phone-yes" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                <RadioGroupItem id="phone-yes" value="yes" className="mt-0.5" />
                <span className="text-sm font-medium">Add a desk phone or phone app now</span>
              </label>
              <label htmlFor="phone-later" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                <RadioGroupItem id="phone-later" value="later" className="mt-0.5" />
                <span className="text-sm font-medium">Later</span>
              </label>
            </RadioGroup>
          )}
          {step === 5 && created && (
            addPhone === "yes" && !creds && !deviceDone ? (
              <AddDevice me={me} extensionId={created.id} personName={personName()} onDone={setCreds} onSkip={() => setDeviceDone(true)} />
            ) : (
              <p className="text-sm text-muted-foreground">Extension {created.number} ({created.display_name}) is ready.</p>
            )
          )}
          <FormError message={error} />
        </div>
        <div className="mt-auto flex items-center justify-between gap-2 border-t p-4">
          <Button variant="outline" disabled={step === 1 || busy || !!created} onClick={() => setStep(step - 1)}>Back</Button>
          {step < 4 && (
            <Button disabled={(step === 1 && choice === "other" && !number.trim()) || (step === 2 && !name.trim())}
              onClick={() => setStep(step + 1)}>
              Next
            </Button>
          )}
          {step === 4 && <Button disabled={busy} aria-busy={busy} onClick={() => void create()}>Create</Button>}
          {step === 5 && (creds || deviceDone || addPhone === "later") && <Button onClick={onDone}>Done</Button>}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function QuickAddExtension({ open, onOpenChange, onGuideInstead, always, onDone }: {
  open: boolean; onOpenChange: (o: boolean) => void; onGuideInstead: () => void; always: boolean; onDone: () => void;
}) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const next = useNextNumber(open);

  useEffect(() => { if (open) { setName(""); setError(""); } }, [open]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/extensions", { body: { number: next, display_name: name.trim() } });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    onOpenChange(false);
    onDone();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>Add an extension</DialogTitle></DialogHeader>
        {always && (
          <button type="button" className="-mt-2 self-start text-sm text-link underline-offset-4 hover:underline" onClick={() => { onOpenChange(false); onGuideInstead(); }}>
            Guide me instead
          </button>
        )}
        <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
          <Field label="Name" htmlFor="quick-ext-name"><Input id="quick-ext-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="Reception, Kitchen…" /></Field>
          <p className="text-sm text-muted-foreground">Number {next}, no person. You can change it any time.</p>
          <FormError message={error} />
          <DialogFooter><Button type="submit" disabled={busy || !name.trim()} aria-busy={busy}>Add</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// --- Extension detail sheet ---

function ExtensionSheet({ me, ext, person, onClose, onChanged, onRemoved }: {
  me: Me; ext: Extension; person: User | undefined; onClose: () => void; onChanged: (e: Extension) => void; onRemoved: (id: string) => void;
}) {
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(ext.display_name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [addingDevice, setAddingDevice] = useState(false);
  const [resettingDevice, setResettingDevice] = useState<Device | null>(null);
  const [resetCreds, setResetCreds] = useState<DeviceCredentials | null>(null);
  const [removingDevice, setRemovingDevice] = useState<Device | null>(null);
  const [confirmRemoveExt, setConfirmRemoveExt] = useState(false);
  const confirm = useConfirmIdentity(me);

  const loadDevices = useCallback(async () => {
    const { data } = await api.GET("/api/v1/extensions/{id}/devices", { params: { path: { id: ext.id } } });
    if (data) setDevices(data.items);
  }, [ext.id]);
  useEffect(() => { void loadDevices(); }, [loadDevices]);

  const save = async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/extensions/{id}", {
      params: { path: { id: ext.id }, header: { "If-Match": ext.etag } }, body: { display_name: name.trim() },
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    onChanged(data);
    setEditing(false);
  };

  const doResetPassword = () => confirm.run(async () => {
    if (!resettingDevice) return { confirm: false };
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/devices/{id}/reset-password", { params: { path: { id: resettingDevice.id } } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setResetCreds(data);
    return { confirm: false };
  });

  const removeDevice = async () => {
    if (!removingDevice) return;
    setBusy(true);
    const { response } = await api.DELETE("/api/v1/devices/{id}", { params: { path: { id: removingDevice.id } } });
    setBusy(false);
    setRemovingDevice(null);
    if (response.ok) void loadDevices();
  };

  const removeExtension = async () => {
    setBusy(true);
    const { response, error: err } = await api.DELETE("/api/v1/extensions/{id}", { params: { path: { id: ext.id } } });
    setBusy(false);
    setConfirmRemoveExt(false);
    if (!response.ok) { setError(problemMessage(err)); return; }
    onRemoved(ext.id);
    onClose();
  };

  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Extension {ext.number}</SheetTitle>
          <SheetDescription>{ext.display_name}{person ? ` · ${person.name}` : ""}{!ext.enabled ? " · Turned off" : ""}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 overflow-y-auto px-4 pb-4">
          <section>
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-semibold">Details</h3>
              {!editing && <Button size="sm" variant="outline" onClick={() => setEditing(true)}>Edit</Button>}
            </div>
            {!editing ? (
              <dl className="mt-2 grid grid-cols-[6rem_1fr] gap-y-1 text-sm">
                <dt className="text-muted-foreground">Number</dt><dd className="font-mono">{ext.number}</dd>
                <dt className="text-muted-foreground">Name</dt><dd>{ext.display_name}</dd>
                <dt className="text-muted-foreground">Person</dt><dd>{person ? person.name : "—"}</dd>
              </dl>
            ) : (
              <div className="mt-2 flex flex-col gap-3">
                <Field label="Name" htmlFor="edit-ext-name"><Input id="edit-ext-name" value={name} onChange={(e) => setName(e.target.value)} /></Field>
                <div className="flex gap-2">
                  <Button size="sm" disabled={busy} onClick={() => void save()}>Save</Button>
                  <Button size="sm" variant="outline" onClick={() => { setEditing(false); setName(ext.display_name); }}>Cancel</Button>
                </div>
              </div>
            )}
          </section>

          <section>
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-semibold">Phones</h3>
              <Button size="sm" variant="outline" onClick={() => setAddingDevice(true)}>+ Add a desk phone or phone app</Button>
            </div>
            <div className="mt-2 flex flex-col gap-2">
              {devices === null && <p className="text-sm text-muted-foreground">Loading…</p>}
              {devices?.length === 0 && <p className="text-sm text-muted-foreground">No phones yet.</p>}
              {devices?.map((d) => (
                <div key={d.id} className="flex items-center gap-2.5 rounded-md border p-2.5 text-sm">
                  <Dot tone={d.online ? "good" : "neutral"} />
                  <span className="flex-1">{d.name} <span className="text-muted-foreground">· {kindLabel[d.kind] ?? d.kind}{d.revoked_at ? " · Revoked" : ""}</span></span>
                  {!d.revoked_at && (
                    <>
                      <Button size="sm" variant="outline" onClick={() => setResettingDevice(d)}>
                        <KeyRound aria-hidden="true" className="size-3.5" />
                        New password
                      </Button>
                      <Button size="sm" variant="outline" onClick={() => setRemovingDevice(d)}>Remove</Button>
                    </>
                  )}
                </div>
              ))}
            </div>
            <p className="mt-2 text-sm text-muted-foreground">Desk phones work on your home/office network. Using one from outside comes later.</p>
          </section>

          <FormError message={error} />

          <section className="rounded-md border border-destructive/30 p-3">
            <h3 className="text-sm font-semibold text-destructive">Danger zone</h3>
            <div className="mt-2">
              <Button size="sm" variant="outline" onClick={() => setConfirmRemoveExt(true)}>Remove extension</Button>
            </div>
          </section>
        </div>

        <Dialog open={addingDevice} onOpenChange={setAddingDevice}>
          <DialogContent>
            <DialogHeader><DialogTitle>Add a desk phone or phone app</DialogTitle></DialogHeader>
            <AddDevice me={me} extensionId={ext.id} personName={person?.name ?? ""}
              onDone={() => { setAddingDevice(false); void loadDevices(); }} />
          </DialogContent>
        </Dialog>

        <Dialog open={!!resettingDevice} onOpenChange={(o) => { if (!o) { setResettingDevice(null); setResetCreds(null); } }}>
          <DialogContent>
            <DialogHeader><DialogTitle>New password for "{resettingDevice?.name}"</DialogTitle></DialogHeader>
            {!resetCreds ? (
              <>
                <DialogDescription>The phone stops working until you enter the new password.</DialogDescription>
                <FormError message={error} />
                <DialogFooter>
                  <Button variant="outline" onClick={() => setResettingDevice(null)}>Cancel</Button>
                  <Button disabled={busy} aria-busy={busy} onClick={() => void doResetPassword()}>New password</Button>
                </DialogFooter>
              </>
            ) : (
              <DeviceCredentialsBox creds={resetCreds} onClose={() => { setResettingDevice(null); setResetCreds(null); }} />
            )}
          </DialogContent>
        </Dialog>

        <Dialog open={!!removingDevice} onOpenChange={(o) => { if (!o) setRemovingDevice(null); }}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Remove "{removingDevice?.name}"?</DialogTitle>
              <DialogDescription>It stops working at once. This can't be undone; add a new one instead.</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setRemovingDevice(null)}>Cancel</Button>
              <Button variant="destructive" disabled={busy} onClick={() => void removeDevice()}>Remove</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        <Dialog open={confirmRemoveExt} onOpenChange={setConfirmRemoveExt}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Remove extension {ext.number}?</DialogTitle>
              <DialogDescription>
                Its {devices?.length ?? 0} phone{devices?.length === 1 ? "" : "s"} stop working and number {ext.number} becomes free.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setConfirmRemoveExt(false)}>Cancel</Button>
              <Button variant="destructive" disabled={busy} onClick={() => void removeExtension()}>Remove</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

// --- List ---

export function ExtensionsScreen({ me }: { me: Me }) {
  const [extensions, setExtensions] = useState<Extension[] | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [devices, setDevices] = useState<Device[]>([]);
  const [query, setQuery] = useState("");
  const [chooserOpen, setChooserOpen] = useState(false);
  const [guidedOpen, setGuidedOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [always, setAlways] = useAlwaysQuickAdd("extensions");
  const [selected, setSelected] = useState<Extension | null>(null);
  const readOnly = isReadOnlyAdmin(me);

  const load = useCallback(async () => {
    const [ext, u, d] = await Promise.all([
      api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/users", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/devices", { params: { query: { limit: 200 } } }),
    ]);
    if (ext.data) setExtensions(ext.data.items);
    if (u.data) setUsers(u.data.items);
    if (d.data) setDevices(d.data.items);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const personFor = (extId: string) => users.find((u) => u.extension_id === extId);
  const devicesFor = (extId: string) => devices.filter((d) => d.extension_id === extId && !d.revoked_at);

  const q = query.trim().toLowerCase();
  const rows = useMemo(() => (extensions ?? []).filter((e) => {
    if (!q) return true;
    return e.number.includes(q) || e.display_name.toLowerCase().includes(q) || (personFor(e.id)?.name.toLowerCase().includes(q) ?? false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }), [extensions, users, q]);

  const columns: ColumnDef<Extension>[] = [
    { accessorKey: "number", header: "Ext" },
    { accessorKey: "display_name", header: "Name" },
    { id: "person", header: "Person", accessorFn: (e) => personFor(e.id)?.name ?? "" },
    {
      id: "phones", header: "Phones",
      cell: ({ row }) => {
        const list = devicesFor(row.original.id);
        if (list.length === 0) return <span className="text-muted-foreground">—</span>;
        return (
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {list.map((d) => (
              <span key={d.id} className="flex items-center gap-1.5">
                {kindLabel[d.kind] ?? d.kind} <Dot tone={d.online ? "good" : "neutral"} />
              </span>
            ))}
          </span>
        );
      },
    },
  ];

  if (extensions === null) return <div className="p-6" aria-busy="true" />;

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-semibold tracking-tight">Extensions</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {extensions.length} extension{extensions.length === 1 ? "" : "s"}
          </p>
        </div>
        {!readOnly && <Button onClick={() => (always ? setQuickOpen(true) : setChooserOpen(true))}>+ Add</Button>}
      </div>
      <p className="mt-1 text-sm text-muted-foreground">
        <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/setup")}>Change</button> the numbering plan in setup.
      </p>

      <div className="mt-4 w-full max-w-xs relative">
        <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
        <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-9" aria-label="Search extensions" />
      </div>

      <div className="mt-4">
        {extensions.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
            <p className="text-sm text-muted-foreground">An extension is a number that rings a phone — for a person, or a shared one like a reception desk.</p>
            {!readOnly && (
              <div className="flex gap-2">
                <Button variant="outline" onClick={() => setGuidedOpen(true)}>Guide me</Button>
                <Button onClick={() => setQuickOpen(true)}>Quick add</Button>
              </div>
            )}
          </div>
        ) : (
          <DataTable columns={columns} data={rows} onRowClick={setSelected}
            emptyState={<p className="p-6 text-center text-sm text-muted-foreground">No extensions match.</p>} />
        )}
      </div>

      <AddChooserDialog open={chooserOpen} onOpenChange={setChooserOpen} title="Add an extension"
        guideHint="Number, name, person and a phone, step by step." quickHint="Just a name. Change the rest later."
        always={always} onAlwaysChange={setAlways} onGuide={() => setGuidedOpen(true)} onQuick={() => setQuickOpen(true)} />
      <GuidedAddExtension me={me} open={guidedOpen} onOpenChange={setGuidedOpen} people={users}
        onDone={() => { setGuidedOpen(false); void load(); }} />
      <QuickAddExtension open={quickOpen} onOpenChange={setQuickOpen} always={always} onGuideInstead={() => setGuidedOpen(true)}
        onDone={() => void load()} />
      {selected && (
        <ExtensionSheet me={me} ext={selected} person={personFor(selected.id)} onClose={() => setSelected(null)}
          onChanged={(e) => { setSelected(e); setExtensions((list) => list?.map((x) => (x.id === e.id ? e : x)) ?? list); }}
          onRemoved={() => void load()} />
      )}
    </div>
  );
}
