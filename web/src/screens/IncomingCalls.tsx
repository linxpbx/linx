// Incoming calls (docs/ui/SCREENS_PHASE1F.md §10, ADR-068): every number,
// and every line's calls for none of its numbers, with the sentence saying
// what a caller gets; Change opens the "When someone calls" wizard, Try the
// simulator.
import { useCallback, useEffect, useRef, useState } from "react";
import { FlaskConical, TriangleAlert } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { CLOSED, DestinationPicker, NOT_AVAILABLE, type Destination, type ExtensionChoice, type RingGroup } from "@/components/DestinationPicker";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { navigate } from "@/hooks/useRoute";
import { hasScope, isReadOnlyAdmin } from "@/lib/roles";
import { cn } from "@/lib/utils";
import { TimeSelect } from "@/components/TimeSelect";
import { zonedISO, zonedToday, zoneLabel, type Schedule } from "./OfficeHours";

type Incoming = components["schemas"]["Incoming"];
type IncomingSet = components["schemas"]["IncomingSet"];
type Extension = components["schemas"]["Extension"];

const STEPS = ["Office hours", "No answer", "After hours", "Check"] as const;

function Recommended() {
  return <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>;
}

function title(i: Incoming): string {
  return i.kind === "number" ? (i.number ?? "") : `Other calls on ${i.line_name}`;
}

/** What the wizard holds while it's open. */
type Draft = {
  rings?: Destination; schedule?: string; seconds: number; noAnswer: Destination;
  closedKind: "closed" | "other"; closed?: Destination; holidaysDiffer: boolean; holidays?: Destination;
};

function draftOf(i: Incoming, schedules: Schedule[], home: boolean): Draft {
  const officeHours = schedules.find((s) => s.name === "Office hours") ?? schedules[0];
  const fresh = i.just_ring;
  const closedIsMessage = i.after_hours.kind === "message" && i.after_hours.message === "closed";
  return {
    rings: i.rings,
    schedule: fresh ? (home ? undefined : officeHours?.id) : i.schedule_id,
    seconds: fresh ? 25 : i.if_no_answer_seconds,
    noAnswer: fresh ? NOT_AVAILABLE : i.if_no_answer,
    closedKind: closedIsMessage ? "closed" : "other",
    closed: closedIsMessage ? undefined : i.after_hours,
    holidaysDiffer: !!i.holidays,
    holidays: i.holidays,
  };
}

function bodyOf(d: Draft): IncomingSet {
  const body: IncomingSet = { if_no_answer_seconds: d.seconds, if_no_answer: d.noAnswer };
  if (d.rings) body.rings = d.rings;
  if (d.schedule) {
    body.schedule_id = d.schedule;
    body.after_hours = d.closedKind === "closed" || !d.closed ? CLOSED : d.closed;
    if (d.holidaysDiffer && d.holidays) body.holidays = d.holidays;
  }
  return body;
}

function MiniTry({ number, zone }: { number: string; zone: string }) {
  const [day, setDay] = useState(() => zonedToday(zone));
  const [time, setTime] = useState("20:00");
  const [result, setResult] = useState<components["schemas"]["RouteTest"] | null>(null);
  const run = async () => {
    const { data } = await api.POST("/api/v1/route-test", {
      body: { direction: "inbound", number, at: zonedISO(day, time, zone) },
    });
    setResult(data ?? null);
  };
  return (
    <div className="flex flex-col gap-3 rounded-lg border p-3">
      <p className="text-sm font-medium">Try it: what happens at <span className="font-normal text-muted-foreground">({zoneLabel(zone)} time)</span></p>
      <div className="flex flex-wrap items-end gap-2">
        <Input type="date" value={day} onChange={(e) => setDay(e.target.value)} className="w-40" aria-label="Day" />
        <TimeSelect value={time} onChange={setTime} label="Time" />
        <Button variant="outline" onClick={() => void run()} disabled={!day || !time}>Try</Button>
      </div>
      {result && (
        <div className="flex flex-col gap-1 text-sm" aria-live="polite">
          {result.when && <p className="font-medium">{result.when}</p>}
          <ol className="list-decimal ps-5">{(result.steps ?? []).map((s, i) => <li key={i}>{s.words}</li>)}</ol>
        </div>
      )}
    </div>
  );
}

function IncomingWizard({ item, schedules, zone, groups, extensions, home, onClose, onSaved }: {
  item: Incoming; schedules: Schedule[]; zone: string; groups: RingGroup[]; extensions: ExtensionChoice[]; home: boolean;
  onClose: () => void; onSaved: (i: Incoming) => void;
}) {
  const [quick, setQuick] = useState(false);
  const [step, setStep] = useState(0);
  const [draft, setDraft] = useState<Draft>(() => draftOf(item, schedules, home));
  const [quickRings, setQuickRings] = useState<Destination | undefined>(item.rings);
  const [words, setWords] = useState(item.words);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<Incoming | null>(null);
  const etag = useRef(item.etag);
  const set = (p: Partial<Draft>) => { setDraft((d) => ({ ...d, ...p })); setSaved(null); };
  const name = title(item);
  const group = draft.rings?.kind === "ring_group" ? groups.find((g) => g.id === draft.rings?.ring_group_id) : undefined;
  const schedule = schedules.find((s) => s.id === draft.schedule);

  // The sentence, rebuilt by the server as each choice changes.
  useEffect(() => {
    if (quick) return;
    const t = window.setTimeout(() => {
      void api.PUT("/api/v1/incoming/{id}", { params: { path: { id: item.id } }, body: { ...bodyOf(draft), preview: true } })
        .then(({ data, error: err }) => { if (data) { setWords(data.words); setError(""); } else setError(problemMessage(err)); });
    }, 250);
    return () => window.clearTimeout(t);
  }, [draft, item.id, quick]);

  const save = async (body: IncomingSet) => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PUT("/api/v1/incoming/{id}", {
      params: { path: { id: item.id }, header: { "If-Match": etag.current } }, body,
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    etag.current = data.etag;
    setSaved(data);
    setWords(data.words);
    onSaved(data);
  };

  return (
    <Sheet open onOpenChange={(v) => { if (!v) onClose(); }}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>When someone calls {name}</SheetTitle>
          <SheetDescription>
            {quick ? "Just one person, all the time." : "Who rings, what happens if nobody answers, and outside office hours."}{" "}
            <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => { setQuick(!quick); setSaved(null); setError(""); }}>
              {quick ? "Use the full wizard" : "Just ring one person"}
            </button>
          </SheetDescription>
        </SheetHeader>

        {quick ? (
          <div className="flex flex-col gap-4 px-4 pb-6">
            <div className="flex flex-col gap-2">
              <Label htmlFor="quick-rings">{name} rings</Label>
              <DestinationPicker id="quick-rings" value={quickRings} onChange={setQuickRings} extensions={extensions} groups={groups}
                ringOnly placeholder="Nobody" />
            </div>
            {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
            {saved && <p role="status" className="text-sm">{saved.words}</p>}
            <div className="flex gap-2">
              <Button onClick={() => void save({ just_ring: true, ...(quickRings ? { rings: quickRings } : {}) })} disabled={busy} aria-busy={busy}>Save</Button>
              <Button variant="outline" onClick={onClose}>{saved ? "Done" : "Cancel"}</Button>
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-5 px-4 pb-6">
            <ol className="flex flex-wrap gap-x-3 gap-y-1 text-sm" aria-label="Steps">
              {STEPS.map((s, i) => (
                <li key={s}>
                  <button type="button" onClick={() => setStep(i)} aria-current={i === step ? "step" : undefined}
                    className={cn("flex items-center gap-1.5", i === step ? "font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}>
                    <span aria-hidden="true" className={cn("inline-block size-2 rounded-full", i <= step ? "bg-primary" : "bg-muted-foreground/40")} />
                    {s}
                  </button>
                </li>
              ))}
            </ol>

            {step === 0 && (
              <div className="flex flex-col gap-4">
                <div className="flex flex-col gap-2">
                  <Label htmlFor="wiz-rings">{draft.schedule ? "During office hours, ring" : "Ring"}</Label>
                  <DestinationPicker id="wiz-rings" value={draft.rings} onChange={(d) => set({ rings: d })} extensions={extensions} groups={groups}
                    ringOnly placeholder="Choose a person or a ring group" />
                </div>
                {schedules.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    It rings the same all the time.{" "}
                    <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/office-hours")}>Add office hours</button>
                  </p>
                ) : (
                  <RadioGroup value={draft.schedule ? "hours" : "always"} aria-label="When"
                    onValueChange={(v) => set({ schedule: v === "hours" ? (schedule ?? schedules[0])?.id : undefined })} className="flex flex-col gap-3">
                    <label htmlFor="wiz-hours" className="flex cursor-pointer items-start gap-2 text-sm">
                      <RadioGroupItem id="wiz-hours" value="hours" className="mt-0.5" />
                      <span className="flex flex-col gap-2">
                        <span>Follow office hours{home ? "" : <Recommended />}</span>
                        {draft.schedule && (
                          <span className="flex flex-wrap items-center gap-2 text-muted-foreground">
                            {schedules.length > 1 ? (
                              <Select value={draft.schedule} onValueChange={(v) => set({ schedule: v })}>
                                <SelectTrigger className="w-48" aria-label="Which hours"><SelectValue /></SelectTrigger>
                                <SelectContent>{schedules.map((s) => <SelectItem key={s.id} value={s.id}>{s.name}</SelectItem>)}</SelectContent>
                              </Select>
                            ) : <span>{schedule?.name}:</span>}
                            <span>{schedule?.words}</span>
                            <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/office-hours")}>Change hours</button>
                          </span>
                        )}
                      </span>
                    </label>
                    <label htmlFor="wiz-always" className="flex cursor-pointer items-start gap-2 text-sm">
                      <RadioGroupItem id="wiz-always" value="always" className="mt-0.5" />
                      <span>The same all the time (no office hours){home ? <Recommended /> : null}</span>
                    </label>
                  </RadioGroup>
                )}
              </div>
            )}

            {step === 1 && (group ? (
              <div className="flex flex-col gap-2 rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
                <p>{group.name} decides this itself ({group.if_no_answer.label ?? "its own choice"}).</p>
                <button type="button" className="self-start text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/ring-groups")}>
                  Change it in Ring groups
                </button>
              </div>
            ) : (
              <div className="flex flex-col gap-3">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <Label htmlFor="wiz-seconds">If nobody answers after</Label>
                  <Input id="wiz-seconds" type="number" inputMode="numeric" min={5} max={300} value={draft.seconds} className="w-20"
                    onChange={(e) => set({ seconds: Number(e.target.value) || 0 })} />
                  <span>seconds</span>
                </div>
                <DestinationPicker id="wiz-noanswer" value={draft.noAnswer} onChange={(d) => set({ noAnswer: d })} extensions={extensions} groups={groups} />
                <p className="text-xs text-muted-foreground">Voicemail comes in a later update; until then, "Nobody" plays "not available".</p>
              </div>
            ))}

            {step === 2 && (!draft.schedule ? (
              <p className="rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
                Calls ring the same all the time, so there's nothing to choose here. To follow office hours, go back to step 1.
              </p>
            ) : (
              <div className="flex flex-col gap-4">
                <p className="text-sm font-medium">Outside office hours and on holidays</p>
                <RadioGroup value={draft.closedKind} onValueChange={(v) => set({ closedKind: v as Draft["closedKind"] })} className="flex flex-col gap-3"
                  aria-label="Outside office hours">
                  <label htmlFor="wiz-closed" className="flex cursor-pointer items-start gap-2 text-sm">
                    <RadioGroupItem id="wiz-closed" value="closed" className="mt-0.5" />
                    <span>Play "We're closed", then hang up<Recommended />
                      <span className="block text-muted-foreground">Voicemail after it comes in a later update.</span></span>
                  </label>
                  <label htmlFor="wiz-other" className="flex cursor-pointer items-start gap-2 text-sm">
                    <RadioGroupItem id="wiz-other" value="other" className="mt-0.5" />
                    <span>Somewhere else</span>
                  </label>
                </RadioGroup>
                {draft.closedKind === "other" && (
                  <DestinationPicker id="wiz-closed-to" value={draft.closed} onChange={(d) => set({ closed: d })} extensions={extensions} groups={groups}
                    placeholder="Choose where" />
                )}
                <label htmlFor="wiz-holidays" className="flex items-start gap-2 text-sm">
                  <Checkbox id="wiz-holidays" checked={draft.holidaysDiffer} className="mt-0.5"
                    onCheckedChange={(v) => set({ holidaysDiffer: v === true, holidays: v === true ? draft.holidays : undefined })} />
                  <span>Holidays go somewhere else</span>
                </label>
                {draft.holidaysDiffer && (
                  <DestinationPicker id="wiz-holidays-to" value={draft.holidays} onChange={(d) => set({ holidays: d })} extensions={extensions} groups={groups}
                    placeholder="Choose where" />
                )}
              </div>
            ))}

            {step === 3 && (
              <div className="flex flex-col gap-4">
                <p className="text-sm font-medium">Check</p>
                {saved ? (item.kind === "number" && <MiniTry number={item.number ?? ""} zone={zone} />) : (
                  <p className="text-sm text-muted-foreground">Save{item.kind === "number" ? ", then try a day and time to see what a caller gets" : " when it reads right"}.</p>
                )}
              </div>
            )}

            <div className="border-t pt-3">
              <p className="text-sm" aria-live="polite">{words}</p>
            </div>
            {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
            {saved && <p role="status" className="text-sm text-status-available">Saved.</p>}
            <div className="flex flex-wrap justify-end gap-2">
              {step > 0 && <Button variant="outline" onClick={() => setStep(step - 1)}>Back</Button>}
              {step < 3 ? <Button onClick={() => setStep(step + 1)}>Next</Button> : saved ? (
                <Button onClick={onClose}>Done</Button>
              ) : (
                <Button onClick={() => void save(bodyOf(draft))} disabled={busy || !draft.rings} aria-busy={busy}>Save</Button>
              )}
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  );
}

function IncomingCard({ item, readOnly, onChange, onTry }: { item: Incoming; readOnly: boolean; onChange: () => void; onTry?: () => void }) {
  return (
    <li className="flex flex-col gap-1.5 p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className={cn("grow text-sm font-medium", item.kind === "number" && "font-mono")}>
          {title(item)}
          <span className="font-sans font-normal text-muted-foreground">{item.kind === "number" ? ` · ${item.line_name}` : ""}{item.label ? ` · ${item.label}` : ""}</span>
        </span>
        {!item.rings && <TriangleAlert aria-label="Rings nobody" className="size-4 text-status-away" />}
        {!readOnly && <Button variant="outline" size="sm" onClick={onChange} aria-label={`Change where ${title(item)} goes`}>Change</Button>}
        {onTry && (
          <Button variant="ghost" size="sm" onClick={onTry} aria-label={`Try a call to ${title(item)}`}>
            <FlaskConical aria-hidden="true" className="size-4" /> Try
          </Button>
        )}
      </div>
      <p className="text-sm text-muted-foreground">{item.words}</p>
    </li>
  );
}

export function IncomingCallsScreen({ me }: { me: Me }) {
  const [items, setItems] = useState<Incoming[] | null>(null);
  const [groups, setGroups] = useState<RingGroup[]>([]);
  const [extensions, setExtensions] = useState<ExtensionChoice[]>([]);
  const [schedules, setSchedules] = useState<Schedule[]>([]);
  const [zone, setZone] = useState("UTC");
  const [home, setHome] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const readOnly = isReadOnlyAdmin(me) || !hasScope(me, "routing:write");

  const load = useCallback(async () => {
    const [i, g, e, s, st] = await Promise.all([
      api.GET("/api/v1/incoming"),
      api.GET("/api/v1/ring-groups"),
      api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/schedules"),
      api.GET("/api/v1/settings"),
    ]);
    if (i.data) setItems(i.data.items);
    if (g.data) setGroups(g.data.items);
    if (e.data) setExtensions(e.data.items.filter((x: Extension) => x.enabled).map((x: Extension) => ({ id: x.id, number: x.number, display_name: x.display_name })));
    if (s.data) { setSchedules(s.data.items); setZone(s.data.time_zone); }
    if (st.data) setHome(st.data.site_kind === "home");
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (items === null) return <div className="p-6" aria-busy="true" />;
  const numbers = items.filter((i) => i.kind === "number");
  // Lines that take calls for other numbers: phone systems, where it matters most, and any line that has one set.
  const lines = items.filter((i) => i.kind === "line" && (i.line_kind === "registers_here" || i.line_kind === "lan_peer" || i.rings || !i.just_ring));
  const selected = items.find((i) => i.id === open);

  return (
    <div className="w-full max-w-4xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">Incoming calls</h1>
      <p className="mt-1 text-sm text-muted-foreground">When someone calls one of your numbers: who rings, if nobody answers, and outside office hours.</p>

      {numbers.length === 0 ? (
        <div className="mt-6 flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
          <p className="max-w-prose text-sm text-muted-foreground">
            No phone numbers yet. Numbers come with a phone line: add them on the line.
          </p>
          <Button variant="outline" onClick={() => navigate("/admin/lines")}>Phone lines</Button>
        </div>
      ) : (
        <ul className="mt-6 flex flex-col divide-y rounded-lg border" aria-label="Your numbers">
          {numbers.map((i) => (
            <IncomingCard key={i.id} item={i} readOnly={readOnly} onChange={() => setOpen(i.id)}
              onTry={() => navigate(`/admin/simulator?direction=in&number=${encodeURIComponent(i.number ?? "")}`)} />
          ))}
        </ul>
      )}

      {lines.length > 0 && (
        <section className="mt-8">
          <h2 className="text-lg font-semibold">Calls for any other number</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            A call that comes in on a line with none of its numbers (a landline often sends none).
          </p>
          <ul className="mt-3 flex flex-col divide-y rounded-lg border" aria-label="Lines' other calls">
            {lines.map((i) => <IncomingCard key={i.id} item={i} readOnly={readOnly} onChange={() => setOpen(i.id)} />)}
          </ul>
        </section>
      )}

      {readOnly && <p className="mt-4 text-sm text-muted-foreground">You can see where calls go; changing it needs an admin.</p>}
      {selected && (
        <IncomingWizard item={selected} schedules={schedules} zone={zone} groups={groups} extensions={extensions} home={home}
          onClose={() => setOpen(null)} onSaved={(n) => setItems((list) => list?.map((x) => (x.id === n.id ? n : x)) ?? list)} />
      )}
    </div>
  );
}
