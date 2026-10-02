// "Saved. Undo" for call routing (docs/ui/SCREENS_PHASE1F.md §14.1,
// ADR-071): every request that changes routing answers with the change's
// id in a header; the toast (components/UndoToast.tsx) offers to put the
// routing back as it was just before. Screens showing routing reload when
// it's put back.
import { useEffect } from "react";
import type { Middleware } from "openapi-fetch";

export const ROUTING_CHANGE_HEADER = "Linx-Routing-Change";
const ROUTING_CHANGED_EVENT = "linx:routing-changed";

type Listener = (change: string) => void;
const listeners = new Set<Listener>();

/** Calls listener with each routing change this page makes; returns its removal. */
export function onRoutingSaved(listener: Listener): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

/** Routing changes and put-backs made from Routing changes announce themselves there. */
const OWN_PAGE = /\/api\/v1\/routing-changes\//;

export const routingUndo: Middleware = {
  onResponse({ request, response }) {
    if (request.method === "GET" || !response.ok || OWN_PAGE.test(new URL(request.url).pathname)) return response;
    const change = response.headers.get(ROUTING_CHANGE_HEADER);
    if (change) for (const l of listeners) l(change);
    return response;
  },
};

/** Tells screens showing routing that it was put back. */
export function routingPutBack() {
  window.dispatchEvent(new Event(ROUTING_CHANGED_EVENT));
}

/** Runs reload when routing is put back (Undo, Redo, Put this back). */
export function useRoutingPutBack(reload: () => unknown) {
  useEffect(() => {
    const on = () => { void reload(); };
    window.addEventListener(ROUTING_CHANGED_EVENT, on);
    return () => window.removeEventListener(ROUTING_CHANGED_EVENT, on);
  }, [reload]);
}
