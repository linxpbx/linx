// System → Activity (docs/ui/ADMIN_SCREENS_PHASE1E.md §10.3): the audit log
// as plain sentences, filtered by who, what and when (GET /audit-log), a
// row opening its details. Never passwords or codes: the server never
// writes them.
import { useCallback, useEffect, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { SystemHeader } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ACTIVITY_KINDS, activitySentence } from "@/lib/activity";

type Entry = components["schemas"]["AuditLogEntry"];
type User = components["schemas"]["User"];

const PERIODS = [
  { value: "1", label: "Last 24 hours" }, { value: "7", label: "Last 7 days" }, { value: "30", label: "Last 30 days" }, { value: "all", label: "Any time" },
];
const ANYONE = "anyone";
const ANY = "any";

function when(iso: string) {
  const d = new Date(iso);
  const t = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const today = new Date();
  if (d.toDateString() === today.toDateString()) return t;
  if (d.toDateString() === new Date(today.getTime() - 86_400_000).toDateString()) return `Yesterday ${t}`;
  return `${d.toLocaleDateString([], { day: "numeric", month: "short" })} ${t}`;
}

export function SystemActivityScreen({ me }: { me: Me }) {
  const [users, setUsers] = useState<User[]>([]);
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [next, setNext] = useState<string | undefined>();
  const [actor, setActor] = useState(ANYONE);
  const [kind, setKind] = useState(ANY);
  const [period, setPeriod] = useState("7");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Entry | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    void api.GET("/api/v1/users", { params: { query: { limit: 200 } } }).then(({ data }) => { if (data) setUsers(data.items); });
  }, []);

  const fetchPage = useCallback(async (cursor?: string) => {
    setError("");
    const since = period === "all" ? undefined : new Date(Date.now() - Number(period) * 86_400_000).toISOString();
    const { data, error: err } = await api.GET("/api/v1/audit-log", {
      params: { query: {
        limit: 50, ...(cursor ? { cursor } : {}), ...(actor !== ANYONE ? { actor } : {}),
        ...(kind !== ANY ? { action: kind } : {}), ...(since ? { since } : {}),
      } },
    });
    if (!data) { setError(problemMessage(err)); return; }
    setEntries((list) => (cursor ? [...(list ?? []), ...data.items] : data.items));
    setNext(data.next_cursor);
  }, [actor, kind, period]);
  useEffect(() => { setEntries(null); void fetchPage(); }, [fetchPage]);

  const who = useCallback((a: string) => {
    const [type, id] = a.split(":");
    if (type === "user") return users.find((u) => u.id === id)?.name ?? "A person";
    if (type === "api_key") return "An API key";
    if (type === "oauth_client") return "An app";
    if (type === "system") return id === "cli" ? "On the server" : "Linx";
    return a ? a : "(unknown)";
  }, [users]);

  const q = query.trim().toLowerCase();
  const shown = useMemo(() => (entries ?? []).filter((e) => !q
    || activitySentence(e).toLowerCase().includes(q) || who(e.actor).toLowerCase().includes(q) || (e.ip ?? "").includes(q)),
  [entries, q, who]);

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <SystemHeader current="/admin/system/activity" systemAdmin={me.role === "system_admin"} />
      <div className="mt-6 flex flex-wrap items-end gap-3">
        <div className="relative w-full max-w-xs">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-9" aria-label="Search activity" />
        </div>
        <Select value={actor} onValueChange={setActor}>
          <SelectTrigger className="w-44" aria-label="Who"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value={ANYONE}>Anyone</SelectItem>
            <SelectItem value="system:cli">On the server</SelectItem>
            {users.map((u) => <SelectItem key={u.id} value={`user:${u.id}`}>{u.name}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={kind} onValueChange={setKind}>
          <SelectTrigger className="w-52" aria-label="What"><SelectValue /></SelectTrigger>
          <SelectContent>{ACTIVITY_KINDS.map((k) => <SelectItem key={k.label} value={k.value || ANY}>{k.label}</SelectItem>)}</SelectContent>
        </Select>
        <Select value={period} onValueChange={setPeriod}>
          <SelectTrigger className="w-40" aria-label="When"><SelectValue /></SelectTrigger>
          <SelectContent>{PERIODS.map((p) => <SelectItem key={p.value} value={p.value}>{p.label}</SelectItem>)}</SelectContent>
        </Select>
      </div>

      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}
      {entries === null && !error && <div className="p-6" aria-busy="true" />}
      {entries !== null && shown.length === 0 && (
        <p className="mt-6 rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">Nothing in this period.</p>
      )}
      {shown.length > 0 && (
        <ul className="mt-4 flex flex-col divide-y rounded-lg border" aria-label="Activity">
          {shown.map((e) => (
            <li key={e.id}>
              <button type="button" onClick={() => setSelected(e)}
                className="grid w-full grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-0.5 p-3 text-start text-sm hover:bg-card md:grid-cols-[8rem_12rem_minmax(0,1fr)]">
                <span className="text-muted-foreground">{when(e.at)}</span>
                <span className="truncate max-md:col-start-2 max-md:row-start-2 max-md:text-muted-foreground">{who(e.actor)}</span>
                <span className={e.result === "ok" ? "" : "text-status-away"}>{activitySentence(e)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {next && <div className="mt-3 flex justify-center"><Button variant="outline" onClick={() => void fetchPage(next)}>Show more</Button></div>}

      {selected && (
        <Sheet open onOpenChange={(o) => { if (!o) setSelected(null); }}>
          <SheetContent className="w-full sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{activitySentence(selected)}</SheetTitle>
              <SheetDescription>{new Date(selected.at).toLocaleString()} · {who(selected.actor)}</SheetDescription>
            </SheetHeader>
            <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-y-1.5 overflow-y-auto px-4 pb-4 text-sm">
              <dt className="text-muted-foreground">Event</dt><dd className="break-all font-mono">{selected.action}</dd>
              <dt className="text-muted-foreground">Result</dt><dd>{selected.result}</dd>
              <dt className="text-muted-foreground">Who</dt><dd className="break-all font-mono">{selected.actor}</dd>
              {selected.ip && <><dt className="text-muted-foreground">From</dt><dd className="font-mono">{selected.ip}</dd></>}
              {selected.target && <><dt className="text-muted-foreground">What</dt><dd className="break-all font-mono">{selected.target}</dd></>}
              {selected.detail && Object.keys(selected.detail).length > 0 && (
                <>
                  <dt className="text-muted-foreground">Details</dt>
                  <dd><pre className="whitespace-pre-wrap break-all rounded-md bg-card p-2 font-mono text-xs">{JSON.stringify(selected.detail, null, 2)}</pre></dd>
                </>
              )}
            </dl>
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}
