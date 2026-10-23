// The Call history tab (docs/ui/SCREENS_PHASE1F.md §13.1, ADR-070): my
// calls, newest first by day, missed ones marked, with Call back. A row
// opens its detail: the steps, the line, the voicemail left. Lists again
// when the Team websocket says call history changed (no polling).
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ChevronDown, Phone, PhoneIncoming, PhoneMissed, PhoneOutgoing, Search, Trash2 } from "lucide-react";
import { api, problemMessage, type TeamMember } from "@/api/client";
import { CallDetails } from "@/components/CallDetails";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { byDay, callBackNumber, isMissed, otherParty, resultWords, timeOf, type CallRecord } from "@/lib/calls";
import { cn } from "@/lib/utils";
import { usePhoneLine, usePhoneState } from "@/phone/context";

/// How long "Undo" stands after a call is taken out of this person's list:
/// the same ten seconds the web offers after a routing change (ADR-071).
const UNDO_LASTS = 10_000;

function CallRow({ c, open, onToggle, canCall, onCall, onRemove }: {
  c: CallRecord; open: boolean; onToggle: () => void; canCall: boolean; onCall: () => void; onRemove: () => void;
}) {
  const missed = isMissed(c);
  const Icon = missed ? PhoneMissed : c.placed_by_me ? PhoneOutgoing : PhoneIncoming;
  const back = callBackNumber(c);
  const who = otherParty(c);
  return (
    <li className="rounded-lg border bg-card" data-testid="call-row">
      <div className="flex items-center gap-3 p-3">
        <button type="button" onClick={onToggle} aria-expanded={open}
          className="flex min-w-0 flex-1 items-center gap-3 rounded-md text-start outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
          <Icon aria-hidden="true" className={cn("size-5 shrink-0", missed ? "text-destructive" : "text-muted-foreground")} />
          <span className="min-w-0 flex-1">
            <span className={cn("block break-words font-medium", missed && "text-destructive")}>
              {who}
              {c.ring_group && !c.placed_by_me && <span className="font-normal text-muted-foreground"> · rang {c.ring_group}</span>}
            </span>
            <span className="block text-sm text-muted-foreground">
              <span className="sr-only">{c.placed_by_me ? "Outgoing" : "Incoming"} · </span>{resultWords(c, true)}
              <span className="sm:hidden"> · <span className="font-mono tabular-nums">{timeOf(c.started_at)}</span></span>
            </span>
          </span>
          <span className="shrink-0 font-mono text-sm tabular-nums text-muted-foreground max-sm:hidden">{timeOf(c.started_at)}</span>
          <ChevronDown aria-hidden="true" className={cn("size-4 shrink-0 text-muted-foreground transition-transform max-sm:hidden", open && "rotate-180")} />
        </button>
        <Button size="sm" variant="outline" disabled={!canCall || !back} onClick={onCall} aria-label={`Call ${who}`}
          title={!back ? "There's no number to call back" : undefined}>
          <Phone aria-hidden="true" /> <span className="max-sm:sr-only">Call</span>
        </Button>
        {/* Out of **this person's** list only: Linx keeps the call, and
            Admin → Calls still shows it (owner, 2026-10-23). Undo stands
            for ten seconds, as it does after a routing change. */}
        <Button size="icon" variant="ghost" onClick={onRemove} aria-label={`Remove this call with ${who} from my list`}
          title="Remove from my list">
          <Trash2 aria-hidden="true" className="text-destructive" />
        </Button>
      </div>
      {open && <div className="border-t px-4 py-3"><CallDetails c={c} /></div>}
    </li>
  );
}

export function CallHistoryScreen({ members, stamp }: { members: TeamMember[] | null; stamp?: number }) {
  const line = usePhoneLine();
  const { status, call } = usePhoneState();
  const [missedOnly, setMissedOnly] = useState(false);
  const [search, setSearch] = useState("");
  const [number, setNumber] = useState("");
  const [items, setItems] = useState<CallRecord[] | null>(null);
  const [next, setNext] = useState<string | undefined>();
  const [keep, setKeep] = useState(365);
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async (before?: string) => {
    const { data, error: err } = await api.GET("/api/v1/me/calls", {
      params: { query: { ...(missedOnly ? { missed: true } : {}), ...(number ? { number } : {}), ...(before ? { before } : {}) } },
    });
    if (!data) { setError(problemMessage(err, "Linx couldn't load your calls.")); return; }
    setError("");
    setItems((list) => (before ? [...(list ?? []), ...data.items] : data.items));
    setNext(data.next);
    setKeep(data.keep_days);
  }, [missedOnly, number]);
  useEffect(() => { void load(); }, [load, stamp]);

  const canCall = status === "ready" && !call;
  const callBack = (c: CallRecord) => {
    const n = callBackNumber(c);
    const name = members?.find((m) => m.extension === n)?.name ?? (c.placed_by_me ? c.to.name : c.from.name) ?? undefined;
    line.call(n, name || undefined);
  };
  const submit = (e: FormEvent) => { e.preventDefault(); setNumber(search.trim()); };

  // Taking a call out of my own list. The row goes at once, Linx is told,
  // and Undo puts it back for ten seconds afterwards; if Linx wouldn't
  // have it, the row comes back with a word (owner, 2026-10-23).
  const [undo, setUndo] = useState<CallRecord | null>(null);
  const remove = async (c: CallRecord) => {
    setItems((list) => (list ?? []).filter((x) => x.id !== c.id));
    const { error: err } = await api.DELETE("/api/v1/me/calls/{id}", { params: { path: { id: c.id } } });
    if (err) {
      setItems((list) => [...(list ?? []), c].sort((a, b) => b.started_at.localeCompare(a.started_at)));
      setError(problemMessage(err, "Linx couldn't remove that call, so it is still in your list."));
      return;
    }
    setError("");
    setUndo(c);
    window.setTimeout(() => setUndo((u) => (u?.id === c.id ? null : u)), UNDO_LASTS);
  };
  const putBack = async (c: CallRecord) => {
    setUndo(null);
    const { error: err } = await api.POST("/api/v1/me/calls/{id}/put-back", { params: { path: { id: c.id } } });
    if (err) { setError(problemMessage(err, "Linx couldn't put that call back.")); return; }
    await load();
  };

  return (
    <div className="w-full max-w-4xl px-4 py-6 md:px-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="me-auto font-display text-3xl font-semibold tracking-tight">Call history</h1>
        <div role="group" aria-label="Which calls" className="inline-flex rounded-md border bg-card p-0.5">
          {[false, true].map((m) => (
            <button key={String(m)} type="button" aria-pressed={missedOnly === m} onClick={() => setMissedOnly(m)}
              className={cn("rounded px-3 py-1.5 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
                missedOnly === m ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground")}>
              {m ? "Missed" : "All"}
            </button>
          ))}
        </div>
        <form role="search" onSubmit={submit} className="relative w-full sm:w-56">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={search} onChange={(e) => { setSearch(e.target.value); if (!e.target.value.trim()) setNumber(""); }}
            placeholder="Search number" aria-label="Search number" inputMode="tel" className="ps-9" />
        </form>
      </div>
      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}

      {items === null && !error ? <div className="mt-6 h-40" aria-busy="true" /> : items && items.length === 0 ? (
        <div className="mt-10 flex flex-col items-center gap-2 rounded-lg border border-dashed p-10 text-center">
          <p className="font-medium">{missedOnly ? "No missed calls" : number ? "No calls with that number" : "No calls yet"}</p>
          <p className="max-w-md text-sm text-muted-foreground">
            Calls you make, get or miss appear here, from any of your phones and browsers.
          </p>
        </div>
      ) : (
        <div className="mt-6 flex flex-col gap-6">
          {byDay(items ?? []).map((d) => (
            <section key={d.day} aria-label={d.day} className="flex flex-col gap-2">
              <h2 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">{d.day}</h2>
              <ul className="flex flex-col gap-2">
                {d.calls.map((c) => (
                  <CallRow key={c.id} c={c} open={open === c.id} onToggle={() => setOpen(open === c.id ? null : c.id)}
                    canCall={canCall} onCall={() => callBack(c)} onRemove={() => void remove(c)} />
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}
      {undo && (
        <div role="status" className="mt-4 flex items-center gap-3 rounded-lg border bg-card p-3 text-sm">
          <span className="text-muted-foreground">Call removed from your list. Linx still keeps its own record.</span>
          <Button size="sm" variant="outline" className="ms-auto" onClick={() => void putBack(undo)}>Undo</Button>
        </div>
      )}
      {next && <div className="mt-4 flex justify-center"><Button variant="outline" onClick={() => void load(next)}>Show more</Button></div>}
      {items && <p className="mt-6 text-sm text-muted-foreground">Calls are kept {keep === 365 ? "1 year" : `${keep} days`}.</p>}
    </div>
  );
}
