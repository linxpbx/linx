// The Dialer (WEB_SCREENS_PHASE1C.md §3).
import { useEffect, useState, type FormEvent } from "react";
import { Delete, Phone, PhoneIncoming, PhoneMissed, PhoneOutgoing, Video } from "lucide-react";
import { api, type TeamMember } from "@/api/client";
import { Keypad } from "@/components/Keypad";
import { Avatar, StatusDot, statusLabel } from "@/components/presence";
import { Input } from "@/components/ui/input";
import { usePhoneLine, usePhoneState } from "@/phone/context";
import { navigate } from "@/hooks/useRoute";
import { callBackNumber, dayHeading, isMissed, otherParty, resultWords, timeOf, type CallRecord } from "@/lib/calls";
import { cn } from "@/lib/utils";

export const DIALABLE = /^[0-9*#+]+$/;

export function matchTeam(members: TeamMember[] | null, query: string, me?: string): TeamMember[] {
  const q = query.trim().toLowerCase();
  if (!q || !members) return [];
  return members.filter((m) => m.extension !== me && (m.name.toLowerCase().includes(q) || m.extension.startsWith(q))).slice(0, 5);
}

const RECENT = 5;

export function DialerScreen({ members, stamp }: { members: TeamMember[] | null; stamp?: number }) {
  const line = usePhoneLine();
  const { status, call, extension } = usePhoneState();
  // The last few calls from Call history (docs/ui/SCREENS_PHASE1F.md
  // §13.1), again whenever call history changes.
  const [recent, setRecent] = useState<CallRecord[] | null>(null);
  useEffect(() => {
    let live = true;
    void api.GET("/api/v1/me/calls", { params: { query: { limit: RECENT } } }).then(({ data }) => { if (live && data) setRecent(data.items); });
    return () => { live = false; };
  }, [stamp]);
  const [value, setValue] = useState("");
  const matches = matchTeam(members, value, extension);
  const canCall = status === "ready" && !call;
  const dial = (e?: FormEvent, video = false) => {
    e?.preventDefault();
    const target = value.trim();
    if (!canCall || !target) return;
    if (DIALABLE.test(target)) {
      const who = members?.find((m) => m.extension === target);
      line.call(target, who?.name, video);
    } else if (matches[0]) {
      line.call(matches[0].extension, matches[0].name, video);
    } else {
      return;
    }
    setValue("");
  };
  const callMatch = (m: TeamMember) => { line.call(m.extension, m.name); setValue(""); };

  return (
    <div className="mx-auto flex w-full max-w-md flex-col items-center px-4 py-8">
      <h1 className="sr-only">Dialer</h1>
      <form onSubmit={dial} className="w-full">
        <div className="relative">
          <Input value={value} onChange={(e) => setValue(e.target.value)} aria-label="Name, extension or number"
            placeholder="Enter a name, extension or number" autoComplete="off"
            className="h-14 px-12 text-center font-mono text-2xl md:text-2xl placeholder:font-sans placeholder:text-base" />
          {value && (
            <button type="button" aria-label="Delete last" onClick={() => setValue(value.slice(0, -1))}
              className="absolute inset-y-0 end-3 my-auto size-8 rounded-full text-muted-foreground hover:text-foreground [&_svg]:mx-auto">
              <Delete className="size-5" />
            </button>
          )}
        </div>
        {/* One row, always the same height, so the keypad below never moves
            while matches come and go (owner, 2026-10-17): the best match here,
            any others under the call buttons. */}
        <div className="mt-2 h-14">
          {matches[0] && <MatchList matches={matches.slice(0, 1)} canCall={canCall} onCall={callMatch} />}
        </div>
      </form>
      <div className="mt-4">
        <Keypad onKey={(k) => setValue((v) => v + k)} />
      </div>
      {/* Call in green, Video call in Cobalt (owner, 2026-10-12). A video
          call to a desk phone or an outside number goes on as sound. */}
      <div className="mt-8 flex items-start gap-6">
        <button type="button" onClick={() => dial()} disabled={!canCall || !value.trim()} aria-label="Call"
          className="inline-flex size-18 items-center justify-center rounded-full bg-call text-call-foreground shadow-md transition-colors hover:bg-call/90 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40">
          <Phone className="size-7" />
        </button>
        <button type="button" onClick={() => dial(undefined, true)} disabled={!canCall || !value.trim()} aria-label="Video call"
          className="inline-flex size-18 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-md transition-colors hover:bg-primary/90 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40">
          <Video className="size-7" />
        </button>
      </div>
      {matches.length > 1 && (
        <div className="mt-6 w-full">
          <MatchList matches={matches.slice(1)} canCall={canCall} onCall={callMatch} label="More matching people" />
        </div>
      )}

      <section className="mt-10 w-full" aria-labelledby="recent-title">
        <h2 id="recent-title" className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">Recent</h2>
        {recent === null ? <div className="mt-3 h-12" aria-busy="true" /> : recent.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">Calls you make or get show up here.</p>
        ) : (
          <ul className="mt-2 divide-y rounded-md border bg-card">
            {recent.map((c) => {
              const missed = isMissed(c);
              const Icon = missed ? PhoneMissed : c.placed_by_me ? PhoneOutgoing : PhoneIncoming;
              const back = callBackNumber(c);
              const who = members?.find((m) => m.extension === back);
              const day = dayHeading(c.started_at);
              return (
                <li key={c.id}>
                  <button type="button" disabled={!canCall || !back} onClick={() => line.call(back, who?.name ?? undefined)}
                    className="flex w-full items-center gap-3 px-3 py-2.5 text-start hover:bg-background disabled:opacity-60">
                    <Icon aria-hidden="true" className={cn("size-4", missed ? "text-destructive" : "text-muted-foreground")} />
                    <span className="min-w-0 flex-1">
                      <span className={cn("block break-words font-medium", missed && "text-destructive")}>{otherParty(c)}</span>
                      <span className="block text-sm text-muted-foreground">
                        {resultWords(c, true)}, {day === "Today" ? timeOf(c.started_at) : `${day} ${timeOf(c.started_at)}`}
                      </span>
                    </span>
                    {who && <StatusDot status={who.status} />}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        <button type="button" onClick={() => navigate("/calls")} className="mt-3 text-sm font-medium text-link underline-offset-4 hover:underline">
          All calls
        </button>
      </section>
    </div>
  );
}

function MatchList({ matches, canCall, onCall, label = "Matching people" }: {
  matches: TeamMember[]; canCall: boolean; onCall: (m: TeamMember) => void; label?: string;
}) {
  return (
    <ul className="divide-y overflow-hidden rounded-md border bg-card" aria-label={label}>
      {matches.map((m) => (
        <li key={m.extension}>
          <button type="button" disabled={!canCall} onClick={() => onCall(m)}
            className="flex h-14 w-full items-center gap-3 px-3 text-start hover:bg-background disabled:opacity-50">
            <Avatar name={m.name} status={m.status} />
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium">{m.name}</span>
              <span className="block truncate text-sm text-muted-foreground">Ext <span className="font-mono">{m.extension}</span> · {statusLabel[m.status]}</span>
            </span>
            <Phone aria-hidden="true" className="size-4 text-call" />
          </button>
        </li>
      ))}
    </ul>
  );
}
