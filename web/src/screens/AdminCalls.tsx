// Admin → Calls (docs/ui/SCREENS_PHASE1F.md §13.2, ADR-070): everyone's
// call history, filtered by person, number, missed and when, with
// Download (CSV) for the same filter. A row opens its detail: every step,
// the line, who answered, the voicemail (admins can play it). Reporters
// see it too.
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { Download, Search } from "lucide-react";
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { CallDetails } from "@/components/CallDetails";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { partyLabel, resultShort, talkClock, timeOf, dayHeading, type CallRecord } from "@/lib/calls";
import { cn } from "@/lib/utils";

type Extension = components["schemas"]["Extension"];

const PERIODS = [
  { value: "1", label: "Today" }, { value: "7", label: "Last 7 days" }, { value: "30", label: "Last 30 days" }, { value: "all", label: "Everything kept" },
];
const ANYONE = "anyone";

function since(period: string): string | undefined {
  if (period === "all") return undefined;
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  d.setDate(d.getDate() - (Number(period) - 1));
  return d.toISOString();
}

function when(iso: string): string {
  const day = dayHeading(iso);
  return day === "Today" ? timeOf(iso) : `${day} ${timeOf(iso)}`;
}

export function AdminCallsScreen() {
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [person, setPerson] = useState(ANYONE);
  const [search, setSearch] = useState("");
  const [number, setNumber] = useState("");
  const [missed, setMissed] = useState(false);
  const [period, setPeriod] = useState("7");
  const [items, setItems] = useState<CallRecord[] | null>(null);
  const [next, setNext] = useState<string | undefined>();
  const [keep, setKeep] = useState(365);
  const [selected, setSelected] = useState<CallRecord | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    void api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }).then(({ data }) => { if (data) setExtensions(data.items); });
  }, []);

  const query = useMemo(() => {
    const from = since(period);
    return {
      ...(person !== ANYONE ? { extension_id: person } : {}), ...(number ? { number } : {}),
      ...(missed ? { missed: true } : {}), ...(from ? { from } : {}),
    };
  }, [person, number, missed, period]);

  const load = useCallback(async (before?: string) => {
    const { data, error: err } = await api.GET("/api/v1/calls", { params: { query: { ...query, ...(before ? { before } : {}) } } });
    if (!data) { setError(problemMessage(err, "Linx couldn't load the calls.")); return; }
    setError("");
    setItems((list) => (before ? [...(list ?? []), ...data.items] : data.items));
    setNext(data.next);
    setKeep(data.keep_days);
  }, [query]);
  useEffect(() => { setItems(null); void load(); }, [load]);

  const csv = `/api/v1/calls/csv?${new URLSearchParams(Object.entries(query).map(([k, v]) => [k, String(v)])).toString()}`;
  const submit = (e: FormEvent) => { e.preventDefault(); setNumber(search.trim()); };
  const to = (c: CallRecord) => partyLabel(c.to) + (c.ring_group && c.to.name !== c.ring_group ? ` → ${c.ring_group}` : "");

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="me-auto font-display text-3xl font-semibold tracking-tight">Calls</h1>
        <Button variant="outline" asChild>
          <a href={csv} download><Download aria-hidden="true" /> Download (CSV)</a>
        </Button>
      </div>
      <div className="mt-6 flex flex-wrap items-center gap-3">
        <Select value={person} onValueChange={setPerson}>
          <SelectTrigger className="w-48" aria-label="Whose calls"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value={ANYONE}>Anyone</SelectItem>
            {extensions.map((x) => <SelectItem key={x.id} value={x.id}>{x.display_name} ({x.number})</SelectItem>)}
          </SelectContent>
        </Select>
        <form role="search" onSubmit={submit} className="relative w-full max-w-48">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={search} onChange={(e) => { setSearch(e.target.value); if (!e.target.value.trim()) setNumber(""); }}
            placeholder="Any number" aria-label="Number" inputMode="tel" className="ps-9" />
        </form>
        <div className="flex items-center gap-2">
          <Checkbox id="calls-missed" checked={missed} onCheckedChange={(v) => setMissed(v === true)} />
          <Label htmlFor="calls-missed" className="font-normal">Missed only</Label>
        </div>
        <Select value={period} onValueChange={setPeriod}>
          <SelectTrigger className="w-40" aria-label="When"><SelectValue /></SelectTrigger>
          <SelectContent>{PERIODS.map((p) => <SelectItem key={p.value} value={p.value}>{p.label}</SelectItem>)}</SelectContent>
        </Select>
      </div>

      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}
      {items === null && !error && <div className="mt-6 h-40" aria-busy="true" />}
      {items !== null && items.length === 0 && (
        <p className="mt-6 rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">No calls for this choice.</p>
      )}
      {items && items.length > 0 && (
        <div className="mt-4 rounded-lg border">
          <div aria-hidden="true" className="hidden grid-cols-[7.5rem_minmax(0,1fr)_minmax(0,1fr)_8rem_4rem] gap-x-4 border-b px-3 py-2 text-xs font-medium tracking-wide text-muted-foreground md:grid">
            <span>WHEN</span><span>FROM</span><span>TO</span><span>RESULT</span><span className="text-end">LENGTH</span>
          </div>
          <ul className="flex flex-col divide-y" aria-label="Calls">
            {items.map((c) => {
              const missedRow = c.missed;
              return (
                <li key={c.id}>
                  <button type="button" onClick={() => setSelected(c)} data-testid="admin-call-row"
                    className="grid w-full grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-0.5 px-3 py-2.5 text-start text-sm hover:bg-card md:grid-cols-[7.5rem_minmax(0,1fr)_minmax(0,1fr)_8rem_4rem]">
                    <span className="text-muted-foreground max-md:order-2 max-md:text-end">{when(c.started_at)}</span>
                    <span className="break-words max-md:order-1">{partyLabel(c.from)}</span>
                    <span className="break-words text-muted-foreground md:text-foreground max-md:order-3 max-md:col-span-2">
                      <span className="md:hidden">to </span>{to(c)}
                    </span>
                    <span className={cn("max-md:order-4", missedRow && "text-destructive")}>{resultShort(c)}</span>
                    <span className="font-mono tabular-nums text-muted-foreground max-md:order-5 md:text-end">
                      {c.result === "answered" ? talkClock(c.talk_seconds) : "—"}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {next && <div className="mt-3 flex justify-center"><Button variant="outline" onClick={() => void load(next)}>Show more</Button></div>}
      {items && (
        <p className="mt-6 text-sm text-muted-foreground">
          Kept {keep === 365 ? "1 year" : `${keep} days`} (System → Settings). The line each call used is in its detail.
        </p>
      )}

      {selected && (
        <Sheet open onOpenChange={(o) => { if (!o) setSelected(null); }}>
          <SheetContent className="w-full sm:max-w-md">
            <SheetHeader>
              <SheetTitle className="break-words">{partyLabel(selected.from)} → {to(selected)}</SheetTitle>
              <SheetDescription>{resultShort(selected)} · {when(selected.started_at)}</SheetDescription>
            </SheetHeader>
            <div className="overflow-y-auto px-4 pb-4"><CallDetails c={selected} /></div>
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}
