// The Team list (WEB_SCREENS_PHASE1C.md §6): one flat list by name, with
// live status.
import { Phone } from "lucide-react";
import type { TeamMember } from "@/api/client";
import { Avatar, statusLabel, useElapsed } from "@/components/presence";
import { usePhoneLine, usePhoneState } from "@/phone/context";

function StatusText({ m }: { m: TeamMember }) {
  const since = m.since ? new Date(m.since).getTime() : undefined;
  const elapsed = useElapsed(m.status === "on_call" ? since : undefined);
  return <>{statusLabel[m.status]}{elapsed && <span className="font-mono"> · {elapsed}</span>}</>;
}

export function TeamScreen({ members, query }: { members: TeamMember[] | null; query: string }) {
  const line = usePhoneLine();
  const { status, call, extension } = usePhoneState();
  const q = query.trim().toLowerCase();
  const shown = (members ?? []).filter((m) => !q || m.name.toLowerCase().includes(q) || m.extension.startsWith(q));
  const reachable = (members ?? []).filter((m) => m.status !== "offline").length;
  const canCall = status === "ready" && !call;

  return (
    <div className="w-full max-w-6xl px-4 py-6 md:px-6">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h1 className="font-display text-3xl font-semibold tracking-tight">Team</h1>
        {members && (
          <p className="text-muted-foreground">
            {members.length} {members.length === 1 ? "person" : "people"} · {reachable} reachable
          </p>
        )}
      </div>
      <p className="mt-1 text-sm text-muted-foreground">Grouping and favourites are coming.</p>

      <div className="mt-5 overflow-hidden rounded-lg border bg-card">
        <table className="w-full table-fixed text-start">
          <thead>
            <tr className="border-b text-xs font-semibold tracking-wider text-muted-foreground uppercase">
              <th scope="col" className="px-4 py-3 text-start md:px-5">Name</th>
              <th scope="col" className="w-20 px-2 py-3 text-start md:w-28">Ext</th>
              <th scope="col" className="hidden w-56 px-2 py-3 text-start sm:table-cell">Status</th>
              <th scope="col" className="w-14 py-3"><span className="sr-only">Call</span></th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {members === null &&
              [0, 1, 2, 3].map((i) => (
                <tr key={i} aria-hidden="true">
                  <td className="px-4 py-3 md:px-5" colSpan={4}>
                    <div className="flex items-center gap-3">
                      <div className="size-9 animate-pulse rounded-full bg-background" />
                      <div className="h-4 w-40 animate-pulse rounded bg-background" />
                    </div>
                  </td>
                </tr>
              ))}
            {members !== null && members.length <= 1 && !q && (
              <tr>
                <td colSpan={4} className="px-5 py-10 text-center text-muted-foreground">Nobody else has an account yet.</td>
              </tr>
            )}
            {members !== null && shown.length === 0 && q && (
              <tr>
                <td colSpan={4} className="px-5 py-10 text-center text-muted-foreground">Nobody matches “{query}”.</td>
              </tr>
            )}
            {shown.map((m) => {
              const self = m.extension === extension;
              return (
                <tr key={m.extension} data-testid={`team-${m.extension}`} data-status={m.status}
                  className={self ? "" : "hover:bg-background/60"}>
                  <td className="px-4 py-2.5 md:px-5">
                    <div className="flex items-center gap-3">
                      <Avatar name={m.name} status={m.status} />
                      <div className="min-w-0">
                        <p className="truncate font-medium">{m.name}{self && <span className="font-normal text-muted-foreground"> (you)</span>}</p>
                        <p className="truncate text-sm text-muted-foreground sm:hidden"><StatusText m={m} /></p>
                      </div>
                    </div>
                  </td>
                  <td className="px-2 py-2.5 font-mono">{m.extension}</td>
                  <td className="hidden px-2 py-2.5 text-muted-foreground sm:table-cell"><StatusText m={m} /></td>
                  <td className="py-2.5 pe-3 text-end">
                    {!self && (
                      <button type="button" aria-label={`Call ${m.name}`} disabled={!canCall}
                        onClick={() => line.call(m.extension, m.name)}
                        className="inline-flex size-9 items-center justify-center rounded-full text-call hover:bg-call/10 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40">
                        <Phone className="size-5" />
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
