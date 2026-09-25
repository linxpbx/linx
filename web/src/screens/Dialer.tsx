// The Dialer (WEB_SCREENS_PHASE1C.md §3).
import { useState, type FormEvent } from "react";
import { Delete, Phone, PhoneIncoming, PhoneMissed, PhoneOutgoing } from "lucide-react";
import type { TeamMember } from "@/api/client";
import { Keypad } from "@/components/Keypad";
import { Avatar, StatusDot, statusLabel } from "@/components/presence";
import { Input } from "@/components/ui/input";
import { usePhoneLine, usePhoneState } from "@/phone/context";
import type { RecentCall } from "@/phone/line";

export const DIALABLE = /^[0-9*#+]+$/;

export function matchTeam(members: TeamMember[] | null, query: string, me?: string): TeamMember[] {
  const q = query.trim().toLowerCase();
  if (!q || !members) return [];
  return members.filter((m) => m.extension !== me && (m.name.toLowerCase().includes(q) || m.extension.startsWith(q))).slice(0, 5);
}

function recentWhen(at: number): string {
  const d = new Date(at);
  const today = new Date();
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === today.toDateString() ? time : `${d.toLocaleDateString()} ${time}`;
}

const recentIcon: Record<RecentCall["kind"], typeof Phone> = { outgoing: PhoneOutgoing, incoming: PhoneIncoming, missed: PhoneMissed };

export function DialerScreen({ members }: { members: TeamMember[] | null }) {
  const line = usePhoneLine();
  const { recent, status, call, extension } = usePhoneState();
  const [value, setValue] = useState("");
  const matches = matchTeam(members, value, extension);
  const canCall = status === "ready" && !call;
  const dial = (e?: FormEvent) => {
    e?.preventDefault();
    const target = value.trim();
    if (!canCall || !target) return;
    if (DIALABLE.test(target)) {
      const who = members?.find((m) => m.extension === target);
      line.call(target, who?.name);
    } else if (matches[0]) {
      line.call(matches[0].extension, matches[0].name);
    } else {
      return;
    }
    setValue("");
  };

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
        {matches.length > 0 && (
          <ul className="mt-2 overflow-hidden rounded-md border bg-card" aria-label="Matching people">
            {matches.map((m) => (
              <li key={m.extension}>
                <button type="button" disabled={!canCall} onClick={() => { line.call(m.extension, m.name); setValue(""); }}
                  className="flex w-full items-center gap-3 px-3 py-2 text-start hover:bg-background disabled:opacity-50">
                  <Avatar name={m.name} status={m.status} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-medium">{m.name}</span>
                    <span className="block text-sm text-muted-foreground">Ext <span className="font-mono">{m.extension}</span> · {statusLabel[m.status]}</span>
                  </span>
                  <Phone aria-hidden="true" className="size-4 text-call" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </form>
      <div className="mt-8">
        <Keypad onKey={(k) => setValue((v) => v + k)} />
      </div>
      <button type="button" onClick={() => dial()} disabled={!canCall || !value.trim()} aria-label="Call"
        className="mt-8 inline-flex size-18 items-center justify-center rounded-full bg-call text-call-foreground shadow-md transition-colors hover:bg-call/90 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40">
        <Phone className="size-7" />
      </button>

      <section className="mt-10 w-full" aria-labelledby="recent-title">
        <h2 id="recent-title" className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">Recent</h2>
        {recent.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">Calls you make or get while this page is open show up here.</p>
        ) : (
          <ul className="mt-2 divide-y rounded-md border bg-card">
            {recent.map((r) => {
              const Icon = recentIcon[r.kind];
              const who = members?.find((m) => m.extension === r.peer.number);
              return (
                <li key={r.id}>
                  <button type="button" disabled={!canCall} onClick={() => line.call(r.peer.number, r.peer.name)}
                    className="flex w-full items-center gap-3 px-3 py-2.5 text-start hover:bg-background disabled:opacity-60">
                    <Icon aria-hidden="true" className="size-4 text-muted-foreground" />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate font-medium">{r.peer.name}</span>
                      <span className="block text-sm text-muted-foreground">
                        {r.peer.name !== r.peer.number && <>Ext <span className="font-mono">{r.peer.number}</span> · </>}
                        {r.kind}, {recentWhen(r.at)}
                      </span>
                    </span>
                    {who && <StatusDot status={who.status} />}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        <p className="mt-3 text-xs text-muted-foreground">Nothing older is kept yet. Call history comes in a later update.</p>
      </section>
    </div>
  );
}
