// Ring groups (docs/ui/SCREENS_PHASE1F.md §8, ADR-068): list, add (guided
// or quick), and the detail sheet. Several phones ring for one call, all at
// once or one after another, and unanswered calls go somewhere else.
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, Search } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { AddChooserDialog, useAlwaysQuickAdd } from "@/components/AddChooser";
import { DataTable } from "@/components/DataTable";
import {
  DestinationPicker, NOT_AVAILABLE, destinationLabel, type Destination, type ExtensionChoice, type RingGroup,
} from "@/components/DestinationPicker";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { navigate } from "@/hooks/useRoute";
import { isReadOnlyAdmin } from "@/lib/roles";

type Extension = components["schemas"]["Extension"];
type Strategy = RingGroup["strategy"];

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
function Recommended() {
  return <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>;
}

/** "3 people, all at once" */
export function ringsSummary(g: Pick<RingGroup, "members" | "strategy">): string {
  const n = g.members.length;
  return `${n} ${n === 1 ? "person" : "people"}, ${g.strategy === "all" ? "all at once" : "in turn"}`;
}

function useNextGroupNumber(trigger: unknown) {
  const [number, setNumber] = useState("");
  useEffect(() => {
    void api.GET("/api/v1/numbering/next", { params: { query: { kind: "groups" } } }).then(({ data }) => { if (data) setNumber(data.number); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [trigger]);
  return number;
}

// --- The parts each add flow and the detail sheet share ---

/** Who's in it: ticks, in the order they're ticked. */
function PeopleChoice({ extensions, value, onChange }: {
  extensions: ExtensionChoice[]; value: string[]; onChange: (ids: string[]) => void;
}) {
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const list = extensions.filter((e) => !q || e.number.includes(q) || e.display_name.toLowerCase().includes(q));
  return (
    <div className="flex flex-col gap-2">
      <div className="relative">
        <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
        <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-9" aria-label="Search people" />
      </div>
      <div className="flex max-h-72 flex-col gap-1 overflow-y-auto rounded-md border p-1">
        {list.length === 0 && <p className="p-2 text-sm text-muted-foreground">No one matches.</p>}
        {list.map((e) => {
          const checked = value.includes(e.id);
          return (
            <label key={e.id} htmlFor={`member-${e.id}`} className="flex cursor-pointer items-center gap-3 rounded-sm px-2 py-1.5 text-sm hover:bg-accent">
              <Checkbox id={`member-${e.id}`} checked={checked}
                onCheckedChange={(c) => onChange(c ? [...value, e.id] : value.filter((x) => x !== e.id))} />
              <span className="font-mono text-muted-foreground">{e.number}</span>
              <span>{e.display_name}</span>
            </label>
          );
        })}
      </div>
      <p className="text-sm text-muted-foreground">{value.length} chosen</p>
    </div>
  );
}

/** How it rings; one after another shows the order with up/down. */
function HowItRings({ extensions, strategy, onStrategy, turnSeconds, onTurnSeconds, members, onMembers }: {
  extensions: ExtensionChoice[]; strategy: Strategy; onStrategy: (s: Strategy) => void;
  turnSeconds: string; onTurnSeconds: (s: string) => void; members: string[]; onMembers: (ids: string[]) => void;
}) {
  const name = (id: string) => extensions.find((e) => e.id === id)?.display_name ?? "Removed";
  const move = (i: number, by: number) => {
    const next = [...members];
    next.splice(i + by, 0, ...next.splice(i, 1));
    onMembers(next);
  };
  return (
    <RadioGroup value={strategy} onValueChange={(v) => onStrategy(v as Strategy)}>
      <label htmlFor="rings-all" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
        <RadioGroupItem id="rings-all" value="all" className="mt-0.5" />
        <span className="flex flex-col gap-0.5">
          <span className="text-sm font-medium">All at once<Recommended /></span>
          <span className="text-sm text-muted-foreground">Whoever's free answers first.</span>
        </span>
      </label>
      <label htmlFor="rings-turn" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
        <RadioGroupItem id="rings-turn" value="in_turn" className="mt-0.5" />
        <span className="flex flex-1 flex-col gap-2">
          <span className="text-sm font-medium">One after another</span>
          <span className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            In this order,
            <Input aria-label="Seconds each" inputMode="numeric" className="h-8 w-16" value={turnSeconds}
              onChange={(e) => onTurnSeconds(e.target.value.replace(/\D/g, ""))} onFocus={() => onStrategy("in_turn")} />
            seconds each.
          </span>
          {strategy === "in_turn" && (
            <ol className="flex flex-col gap-1">
              {members.map((id, i) => (
                <li key={id} className="flex items-center gap-2 rounded-sm border px-2 py-1 text-sm">
                  <span className="w-5 text-muted-foreground">{i + 1}</span>
                  <span className="flex-1">{name(id)}</span>
                  <Button type="button" size="icon" variant="ghost" className="size-7" disabled={i === 0}
                    aria-label={`Move ${name(id)} up`} onClick={() => move(i, -1)}><ArrowUp className="size-4" /></Button>
                  <Button type="button" size="icon" variant="ghost" className="size-7" disabled={i === members.length - 1}
                    aria-label={`Move ${name(id)} down`} onClick={() => move(i, 1)}><ArrowDown className="size-4" /></Button>
                </li>
              ))}
            </ol>
          )}
        </span>
      </label>
    </RadioGroup>
  );
}

/** If nobody answers: after how long (all at once), and where. */
function IfNobodyAnswers({ strategy, ringSeconds, onRingSeconds, value, onChange, extensions, groups, self }: {
  strategy: Strategy; ringSeconds: string; onRingSeconds: (s: string) => void;
  value: Destination; onChange: (d: Destination) => void; extensions: ExtensionChoice[]; groups: RingGroup[]; self?: string;
}) {
  return (
    <div className="flex flex-col gap-3">
      {strategy === "all" ? (
        <p className="flex flex-wrap items-center gap-2 text-sm">
          If nobody answers after
          <Input aria-label="Seconds before giving up" inputMode="numeric" className="h-8 w-16" value={ringSeconds}
            onChange={(e) => onRingSeconds(e.target.value.replace(/\D/g, ""))} />
          seconds, the call goes to:
        </p>
      ) : (
        <p className="text-sm">If nobody answers once everyone has rung, the call goes to:</p>
      )}
      <DestinationPicker id="no-answer" value={value} onChange={onChange} extensions={extensions} groups={groups} self={self} />
      <p className="text-sm text-muted-foreground">A voicemail box for the group comes with voicemail, soon.</p>
    </div>
  );
}

type NumberChoice = "next" | "other" | "none";

function NumberStep({ next, choice, onChoice, number, onNumber }: {
  next: string; choice: NumberChoice; onChoice: (c: NumberChoice) => void; number: string; onNumber: (n: string) => void;
}) {
  return (
    <RadioGroup value={choice} onValueChange={(v) => onChoice(v as NumberChoice)}>
      {next && (
        <label htmlFor="gnum-next" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
          <RadioGroupItem id="gnum-next" value="next" className="mt-0.5" />
          <span className="flex flex-col gap-0.5">
            <span className="text-sm font-medium">Give it {next}<Recommended /></span>
            <span className="text-sm text-muted-foreground">People can dial it or transfer a call to it.</span>
          </span>
        </label>
      )}
      <label htmlFor="gnum-other" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
        <RadioGroupItem id="gnum-other" value="other" className="mt-0.5" />
        <span className="flex flex-1 flex-col gap-1.5">
          <span className="text-sm font-medium">Pick another number</span>
          {choice === "other" && <Input aria-label="Number" className="font-mono" inputMode="numeric" value={number} onChange={(e) => onNumber(e.target.value)} />}
        </span>
      </label>
      <label htmlFor="gnum-none" className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
        <RadioGroupItem id="gnum-none" value="none" className="mt-0.5" />
        <span className="flex flex-col gap-0.5">
          <span className="text-sm font-medium">No number</span>
          <span className="text-sm text-muted-foreground">Only phone numbers and other groups can send calls here.</span>
        </span>
      </label>
    </RadioGroup>
  );
}

function seconds(s: string, fallback: number): number {
  const n = Number.parseInt(s, 10);
  return Number.isFinite(n) ? n : fallback;
}

// --- Add a ring group ---

function GuidedAddRingGroup({ open, onOpenChange, extensions, groups, onDone }: {
  open: boolean; onOpenChange: (o: boolean) => void; extensions: ExtensionChoice[]; groups: RingGroup[]; onDone: () => void;
}) {
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [members, setMembers] = useState<string[]>([]);
  const [strategy, setStrategy] = useState<Strategy>("all");
  const [turnSeconds, setTurnSeconds] = useState("15");
  const [ringSeconds, setRingSeconds] = useState("25");
  const [noAnswer, setNoAnswer] = useState<Destination>(NOT_AVAILABLE);
  const next = useNextGroupNumber(open);
  const [numberChoice, setNumberChoice] = useState<NumberChoice>("next");
  const [number, setNumber] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<RingGroup | null>(null);

  useEffect(() => {
    if (!open) return;
    setStep(1); setName(""); setMembers([]); setStrategy("all"); setTurnSeconds("15"); setRingSeconds("25");
    setNoAnswer(NOT_AVAILABLE); setNumberChoice("next"); setNumber(""); setError(""); setCreated(null);
  }, [open]);
  useEffect(() => { if (!next && numberChoice === "next") setNumberChoice("none"); }, [next, numberChoice]);

  const create = async () => {
    setBusy(true);
    setError("");
    const chosen = numberChoice === "next" ? next : numberChoice === "other" ? number.trim() : undefined;
    const { data, error: err } = await api.POST("/api/v1/ring-groups", {
      body: {
        name: name.trim(), member_ids: members, strategy, number: chosen || undefined,
        ring_seconds: seconds(ringSeconds, 25), turn_seconds: seconds(turnSeconds, 15), if_no_answer: noAnswer,
      },
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setCreated(data);
    setStep(6);
  };

  const discard = () => {
    if (step > 1 && step < 6 && !window.confirm("Discard this?")) return;
    onOpenChange(false);
  };
  const canNext = (step === 1 && !!name.trim()) || (step === 2 && members.length > 0) || step === 3 || step === 4;

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) discard(); else onOpenChange(o); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add a ring group</SheetTitle>
          <SheetDescription>1 Name · 2 People · 3 How it rings · 4 If nobody answers · 5 Number · 6 Done</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4">
          {step === 1 && (
            <Field label="Name" htmlFor="add-group-name">
              <Input id="add-group-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Sales, Support, Reception…" autoFocus />
            </Field>
          )}
          {step === 2 && (
            <div className="flex flex-col gap-2">
              <p className="text-sm font-medium">Who's in it?</p>
              <PeopleChoice extensions={extensions} value={members} onChange={setMembers} />
            </div>
          )}
          {step === 3 && (
            <HowItRings extensions={extensions} strategy={strategy} onStrategy={setStrategy} turnSeconds={turnSeconds}
              onTurnSeconds={setTurnSeconds} members={members} onMembers={setMembers} />
          )}
          {step === 4 && (
            <IfNobodyAnswers strategy={strategy} ringSeconds={ringSeconds} onRingSeconds={setRingSeconds}
              value={noAnswer} onChange={setNoAnswer} extensions={extensions} groups={groups} />
          )}
          {step === 5 && <NumberStep next={next} choice={numberChoice} onChoice={setNumberChoice} number={number} onNumber={setNumber} />}
          {step === 6 && created && (
            <div className="flex flex-col gap-3">
              <p className="text-sm font-medium">{created.name} is ready{created.number ? `: dial ${created.number}` : ""}.</p>
              <p className="rounded-md bg-card p-3 text-sm">{created.words}</p>
              <p className="text-sm text-muted-foreground">
                Send a phone number here?{" "}
                <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/incoming")}>Go to Numbers</button>
              </p>
            </div>
          )}
          <FormError message={error} />
        </div>
        <div className="mt-auto flex items-center justify-between gap-2 border-t p-4">
          <Button variant="outline" disabled={step === 1 || busy || !!created} onClick={() => setStep(step - 1)}>Back</Button>
          {step < 5 && <Button disabled={!canNext} onClick={() => setStep(step + 1)}>Next</Button>}
          {step === 5 && (
            <Button disabled={busy || (numberChoice === "other" && !number.trim())} aria-busy={busy} onClick={() => void create()}>Create</Button>
          )}
          {step === 6 && <Button onClick={onDone}>Done</Button>}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function QuickAddRingGroup({ open, onOpenChange, onGuideInstead, always, extensions, onDone }: {
  open: boolean; onOpenChange: (o: boolean) => void; onGuideInstead: () => void; always: boolean;
  extensions: ExtensionChoice[]; onDone: (g: RingGroup) => void;
}) {
  const [name, setName] = useState("");
  const [members, setMembers] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const next = useNextGroupNumber(open);

  useEffect(() => { if (open) { setName(""); setMembers([]); setError(""); } }, [open]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/ring-groups", {
      body: { name: name.trim(), member_ids: members, number: next || undefined },
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    onOpenChange(false);
    onDone(data);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>Add a ring group</DialogTitle></DialogHeader>
        {always && (
          <button type="button" className="-mt-2 self-start text-sm text-link underline-offset-4 hover:underline" onClick={() => { onOpenChange(false); onGuideInstead(); }}>
            Guide me instead
          </button>
        )}
        <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
          <Field label="Name" htmlFor="quick-group-name">
            <Input id="quick-group-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="Sales, Support, Reception…" />
          </Field>
          <PeopleChoice extensions={extensions} value={members} onChange={setMembers} />
          <p className="text-sm text-muted-foreground">
            {next ? `Number ${next}, ` : "No number, "}all at once, 25 seconds, then callers hear "not available". You can change it any time.
          </p>
          <FormError message={error} />
          <DialogFooter><Button type="submit" disabled={busy || !name.trim() || members.length === 0} aria-busy={busy}>Add</Button></DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// --- Detail sheet ---

type Editing = "" | "name" | "people" | "how" | "noanswer" | "number";

function RingGroupSheet({ me, group, extensions, groups, onClose, onChanged, onRemoved }: {
  me: Me; group: RingGroup; extensions: ExtensionChoice[]; groups: RingGroup[];
  onClose: () => void; onChanged: (g: RingGroup) => void; onRemoved: () => void;
}) {
  const readOnly = isReadOnlyAdmin(me);
  const [editing, setEditing] = useState<Editing>("");
  const [name, setName] = useState(group.name);
  const [members, setMembers] = useState(group.members.map((m) => m.extension_id));
  const [strategy, setStrategy] = useState<Strategy>(group.strategy);
  const [turnSeconds, setTurnSeconds] = useState(String(group.turn_seconds));
  const [ringSeconds, setRingSeconds] = useState(String(group.ring_seconds));
  const [noAnswer, setNoAnswer] = useState<Destination>(group.if_no_answer);
  const [number, setNumber] = useState(group.number ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState(false);
  const [moveTo, setMoveTo] = useState<Destination>(NOT_AVAILABLE);

  const reset = useCallback((g: RingGroup) => {
    setName(g.name); setMembers(g.members.map((m) => m.extension_id)); setStrategy(g.strategy);
    setTurnSeconds(String(g.turn_seconds)); setRingSeconds(String(g.ring_seconds)); setNoAnswer(g.if_no_answer); setNumber(g.number ?? "");
  }, []);
  useEffect(() => { reset(group); }, [group, reset]);

  const save = async (body: components["schemas"]["RingGroupPatch"]) => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/ring-groups/{id}", {
      params: { path: { id: group.id }, header: { "If-Match": group.etag } }, body,
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    onChanged(data);
    setEditing("");
  };

  const remove = async () => {
    setBusy(true);
    setError("");
    // Whatever still sends calls here goes where the admin chose first.
    for (const u of group.used_by) {
      const other = groups.find((g) => g.id === u.id);
      if (!other) continue;
      const { error: err } = await api.PATCH("/api/v1/ring-groups/{id}", {
        params: { path: { id: other.id }, header: { "If-Match": other.etag } }, body: { if_no_answer: moveTo },
      });
      if (err) { setBusy(false); setError(problemMessage(err)); return; }
    }
    const { response, error: err } = await api.DELETE("/api/v1/ring-groups/{id}", { params: { path: { id: group.id } } });
    setBusy(false);
    if (!response.ok) { setError(problemMessage(err)); return; }
    setRemoving(false);
    onRemoved();
    onClose();
  };

  const nobodyCanRing = group.members.length > 0 && !group.members.some((m) => m.can_ring);
  const row = (title: string, key: Editing, shown: ReactNode, editor: ReactNode, onSave: () => void) => (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{title}</h3>
        {!readOnly && editing !== key && <Button size="sm" variant="outline" onClick={() => { reset(group); setError(""); setEditing(key); }}>Edit</Button>}
      </div>
      {editing === key ? (
        <div className="flex flex-col gap-3">
          {editor}
          <div className="flex gap-2">
            <Button size="sm" disabled={busy} aria-busy={busy} onClick={onSave}>Save</Button>
            <Button size="sm" variant="outline" onClick={() => { reset(group); setEditing(""); setError(""); }}>Cancel</Button>
          </div>
        </div>
      ) : shown}
    </section>
  );
  const otherGroups = groups.filter((g) => g.id !== group.id);

  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{group.name}</SheetTitle>
          <SheetDescription>
            {group.number ? `Number ${group.number}` : "No number"} · {group.members.length} {group.members.length === 1 ? "person" : "people"} · {group.strategy === "all" ? "all at once" : "one after another"}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 overflow-y-auto px-4 pb-4">
          <p className="rounded-md bg-card p-3 text-sm">{group.words}</p>
          {nobodyCanRing && (
            <p className="flex items-center gap-2 text-sm text-muted-foreground"><Dot tone="neutral" /> Nobody can ring (all offline): calls go straight to "if nobody answers".</p>
          )}

          {row("Name", "name", <p className="text-sm">{group.name}</p>,
            <Input aria-label="Name" value={name} onChange={(e) => setName(e.target.value)} />,
            () => void save({ name: name.trim() }))}

          {row("People", "people",
            <ul className="flex flex-col gap-1 text-sm">
              {group.members.map((m) => (
                <li key={m.extension_id} className="flex items-center gap-2">
                  <Dot tone={m.can_ring ? "good" : "neutral"} />
                  <span className="font-mono text-muted-foreground">{m.number}</span> {m.display_name}
                  {!m.can_ring && <span className="text-muted-foreground">· offline</span>}
                </li>
              ))}
            </ul>,
            <PeopleChoice extensions={extensions} value={members} onChange={setMembers} />,
            () => void save({ member_ids: members }))}

          {row("How it rings", "how",
            <p className="text-sm">{group.strategy === "all" ? "All at once" : `One after another, ${group.turn_seconds} seconds each`}</p>,
            <HowItRings extensions={extensions} strategy={strategy} onStrategy={setStrategy} turnSeconds={turnSeconds}
              onTurnSeconds={setTurnSeconds} members={members} onMembers={setMembers} />,
            () => void save({ strategy, turn_seconds: seconds(turnSeconds, 15), member_ids: members }))}

          {row("If nobody answers", "noanswer",
            <p className="text-sm">
              {group.strategy === "all" ? `After ${group.ring_seconds} seconds: ` : "After everyone has rung: "}
              {destinationLabel(group.if_no_answer, extensions, groups)}
            </p>,
            <IfNobodyAnswers strategy={group.strategy} ringSeconds={ringSeconds} onRingSeconds={setRingSeconds}
              value={noAnswer} onChange={setNoAnswer} extensions={extensions} groups={groups} self={group.id} />,
            () => void save({ if_no_answer: noAnswer, ring_seconds: seconds(ringSeconds, 25) }))}

          {row("Number", "number", <p className="font-mono text-sm">{group.number ?? "None"}</p>,
            <div className="flex flex-col gap-1.5">
              <Input aria-label="Number" className="font-mono" inputMode="numeric" value={number} onChange={(e) => setNumber(e.target.value)} />
              <p className="text-sm text-muted-foreground">Leave it empty for no number.</p>
            </div>,
            () => void save({ number: number.trim() }))}

          <section className="flex flex-col gap-1">
            <h3 className="text-sm font-semibold">Used by</h3>
            {group.used_by.length === 0
              ? <p className="text-sm text-muted-foreground">{group.number ? `Nothing sends calls here yet; people can dial ${group.number}.` : "Nothing sends calls here yet."}</p>
              : group.used_by.map((u) => <p key={u.id} className="text-sm">{u.name} (if nobody answers)</p>)}
          </section>

          <FormError message={error} />

          {!readOnly && (
            <section className="rounded-md border border-destructive/30 p-3">
              <h3 className="text-sm font-semibold text-destructive">Danger zone</h3>
              <div className="mt-2">
                <Button size="sm" variant="outline" onClick={() => { setMoveTo(NOT_AVAILABLE); setRemoving(true); }}>Remove {group.name}</Button>
              </div>
            </section>
          )}
        </div>

        <Dialog open={removing} onOpenChange={setRemoving}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Remove {group.name}?</DialogTitle>
              <DialogDescription>
                {group.used_by.length === 0
                  ? `Its number${group.number ? ` ${group.number}` : ""} stops working at once.`
                  : `${group.used_by.map((u) => u.name).join(" and ")}'s unanswered calls go here. Choose where they go instead:`}
              </DialogDescription>
            </DialogHeader>
            {group.used_by.length > 0 && (
              <DestinationPicker value={moveTo} onChange={setMoveTo} extensions={extensions} groups={otherGroups}
                self={group.used_by.length === 1 ? group.used_by[0]?.id : undefined} />
            )}
            <FormError message={error} />
            <DialogFooter>
              <Button variant="outline" onClick={() => setRemoving(false)}>Cancel</Button>
              <Button variant="destructive" disabled={busy} aria-busy={busy} onClick={() => void remove()}>Remove</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </SheetContent>
    </Sheet>
  );
}

// --- List ---

export function RingGroupsScreen({ me }: { me: Me }) {
  const [groups, setGroups] = useState<RingGroup[] | null>(null);
  const [extensions, setExtensions] = useState<ExtensionChoice[]>([]);
  const [query, setQuery] = useState("");
  const [chooserOpen, setChooserOpen] = useState(false);
  const [guidedOpen, setGuidedOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [always, setAlways] = useAlwaysQuickAdd("ring-groups");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const readOnly = isReadOnlyAdmin(me);

  const load = useCallback(async () => {
    const [g, ext] = await Promise.all([
      api.GET("/api/v1/ring-groups"),
      api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }),
    ]);
    if (g.data) setGroups(g.data.items);
    if (ext.data) setExtensions(ext.data.items.filter((e: Extension) => e.enabled).map((e: Extension) => ({ id: e.id, number: e.number, display_name: e.display_name })));
  }, []);
  useEffect(() => { void load(); }, [load]);

  const q = query.trim().toLowerCase();
  const rows = useMemo(() => (groups ?? []).filter((g) => !q || g.name.toLowerCase().includes(q) || (g.number ?? "").includes(q)
    || g.members.some((m) => m.display_name.toLowerCase().includes(q))), [groups, q]);
  const selected = groups?.find((g) => g.id === selectedId);

  const columns: ColumnDef<RingGroup>[] = [
    { accessorKey: "name", header: "Name" },
    { id: "number", header: "Number", accessorFn: (g) => g.number ?? "—", meta: { wide: true } },
    { id: "rings", header: "Rings", accessorFn: (g) => ringsSummary(g) },
    { id: "noanswer", header: "If nobody answers", accessorFn: (g) => destinationLabel(g.if_no_answer, extensions, groups ?? []), meta: { wide: true } },
  ];

  if (groups === null) return <div className="p-6" aria-busy="true" />;

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-semibold tracking-tight">Ring groups</h1>
          <p className="mt-1 text-sm text-muted-foreground">Several phones ring for one call.</p>
        </div>
        {!readOnly && <Button onClick={() => (always ? setQuickOpen(true) : setChooserOpen(true))}>+ Add</Button>}
      </div>

      {groups.length > 0 && (
        <div className="relative mt-4 w-full max-w-xs">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-9" aria-label="Search ring groups" />
        </div>
      )}

      <div className="mt-4">
        {groups.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
            <p className="max-w-md text-sm text-muted-foreground">
              A ring group rings several people for one call: all at once, or one after another. Use it for Sales, Support or Reception.
            </p>
            {!readOnly && (
              <div className="flex gap-2">
                <Button variant="outline" onClick={() => setGuidedOpen(true)}>Guide me</Button>
                <Button onClick={() => setQuickOpen(true)}>Quick add</Button>
              </div>
            )}
          </div>
        ) : (
          <DataTable columns={columns} data={rows} onRowClick={(g) => setSelectedId(g.id)}
            emptyState={<p className="p-6 text-center text-sm text-muted-foreground">No ring groups match.</p>} />
        )}
      </div>

      <AddChooserDialog open={chooserOpen} onOpenChange={setChooserOpen} title="Add a ring group"
        guideHint="Name, people, how it rings and what happens if nobody answers, step by step."
        quickHint="A name and the people. Change the rest later."
        always={always} onAlwaysChange={setAlways} onGuide={() => setGuidedOpen(true)} onQuick={() => setQuickOpen(true)} />
      <GuidedAddRingGroup open={guidedOpen} onOpenChange={setGuidedOpen} extensions={extensions} groups={groups}
        onDone={() => { setGuidedOpen(false); void load(); }} />
      <QuickAddRingGroup open={quickOpen} onOpenChange={setQuickOpen} always={always} onGuideInstead={() => setGuidedOpen(true)}
        extensions={extensions} onDone={() => void load()} />
      {selected && (
        <RingGroupSheet me={me} group={selected} extensions={extensions} groups={groups} onClose={() => setSelectedId(null)}
          onChanged={() => void load()} onRemoved={() => void load()} />
      )}
    </div>
  );
}
