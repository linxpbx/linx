// "Saved. Undo" after every routing change (docs/ui/SCREENS_PHASE1F.md
// §14.1, ADR-071): for 10 seconds, Undo puts the routing back as it was
// just before, then "Put back. Redo". Only while that change is still the
// newest: someone else's later change is never undone with it (Routing
// changes can put any version back). Turning calls abroad or premium
// numbers back on asks to confirm it's you, as when changed directly.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Button } from "@/components/ui/button";
import { navigate } from "@/hooks/useRoute";
import { onRoutingSaved, routingPutBack } from "@/lib/routingUndo";

type Toast =
  | { kind: "saved" | "undone"; change: string }
  | { kind: "busy" }
  | { kind: "error"; message: string; changedSince: boolean };

const SHOWN_MS = 10_000;

export function UndoToast({ me }: { me: Me }) {
  const [toast, setToast] = useState<Toast | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const confirm = useConfirmIdentity(me);

  const show = useCallback((t: Toast | null) => {
    window.clearTimeout(timer.current);
    setToast(t);
    if (t && t.kind !== "busy") timer.current = window.setTimeout(() => setToast(null), SHOWN_MS);
  }, []);
  useEffect(() => onRoutingSaved((change) => show({ kind: "saved", change })), [show]);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const putBack = (change: string, then: "saved" | "undone") => confirm.run(async () => {
    show({ kind: "busy" });
    const { data, error } = await api.POST("/api/v1/routing-changes/{id}/put-back", {
      params: { path: { id: change } }, body: { undo: true },
    });
    if (needsConfirm(error)) { show(null); return { confirm: true }; }
    if (!data) {
      show({ kind: "error", message: problemMessage(error), changedSince: problemCode(error) === "routing_changed" });
      return { confirm: false };
    }
    routingPutBack();
    show({ kind: then, change: data.id });
    return { confirm: false };
  });

  return (
    <>
      {toast && (
        <div className="pointer-events-none fixed inset-x-0 bottom-4 z-50 flex justify-center px-4">
          <div role="status" aria-live="polite"
            className="pointer-events-auto flex max-w-md flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border bg-card px-4 py-2.5 text-sm text-card-foreground shadow-lg">
            {toast.kind === "saved" && (
              <>
                <span>Saved.</span>
                <Button variant="link" className="h-auto p-0" onClick={() => void putBack(toast.change, "undone")}>Undo</Button>
              </>
            )}
            {toast.kind === "undone" && (
              <>
                <span>Put back.</span>
                <Button variant="link" className="h-auto p-0" onClick={() => void putBack(toast.change, "saved")}>Redo</Button>
              </>
            )}
            {toast.kind === "busy" && <span aria-busy="true">Putting it back…</span>}
            {toast.kind === "error" && (
              <>
                <span className="min-w-0">{toast.message}</span>
                {toast.changedSince && (
                  <Button variant="link" className="h-auto p-0" onClick={() => { show(null); navigate("/admin/system/routing-changes"); }}>
                    Routing changes
                  </Button>
                )}
              </>
            )}
          </div>
        </div>
      )}
      {confirm.dialog}
    </>
  );
}
