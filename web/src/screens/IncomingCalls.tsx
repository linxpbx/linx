// Incoming calls (docs/ui/ADMIN_SCREENS_PHASE1E.md §7): where each of your
// numbers rings, one dropdown per number, saved on change; and where a
// line's calls for none of its numbers ring (docs/SIMPLER.md §1.2).
import { useCallback, useEffect, useState } from "react";
import { FlaskConical, TriangleAlert } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { navigate } from "@/hooks/useRoute";
import { hasScope, isReadOnlyAdmin } from "@/lib/roles";
import { RingsSelect } from "./PhoneLines";

type Trunk = components["schemas"]["Trunk"];
type Did = components["schemas"]["Did"];
type Extension = components["schemas"]["Extension"];

function NobodyWarning() {
  return (
    <span className="flex items-center gap-1.5 text-sm text-status-away">
      <TriangleAlert aria-hidden="true" className="size-4 shrink-0" />
      Callers hear that the number isn't available.
    </span>
  );
}

export function IncomingCallsScreen({ me }: { me: Me }) {
  const [dids, setDids] = useState<Did[] | null>(null);
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [saved, setSaved] = useState("");
  const [error, setError] = useState("");
  const readOnly = isReadOnlyAdmin(me) || !hasScope(me, "routing:write");

  const load = useCallback(async () => {
    const [d, t, e] = await Promise.all([
      api.GET("/api/v1/inbound-routes", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/trunks", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/extensions", { params: { query: { limit: 200 } } }),
    ]);
    if (d.data) setDids(d.data.items);
    if (t.data) setTrunks(t.data.items);
    if (e.data) setExtensions(e.data.items);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const say = (what: string) => { setSaved(what); window.setTimeout(() => setSaved(""), 2500); };

  const setRings = async (d: Did, ext: string | undefined) => {
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/dids/{id}", {
      params: { path: { id: d.id }, header: { "If-Match": d.etag } }, body: { extension_id: ext ?? "" },
    });
    if (!data) { setError(problemMessage(err)); return; }
    setDids((list) => list?.map((x) => (x.id === d.id ? data : x)) ?? list);
    say(`Saved: ${d.number}`);
  };
  const setLineRings = async (t: Trunk, ext: string | undefined) => {
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/trunks/{id}", {
      params: { path: { id: t.id }, header: { "If-Match": t.etag } }, body: { rings_extension_id: ext ?? "" },
    });
    if (!data) { setError(problemMessage(err)); return; }
    setTrunks((list) => list.map((x) => (x.id === t.id ? data : x)));
    say(`Saved: ${t.name}`);
  };

  if (dids === null) return <div className="p-6" aria-busy="true" />;
  const lineName = (id: string) => trunks.find((t) => t.id === id)?.name ?? "";
  // Lines that take calls for other numbers: phone systems, where it matters most, and any line that has one set.
  const fallbacks = trunks.filter((t) => t.kind === "registers_here" || t.kind === "lan_peer" || t.rings_extension_id);

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">Incoming calls</h1>
      <p className="mt-1 text-sm text-muted-foreground">When someone calls one of your numbers, it rings here.</p>
      <p className="mt-2 min-h-5 text-sm text-status-available" role="status">{saved}</p>

      {dids.length === 0 ? (
        <div className="mt-2 flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
          <p className="max-w-prose text-sm text-muted-foreground">
            No phone numbers yet. Numbers come with a phone line: add them on the line.
          </p>
          <Button variant="outline" onClick={() => navigate("/admin/lines")}>Phone lines</Button>
        </div>
      ) : (
        <ul className="mt-2 flex flex-col divide-y rounded-lg border" aria-label="Your numbers">
          {dids.map((d) => (
            <li key={d.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 p-3">
              <span className="flex min-w-48 grow basis-48 flex-col">
                <span className="font-mono text-sm font-medium">{d.number}</span>
                <span className="text-sm text-muted-foreground">{lineName(d.trunk_id)}{d.label ? ` · ${d.label}` : ""}</span>
              </span>
              <span className="flex min-w-0 items-start gap-2 max-sm:w-full">
                <span className="flex min-w-0 flex-1 flex-col gap-1">
                  <RingsSelect id={`in-${d.id}`} label={`${d.number} rings`} value={d.extension_id} extensions={extensions}
                    disabled={readOnly} onChange={(v) => void setRings(d, v)} />
                  {!d.extension_id && <NobodyWarning />}
                </span>
                <Button variant="ghost" size="icon" aria-label={`Try a call to ${d.number}`}
                  onClick={() => navigate(`/admin/simulator?direction=in&number=${encodeURIComponent(d.number)}`)}>
                  <FlaskConical aria-hidden="true" className="size-4" />
                </Button>
              </span>
            </li>
          ))}
        </ul>
      )}

      {fallbacks.length > 0 && (
        <section className="mt-8">
          <h2 className="text-lg font-semibold">Calls for any other number</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            A call that comes in on a line with none of its numbers (a landline often sends none) rings here.
          </p>
          <ul className="mt-3 flex flex-col divide-y rounded-lg border" aria-label="Lines' other calls">
            {fallbacks.map((t) => (
              <li key={t.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 p-3">
                <span className="min-w-48 grow basis-48 text-sm font-medium">{t.name}</span>
                <span className="flex min-w-0 flex-col gap-1 max-sm:w-full">
                  <RingsSelect id={`in-line-${t.id}`} label={`Other calls on ${t.name} ring`} value={t.rings_extension_id}
                    extensions={extensions} disabled={readOnly} onChange={(v) => void setLineRings(t, v)} />
                  {!t.rings_extension_id && <NobodyWarning />}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}
      {readOnly && <p className="mt-4 text-sm text-muted-foreground">You can see where calls go; changing it needs an admin.</p>}
      <p className="mt-6 text-sm text-muted-foreground">Office hours, ring groups and "if nobody answers" are coming in the next update.</p>
    </div>
  );
}
