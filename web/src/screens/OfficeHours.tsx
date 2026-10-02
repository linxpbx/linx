// Office hours and holidays (docs/ui/SCREENS_PHASE1F.md §9, ADR-068): the
// week, holidays, and more schedules. "Now: open" is the database's own
// answer, the one calls get.
import { useCallback, useEffect, useState } from "react";
import { ChevronRight, Plus, X } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { hasScope, isReadOnlyAdmin } from "@/lib/roles";

export type Schedule = components["schemas"]["Schedule"];
type Span = components["schemas"]["Span"];
type Holiday = components["schemas"]["Holiday"];

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
/** Monday first, the way the hours are written ("Mon–Fri 08:00–17:00"). */
const WEEK = [1, 2, 3, 4, 5, 6, 0];

/** "Asia/Dubai (UTC+4)" */
export function zoneLabel(zone: string): string {
  try {
    const part = new Intl.DateTimeFormat("en-GB", { timeZone: zone, timeZoneName: "shortOffset" })
      .formatToParts(new Date()).find((p) => p.type === "timeZoneName")?.value;
    return part && part !== zone ? `${zone} (${part.replace("GMT", "UTC")})` : zone;
  } catch {
    return zone;
  }
}

/** The moment that is day ("2026-10-02") at time ("20:00") in zone, as ISO. */
export function zonedISO(day: string, time: string, zone: string): string {
  const [y = 1970, mo = 1, d = 1] = day.split("-").map(Number);
  const [h = 0, mi = 0] = time.split(":").map(Number);
  const wall = Date.UTC(y, mo - 1, d, h, mi);
  const offset = (at: number) => {
    try {
      const p = Object.fromEntries(new Intl.DateTimeFormat("en-GB", {
        timeZone: zone, hourCycle: "h23", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
      }).formatToParts(new Date(at)).map((x) => [x.type, x.value]));
      const n = (k: string) => Number(p[k] ?? 0);
      return Date.UTC(n("year"), n("month") - 1, n("day"), n("hour"), n("minute")) - at;
    } catch {
      return 0;
    }
  };
  let at = wall - offset(wall);
  at = wall - offset(at);
  return new Date(at).toISOString();
}

/** Today's date in zone, "2026-10-02". */
export function zonedToday(zone: string): string {
  try {
    return new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date());
  } catch {
    return new Date().toISOString().slice(0, 10);
  }
}

/** "17:00" or "Mon 08:00" in the server's zone. */
function whenText(iso: string, zone: string): string {
  const d = new Date(iso);
  const opts: Intl.DateTimeFormatOptions = { timeZone: zone, hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
  const time = new Intl.DateTimeFormat("en-GB", opts).format(d);
  const day = (x: Date) => new Intl.DateTimeFormat("en-GB", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit" }).format(x);
  if (day(d) === day(new Date())) return time;
  return `${new Intl.DateTimeFormat("en-GB", { timeZone: zone, weekday: "short" }).format(d)} ${time}`;
}

export function nowText(s: Schedule, zone: string): string {
  if (s.now.holiday) return `Now: closed (${s.now.holiday})`;
  const at = s.now.changes_at ? whenText(s.now.changes_at, zone) : "";
  if (s.now.open) return at ? `Now: open (closes at ${at})` : "Now: open";
  return at ? `Now: closed (opens ${at})` : "Now: closed";
}

/** "20 – 22 Mar 2027", "1 Jan 2027", "2 Dec, every year" */
function holidayDates(h: Holiday): string {
  const parse = (s: string) => new Date(`${s}T12:00:00Z`);
  const f = (d: Date, o: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat("en-GB", { timeZone: "UTC", ...o }).format(d);
  const a = parse(h.first_day);
  const b = parse(h.last_day);
  const year = h.every_year ? {} : { year: "numeric" as const };
  if (h.first_day === h.last_day) return `${f(a, { day: "numeric", month: "short", ...year })}${h.every_year ? ", every year" : ""}`;
  const sameMonth = a.getUTCMonth() === b.getUTCMonth() && a.getUTCFullYear() === b.getUTCFullYear();
  const left = sameMonth ? f(a, { day: "numeric" }) : f(a, { day: "numeric", month: "short" });
  return `${left} – ${f(b, { day: "numeric", month: "short", ...year })}${h.every_year ? ", every year" : ""}`;
}

function isPast(h: Holiday): boolean {
  return !h.every_year && h.last_day < new Date().toISOString().slice(0, 10);
}

/** Every quarter hour, 24-hour clock; closing can also be 24:00 (midnight). */
const TIMES = Array.from({ length: 96 }, (_, i) => `${String(Math.floor(i / 4)).padStart(2, "0")}:${String((i % 4) * 15).padStart(2, "0")}`);

export function TimeSelect({ value, onChange, label, closing, disabled }: {
  value: string; onChange: (v: string) => void; label: string; closing?: boolean; disabled?: boolean;
}) {
  const options = closing ? [...TIMES.slice(1), "24:00"] : TIMES;
  const list = options.includes(value) ? options : [...options, value].sort();
  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger className="w-24 font-mono tabular-nums" aria-label={label}><SelectValue /></SelectTrigger>
      <SelectContent className="max-h-72">
        {list.map((t) => <SelectItem key={t} value={t} className="font-mono tabular-nums">{t === "24:00" ? "24:00 (midnight)" : t}</SelectItem>)}
      </SelectContent>
    </Select>
  );
}

function AddHolidayDialog({ open, onOpenChange, onAdd }: { open: boolean; onOpenChange: (v: boolean) => void; onAdd: (h: Holiday) => Promise<string> }) {
  const [name, setName] = useState("");
  const [first, setFirst] = useState("");
  const [last, setLast] = useState("");
  const [everyYear, setEveryYear] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (open) { setName(""); setFirst(""); setLast(""); setEveryYear(false); setError(""); } }, [open]);
  const add = async () => {
    setBusy(true);
    const err = await onAdd({ name: name.trim(), first_day: first, last_day: last || first, every_year: everyYear });
    setBusy(false);
    if (err) setError(err);
    else onOpenChange(false);
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add a holiday</DialogTitle>
          <DialogDescription>Closed all day. For several days, give the last day too.</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="holiday-name">Name</Label>
            <Input id="holiday-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Eid al-Fitr" maxLength={60} />
          </div>
          <div className="flex flex-wrap gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="holiday-first">First day</Label>
              <Input id="holiday-first" type="date" value={first} onChange={(e) => setFirst(e.target.value)} className="w-44" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="holiday-last">Last day <span className="font-normal text-muted-foreground">(if more than one)</span></Label>
              <Input id="holiday-last" type="date" value={last} min={first} onChange={(e) => setLast(e.target.value)} className="w-44" />
            </div>
          </div>
          <label htmlFor="holiday-every" className="flex items-start gap-2 text-sm">
            <Checkbox id="holiday-every" checked={everyYear} onCheckedChange={(v) => setEveryYear(v === true)} className="mt-0.5" />
            <span>
              Every year on the same dates
              <span className="block text-muted-foreground">For fixed holidays like National Day. Eid moves each year: add it again.</span>
            </span>
          </label>
          {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={() => void add()} disabled={busy || !name.trim() || !first} aria-busy={busy}>Add</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ScheduleEditor({ schedule, readOnly, onSaved, onRemove }: {
  schedule: Schedule; readOnly: boolean; onSaved: (s: Schedule) => void; onRemove?: () => void;
}) {
  const [spans, setSpans] = useState<Span[]>(schedule.spans);
  const [dirty, setDirty] = useState(false);
  const [saved, setSaved] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);
  const [showPast, setShowPast] = useState(false);
  useEffect(() => { setSpans(schedule.spans); setDirty(false); }, [schedule]);

  const say = (what: string) => { setSaved(what); window.setTimeout(() => setSaved(""), 2500); };
  const save = async (body: { spans?: Span[]; holidays?: Holiday[] }): Promise<string> => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/schedules/{id}", {
      params: { path: { id: schedule.id }, header: { "If-Match": schedule.etag } }, body,
    });
    setBusy(false);
    if (!data) return problemMessage(err);
    onSaved(data);
    say("Saved.");
    return "";
  };
  const change = (next: Span[]) => { setSpans(next); setDirty(true); };
  const day = (d: number) => spans.filter((s) => s.weekday === d).sort((a, b) => a.opens.localeCompare(b.opens));
  const others = (d: number) => spans.filter((s) => s.weekday !== d);
  const toggle = (d: number, on: boolean) => change(on ? [...spans, { weekday: d, opens: "08:00", closes: "17:00" }] : others(d));
  const setSpan = (d: number, i: number, part: "opens" | "closes", v: string) => {
    if (!v) return;
    change([...others(d), ...day(d).map((s, j) => (j === i ? { ...s, [part]: v } : s))]);
  };
  const addSpan = (d: number) => {
    const list = day(d);
    const last = list[list.length - 1];
    if (!last) return;
    // A lunch break when there's one span around midday; otherwise the evening.
    if (list.length === 1 && last.opens < "13:00" && last.closes > "14:00") {
      change([...others(d), { ...last, closes: "13:00" }, { weekday: d, opens: "14:00", closes: last.closes }]);
    } else if (last.closes < "24:00") {
      change([...others(d), ...list, { weekday: d, opens: last.closes, closes: "24:00" }]);
    }
  };
  const removeSpan = (d: number, i: number) => change([...others(d), ...day(d).filter((_, j) => j !== i)]);
  const copyMonday = () => {
    const monday = day(1);
    const open = WEEK.filter((d) => d !== 1 && day(d).length > 0);
    change([...monday, ...spans.filter((s) => s.weekday !== 1 && !open.includes(s.weekday)),
      ...open.flatMap((d) => monday.map((s) => ({ ...s, weekday: d })))]);
  };
  const holidays = schedule.holidays;
  const upcoming = holidays.filter((h) => !isPast(h));
  const past = holidays.filter(isPast);
  const removeHoliday = (h: Holiday) => void save({ holidays: holidays.filter((x) => x !== h) }).then((e) => e && setError(e));

  return (
    <div className="flex flex-col gap-6">
      <section aria-label={`${schedule.name}: the week`}>
        <ul className="flex flex-col divide-y rounded-lg border">
          {WEEK.map((d) => {
            const list = day(d);
            const on = list.length > 0;
            return (
              <li key={d} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5">
                <span className="flex w-36 shrink-0 items-center gap-3">
                  <Switch id={`day-${schedule.id}-${d}`} checked={on} disabled={readOnly} onCheckedChange={(v) => toggle(d, v)}
                    aria-label={`Open on ${DAYS[d]}`} />
                  <label htmlFor={`day-${schedule.id}-${d}`} className="text-sm font-medium">{SHORT[d]}</label>
                </span>
                {!on ? <span className="text-sm text-muted-foreground">Closed</span> : (
                  <span className="flex min-w-0 flex-wrap items-center gap-2">
                    {list.map((s, i) => (
                      <span key={`${s.opens}-${i}`} className="flex items-center gap-1.5">
                        <TimeSelect value={s.opens} disabled={readOnly} label={`${DAYS[d]} opens`} onChange={(v) => setSpan(d, i, "opens", v)} />
                        <span aria-hidden="true">–</span>
                        <TimeSelect value={s.closes} disabled={readOnly} label={`${DAYS[d]} closes`} closing onChange={(v) => setSpan(d, i, "closes", v)} />
                        {list.length > 1 && !readOnly && (
                          <Button variant="ghost" size="icon" aria-label={`Remove ${DAYS[d]} ${s.opens}–${s.closes}`} onClick={() => removeSpan(d, i)}>
                            <X aria-hidden="true" className="size-4" />
                          </Button>
                        )}
                      </span>
                    ))}
                    {!readOnly && (list[list.length - 1]?.closes ?? "24:00") < "24:00" && (
                      <Button variant="ghost" size="icon" aria-label={`Add another time on ${DAYS[d]}`} onClick={() => addSpan(d)}>
                        <Plus aria-hidden="true" className="size-4" />
                      </Button>
                    )}
                  </span>
                )}
              </li>
            );
          })}
        </ul>
        {!readOnly && (
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <Button onClick={() => void save({ spans }).then((e) => (e ? setError(e) : setDirty(false)))} disabled={!dirty || busy} aria-busy={busy}>
              Save hours
            </Button>
            {day(1).length > 0 && <Button variant="outline" onClick={copyMonday}>Copy Monday to every open day</Button>}
            {dirty && <span className="text-sm text-muted-foreground">Not saved yet.</span>}
          </div>
        )}
      </section>

      <section aria-labelledby={`holidays-${schedule.id}`}>
        <div className="flex items-center justify-between gap-3">
          <h2 id={`holidays-${schedule.id}`} className="text-lg font-semibold">Holidays</h2>
          {!readOnly && <Button variant="outline" size="sm" onClick={() => setAdding(true)}>+ Add</Button>}
        </div>
        <p className="mt-1 text-sm text-muted-foreground">Closed all day, whatever the week says.</p>
        {upcoming.length === 0 ? (
          <p className="mt-3 rounded-lg border border-dashed p-4 text-sm text-muted-foreground">No holidays coming up.</p>
        ) : (
          <ul className="mt-3 flex flex-col divide-y rounded-lg border" aria-label="Holidays">
            {upcoming.map((h, i) => (
              <li key={`${h.name}-${i}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2">
                <span className="min-w-40 grow text-sm font-medium">{h.name}</span>
                <span className="text-sm text-muted-foreground">{holidayDates(h)}</span>
                {!readOnly && (
                  <Button variant="ghost" size="icon" aria-label={`Remove ${h.name}`} onClick={() => removeHoliday(h)}>
                    <X aria-hidden="true" className="size-4" />
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {past.length > 0 && (
          <div className="mt-2">
            <button type="button" className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
              aria-expanded={showPast} onClick={() => setShowPast(!showPast)}>
              <ChevronRight aria-hidden="true" className={`size-4 transition-transform ${showPast ? "rotate-90" : ""}`} />
              Past holidays ({past.length})
            </button>
            {showPast && (
              <ul className="mt-2 flex flex-col divide-y rounded-lg border text-muted-foreground">
                {past.map((h, i) => (
                  <li key={`${h.name}-${i}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2">
                    <span className="min-w-40 grow text-sm">{h.name}</span>
                    <span className="text-sm">{holidayDates(h)}</span>
                    {!readOnly && (
                      <Button variant="ghost" size="icon" aria-label={`Remove ${h.name}`} onClick={() => removeHoliday(h)}>
                        <X aria-hidden="true" className="size-4" />
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </section>

      <p className="min-h-5 text-sm text-status-available" role="status">{saved}</p>
      {error && <p role="alert" className="-mt-4 text-sm font-medium text-destructive">{error}</p>}
      {onRemove && !readOnly && (
        <div>
          <Button variant="outline" onClick={onRemove} disabled={schedule.used_by.length > 0}>Remove {schedule.name}</Button>
          {schedule.used_by.length > 0 && (
            <p className="mt-1 text-sm text-muted-foreground">Used by {schedule.used_by.map((u) => u.name).join(", ")}: change those first.</p>
          )}
        </div>
      )}
      <AddHolidayDialog open={adding} onOpenChange={setAdding} onAdd={(h) => save({ holidays: [...holidays, h] })} />
    </div>
  );
}

export function OfficeHoursScreen({ me }: { me: Me }) {
  const [schedules, setSchedules] = useState<Schedule[] | null>(null);
  const [zone, setZone] = useState("");
  const [home, setHome] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [error, setError] = useState("");
  const readOnly = isReadOnlyAdmin(me) || !hasScope(me, "routing:write");

  const load = useCallback(async () => {
    const [s, st] = await Promise.all([api.GET("/api/v1/schedules"), api.GET("/api/v1/settings")]);
    if (s.data) { setSchedules(s.data.items); setZone(s.data.time_zone); }
    if (st.data) setHome(st.data.site_kind === "home");
  }, []);
  useEffect(() => { void load(); }, [load]);

  const create = async (name: string) => {
    setError("");
    const { data, error: err } = await api.POST("/api/v1/schedules", { body: { name } });
    if (!data) { setError(problemMessage(err)); return; }
    setNewName("");
    void load();
  };
  const remove = async (s: Schedule) => {
    setError("");
    const { response, error: err } = await api.DELETE("/api/v1/schedules/{id}", { params: { path: { id: s.id } } });
    if (!response.ok) { setError(problemMessage(err)); return; }
    void load();
  };
  const replace = (s: Schedule) => setSchedules((list) => list?.map((x) => (x.id === s.id ? s : x)) ?? list);

  if (schedules === null) return <div className="p-6" aria-busy="true" />;
  const main = schedules.find((s) => s.name === "Office hours") ?? schedules[0];
  const more = schedules.filter((s) => s !== main);

  return (
    <div className="w-full max-w-4xl px-4 py-6 md:px-6">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1">
        <h1 className="font-display text-3xl font-semibold tracking-tight">{main?.name ?? "Office hours"}</h1>
        {main && <p className="pt-2 text-sm font-medium" role="status">{nowText(main, zone)}</p>}
      </div>
      <p className="mt-1 text-sm text-muted-foreground">Times are {zoneLabel(zone)} time, the server's.</p>

      {!main ? (
        <div className="mt-6 flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
          <p className="max-w-md text-sm text-muted-foreground">
            {home
              ? "At home there are no office hours: every number rings the same all the time."
              : "No office hours yet. Add them, then choose in Incoming which numbers follow them."}
          </p>
          {!readOnly && <Button onClick={() => void create("Office hours")}>Add office hours</Button>}
        </div>
      ) : (
        <div className="mt-6">
          <ScheduleEditor schedule={main} readOnly={readOnly} onSaved={replace}
            onRemove={more.length > 0 || home ? () => void remove(main) : undefined} />
        </div>
      )}

      {main && (
        <section className="mt-8 border-t pt-4">
          <button type="button" className="flex items-center gap-1 text-sm font-medium" aria-expanded={moreOpen} onClick={() => setMoreOpen(!moreOpen)}>
            <ChevronRight aria-hidden="true" className={`size-4 transition-transform ${moreOpen ? "rotate-90" : ""}`} />
            More schedules{more.length > 0 ? ` (${more.length})` : ""}
            <span className="font-normal text-muted-foreground">, e.g. Support hours</span>
          </button>
          {moreOpen && (
            <div className="mt-4 flex flex-col gap-8">
              {more.map((s) => (
                <div key={s.id}>
                  <div className="mb-3 flex flex-wrap items-baseline justify-between gap-x-4">
                    <h2 className="text-xl font-semibold">{s.name}</h2>
                    <span className="text-sm text-muted-foreground">{nowText(s, zone)}</span>
                  </div>
                  <ScheduleEditor schedule={s} readOnly={readOnly} onSaved={replace} onRemove={() => void remove(s)} />
                </div>
              ))}
              {!readOnly && (
                <form className="flex flex-wrap items-end gap-3" onSubmit={(e) => { e.preventDefault(); if (newName.trim()) void create(newName.trim()); }}>
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="new-schedule">Add a schedule</Label>
                    <Input id="new-schedule" value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Support hours" maxLength={60} className="w-56" />
                  </div>
                  <Button type="submit" variant="outline" disabled={!newName.trim()}>Add</Button>
                </form>
              )}
            </div>
          )}
        </section>
      )}
      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}
      {readOnly && <p className="mt-4 text-sm text-muted-foreground">You can see the hours; changing them needs an admin.</p>}
    </div>
  );
}
