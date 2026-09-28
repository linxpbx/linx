// "Moved to a new place?" (docs/INSTALL.md §8, docs/ui/INSTALL_SCREENS.md
// §6): after a restore onto another server, what in the backup may still
// point at the old place. Only the rows that apply; ticks are saved on the
// server for every admin; the backups row ticks itself. "Hide for now"
// folds it to one line for a week; it goes away when everything's done.
import { useCallback, useEffect, useState } from "react";
import { Truck } from "lucide-react";
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { navigate } from "@/hooks/useRoute";

type Checklist = components["schemas"]["MovedChecklist"];
type Place = components["schemas"]["InstallPlace"];
type Link = NonNullable<Checklist["items"][number]["link"]>;

// Pages that exist today; Phone lines and Settings come with later
// sessions, so those rows say what to do without a button.
const LINKS: Partial<Record<Link, { label: string; path: string }>> = {
  extensions: { label: "Extensions", path: "/admin/extensions" },
  account: { label: "My account", path: "/account" },
  backups: { label: "Backups", path: "/admin/system/backups" },
};

function describe(p: Place): string {
  const where = p.lan_networks.length > 0 ? `home ${p.lan_networks.join(", ")}` : `rented server${p.public_address ? ` ${p.public_address}` : ""}`;
  return p.domain ? `${where}, ${p.domain}` : where;
}

export function MovedChecklist({ canWrite }: { canWrite: boolean }) {
  const [c, setC] = useState<Checklist | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data } = await api.GET("/api/v1/moved-checklist");
    if (data) setC(data.checklist ?? null);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const change = async (body: { ticks?: Record<string, boolean>; hide?: boolean }) => {
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/moved-checklist", { body });
    if (data) setC(data.checklist ?? null);
    else setError(problemMessage(err));
  };
  if (!c) return null;
  const done = c.items.filter((i) => i.done).length;
  const left = c.items.length - done;

  if (c.hidden_until) {
    return (
      <section aria-label="Moved to a new place?" className="flex flex-wrap items-center gap-3 rounded-lg border bg-card px-5 py-3 text-sm">
        <Truck aria-hidden="true" className="size-4 shrink-0 text-status-away" />
        <span className="flex-1">Moved to a new place? {left} left</span>
        {canWrite && <Button size="sm" variant="outline" onClick={() => void change({ hide: false })}>Show</Button>}
      </section>
    );
  }
  return (
    <section aria-labelledby="moved-title" className="rounded-lg border border-status-away bg-card p-5 md:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="moved-title" className="flex items-center gap-2 font-display text-lg font-semibold">
          <Truck aria-hidden="true" className="size-5 shrink-0 text-status-away" />Moved to a new place?
        </h2>
        <span className="text-sm text-muted-foreground">{done} of {c.items.length} done</span>
      </div>
      <div className="mt-2 text-sm">
        <p>This server was restored from a backup made somewhere else:</p>
        <dl className="mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 text-muted-foreground">
          <dt>before:</dt><dd className="break-words">{describe(c.before)}</dd>
          <dt>now:</dt><dd className="break-words">{describe(c.after)}</dd>
        </dl>
      </div>
      <ul className="mt-4 flex flex-col gap-3">
        {c.items.map((it) => {
          const link = it.link ? LINKS[it.link] : undefined;
          const id = `moved-${it.id}`;
          return (
            <li key={it.id} className="flex min-w-0 items-start gap-3 text-sm">
              <Checkbox id={id} checked={it.done} disabled={!canWrite || !!it.auto} className="mt-0.5"
                onCheckedChange={(v) => void change({ ticks: { [it.id]: v === true } })} />
              <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row sm:items-start">
                <div className="min-w-0 flex-1">
                  <Label htmlFor={id} className={it.done ? "font-normal text-muted-foreground line-through" : "font-medium"}>{it.title}</Label>
                  <p className="mt-0.5 break-words text-muted-foreground">{it.why}</p>
                </div>
                {link && !it.done && (
                  <Button size="sm" variant="outline" className="w-fit" onClick={() => navigate(link.path)}>{link.label}</Button>
                )}
              </div>
            </li>
          );
        })}
      </ul>
      {error && <p role="alert" className="mt-3 text-sm font-medium">{error}</p>}
      {canWrite && (
        <div className="mt-4 flex justify-end">
          <Button variant="outline" size="sm" onClick={() => void change({ hide: true })}>Hide for now</Button>
        </div>
      )}
    </section>
  );
}
