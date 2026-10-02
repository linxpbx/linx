// System → Routing changes (docs/ui/SCREENS_PHASE1F.md §14.2, ADR-071):
// every change to where calls go, the last 50, newest first. See the
// change shows it before and after in the screens' own sentences; Put this
// back puts the routing back to just before it, after showing what that
// changes now, and is itself a new line (so it can be undone too).
import { useCallback, useEffect, useState } from "react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Guarded, SystemHeader } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { hasScope } from "@/lib/roles";
import { routingPutBack, useRoutingPutBack } from "@/lib/routingUndo";

type Change = components["schemas"]["RoutingChange"];
type ItemChange = components["schemas"]["RoutingItemChange"];
type Preview = components["schemas"]["RoutingPutBackPreview"];

function when(iso: string) {
  const d = new Date(iso);
  const t = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const today = new Date();
  if (d.toDateString() === today.toDateString()) return `Today ${t}`;
  if (d.toDateString() === new Date(today.getTime() - 86_400_000).toDateString()) return `Yesterday ${t}`;
  if (today.getTime() - d.getTime() < 6 * 86_400_000) return `${d.toLocaleDateString([], { weekday: "short" })} ${t}`;
  return `${d.toLocaleDateString([], { day: "numeric", month: "short" })} ${t}`;
}

function who(c: Change) {
  if (c.actor_name) return c.actor_name;
  const [type, id] = c.actor.split(":");
  if (type === "api_key") return "An API key";
  if (type === "oauth_client") return "An app";
  if (type === "system") return id === "cli" ? "On the server" : "Linx";
  return "A person";
}

/** The line's words: what it did, or which version it put back. */
function sentence(c: Change) {
  if (c.kind === "change") return c.summary;
  const from = c.put_back_at ? ` from ${when(c.put_back_at)}` : "";
  return c.kind === "undo" ? `Undid the change${from}` : `Put back the version${from}`;
}

/** Before and after, one routing item a block. */
function ItemChanges({ items, beforeLabel = "Before", afterLabel = "After" }: { items: ItemChange[]; beforeLabel?: string; afterLabel?: string }) {
  return (
    <ul className="flex flex-col gap-4">
      {items.map((c, i) => (
        <li key={i} className="text-sm">
          <p className="font-medium">{c.name}</p>
          <dl className="mt-1 grid grid-cols-[4.5rem_minmax(0,1fr)] gap-x-3 gap-y-1">
            <dt className="text-muted-foreground">{beforeLabel}</dt>
            <dd className={c.before ? "" : "text-muted-foreground"}>{c.before ?? "Not there"}</dd>
            <dt className="text-muted-foreground">{afterLabel}</dt>
            <dd className={c.after ? "" : "text-muted-foreground"}>{c.after ?? "Removed"}</dd>
          </dl>
        </li>
      ))}
    </ul>
  );
}

export function SystemRoutingChangesScreen({ me }: { me: Me }) {
  const [changes, setChanges] = useState<Change[] | null>(null);
  const [error, setError] = useState("");
  const [seeing, setSeeing] = useState<Change | null>(null);
  const [putting, setPutting] = useState<Change | null>(null);
  const canWrite = hasScope(me, "routing:write");

  const load = useCallback(async () => {
    const { data, error: err } = await api.GET("/api/v1/routing-changes");
    if (!data) { setError(problemMessage(err)); return; }
    setError("");
    setChanges(data.items);
  }, []);
  useEffect(() => { void load(); }, [load]);
  useRoutingPutBack(load);

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <SystemHeader current="/admin/system/routing-changes" systemAdmin={me.role === "system_admin"} />
      <p className="mt-6 text-sm text-muted-foreground">
        Every change to where calls go is kept (the last 50): numbers and lines, ring groups, office hours and holidays, and outgoing calls.
      </p>

      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}
      {changes === null && !error && <div className="p-6" aria-busy="true" />}
      {changes !== null && changes.length === 0 && (
        <p className="mt-6 rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          No changes yet. Each change to where calls go will be listed here, and can be put back.
        </p>
      )}
      {changes !== null && changes.length > 0 && (
        <ul className="mt-4 flex flex-col divide-y rounded-lg border" aria-label="Routing changes">
          {changes.map((c) => (
            <li key={c.id} className="grid grid-cols-[minmax(0,1fr)] gap-x-4 gap-y-1 p-3 text-sm md:grid-cols-[8.5rem_10rem_minmax(0,1fr)_auto] md:items-center">
              <span className="text-muted-foreground max-md:inline">{when(c.at)}<span className="md:hidden"> · {who(c)}</span></span>
              <span className="truncate max-md:hidden">{who(c)}</span>
              <span className="min-w-0">
                {sentence(c)}
                {c.kind !== "change" && c.changes && c.changes.length > 0 && (
                  <span className="block text-muted-foreground">{c.summary}</span>
                )}
              </span>
              <span className="flex flex-wrap gap-2 md:justify-end">
                <Button variant="outline" size="sm" disabled={!c.changes} onClick={() => setSeeing(c)}>See the change</Button>
                <Guarded allowed={canWrite} reason="Only an admin can change where calls go">
                  <Button variant="outline" size="sm" disabled={!canWrite} onClick={() => setPutting(c)}>Put this back</Button>
                </Guarded>
              </span>
            </li>
          ))}
        </ul>
      )}

      {seeing && (
        <Sheet open onOpenChange={(o) => { if (!o) setSeeing(null); }}>
          <SheetContent className="w-full sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{sentence(seeing)}</SheetTitle>
              <SheetDescription>{when(seeing.at)} · {who(seeing)}</SheetDescription>
            </SheetHeader>
            <div className="overflow-y-auto px-4 pb-4">
              <ItemChanges items={seeing.changes ?? []} />
            </div>
          </SheetContent>
        </Sheet>
      )}
      {putting && (
        <PutBackDialog me={me} change={putting} onClose={() => setPutting(null)}
          onDone={() => { setPutting(null); routingPutBack(); }} />
      )}
    </div>
  );
}

function PutBackDialog({ me, change, onClose, onDone }: { me: Me; change: Change; onClose: () => void; onDone: () => void }) {
  const [preview, setPreview] = useState<Preview | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const confirm = useConfirmIdentity(me);
  const outgoingAllowed = hasScope(me, "trunks:write");

  useEffect(() => {
    void api.GET("/api/v1/routing-changes/{id}/put-back", { params: { path: { id: change.id } } }).then(({ data, error: err }) => {
      if (data) setPreview(data); else setError(problemMessage(err));
    });
  }, [change.id]);

  const putBack = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/routing-changes/{id}/put-back", {
      params: { path: { id: change.id } }, body: {},
    });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    onDone();
    return { confirm: false };
  });

  const blocked = preview?.changes_outgoing && !outgoingAllowed;
  return (
    <>
      <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Put this back?</DialogTitle>
            <DialogDescription>
              Where calls go returns to how it was just before {when(change.at)}. You can undo this too.
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-[50dvh] overflow-y-auto">
            {preview === null && !error && <div className="p-6" aria-busy="true" />}
            {preview && preview.changes.length === 0 && (
              <p className="text-sm text-muted-foreground">Nothing would change: the routing is already like this.</p>
            )}
            {preview && preview.changes.length > 0 && <ItemChanges items={preview.changes} beforeLabel="Now" afterLabel="Then" />}
          </div>
          {preview?.needs_confirm && (
            <p className="text-sm">This turns calls abroad or premium-rate calls back on, so you'll be asked to confirm it's you.</p>
          )}
          {blocked && <p className="text-sm">This changes outgoing calls, which your account can't change.</p>}
          {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" onClick={onClose}>Cancel</Button>
            <Button disabled={!preview || preview.changes.length === 0 || busy || !!blocked} onClick={() => void putBack()}>
              Put it back
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {confirm.dialog}
    </>
  );
}
