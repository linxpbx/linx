// Admin → Calls → Call quality (internal/callquality): how each phone's
// leg of each recent call went, judged by Linx from what the phone system
// measured, beside what the phone itself said of the same call. For an
// admin to see calls that went wrong even when nobody reported them
// (owner, 2026-10-16).
import { useEffect, useMemo, useState } from "react";
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { dayHeading, timeOf } from "@/lib/calls";
import { cn } from "@/lib/utils";

type Quality = components["schemas"]["CallQuality"];
type CallReport = components["schemas"]["CallReport"];

const VERDICT: Record<Quality["verdict"], { word: string; colour: string }> = {
  good: { word: "Good", colour: "text-status-available" },
  problems: { word: "Had problems", colour: "text-status-away" },
  bad: { word: "Bad", colour: "text-destructive" },
};

function when(iso: string): string {
  const day = dayHeading(iso);
  return day === "Today" ? timeOf(iso) : `${day} ${timeOf(iso)}`;
}

/** "Bad" or "Had problems" from the phone's "verdict:" line, else what it said. */
function phoneSays(q: Quality): string {
  if (!q.phone_verdict) return q.kind === "wake" ? "—" : "No report";
  return q.phone_verdict;
}

function phoneColour(q: Quality): string {
  const v = q.phone_verdict ?? "";
  if (v.startsWith("Bad")) return VERDICT.bad.colour;
  if (v.startsWith("Had problems")) return VERDICT.problems.colour;
  if (v.startsWith("Good")) return VERDICT.good.colour;
  return "text-muted-foreground";
}

export function CallQualityView() {
  const [items, setItems] = useState<Quality[] | null>(null);
  const [error, setError] = useState("");
  const [wrongOnly, setWrongOnly] = useState(true);
  const [selected, setSelected] = useState<Quality | null>(null);

  useEffect(() => {
    void (async () => {
      const { data, error: err } = await api.GET("/api/v1/call-quality");
      if (!data) { setError(problemMessage(err, "Linx couldn't load how calls went.")); return; }
      setItems(data.items);
    })();
  }, []);

  const shown = useMemo(
    () => (items ?? []).filter((q) => !wrongOnly || q.verdict !== "good" || (!!q.phone_verdict && !q.phone_verdict.startsWith("Good"))),
    [items, wrongOnly],
  );

  return (
    <>
      <p className="mt-6 max-w-3xl text-sm text-muted-foreground">
        Every call each phone took part in over the last seven days, judged by Linx from the sound it carried: lost,
        uneven or delayed. Beside it, what the Linx app on that phone said of the same call. A phone that was woken for
        a call but never connected is here too.
      </p>
      <div className="mt-4 flex items-center gap-2">
        <Checkbox id="quality-wrong" checked={wrongOnly} onCheckedChange={(v) => setWrongOnly(v === true)} />
        <Label htmlFor="quality-wrong" className="font-normal">Only calls that went wrong</Label>
      </div>

      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}
      {items === null && !error && <div className="mt-6 h-40" aria-busy="true" />}
      {items !== null && shown.length === 0 && (
        <p className="mt-6 rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {wrongOnly ? "No call went wrong in the last seven days." : "No calls in the last seven days."}
        </p>
      )}
      {shown.length > 0 && (
        <div className="mt-4 rounded-lg border">
          <div aria-hidden="true" className="hidden grid-cols-[7.5rem_minmax(0,1fr)_minmax(0,1.4fr)_minmax(0,1fr)] gap-x-4 border-b px-3 py-2 text-xs font-medium tracking-wide text-muted-foreground md:grid">
            <span>WHEN</span><span>PHONE</span><span>LINX SAW</span><span>THE PHONE SAID</span>
          </div>
          <ul className="flex flex-col divide-y" aria-label="How calls went">
            {shown.map((q) => (
              <li key={q.id}>
                <button type="button" onClick={() => setSelected(q)} data-testid="quality-row"
                  className="grid w-full grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-0.5 px-3 py-2.5 text-start text-sm hover:bg-card md:grid-cols-[7.5rem_minmax(0,1fr)_minmax(0,1.4fr)_minmax(0,1fr)]">
                  <span className="text-muted-foreground max-md:order-2 max-md:text-end">{when(q.ended_at)}</span>
                  <span className="break-words max-md:order-1">
                    {q.device_name}<span className="text-muted-foreground"> · {q.person} ({q.extension})</span>
                  </span>
                  <span className="break-words max-md:order-3 max-md:col-span-2">
                    <span className={cn("font-medium", VERDICT[q.verdict].colour)}>{VERDICT[q.verdict].word}</span>
                    {q.reasons.length > 0 && <span className="text-muted-foreground">: {q.reasons.join("; ")}</span>}
                  </span>
                  <span className={cn("break-words max-md:order-4 max-md:col-span-2", phoneColour(q), q.kind === "wake" && "max-md:hidden")}>
                    <span className="md:hidden text-muted-foreground">The phone said: </span>{phoneSays(q)}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {selected && <QualitySheet q={selected} onClose={() => setSelected(null)} />}
    </>
  );
}

function QualitySheet({ q, onClose }: { q: Quality; onClose: () => void }) {
  const [report, setReport] = useState<CallReport | null | undefined>(q.phone_report_id ? undefined : null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!q.phone_report_id) return;
    let live = true;
    void (async () => {
      const { data } = await api.GET("/api/v1/devices/{id}/call-reports", { params: { path: { id: q.device_id } } });
      if (live) setReport(data?.items.find((r) => r.id === q.phone_report_id) ?? null);
    })();
    return () => { live = false; };
  }, [q]);

  const s = q.sound;
  const lines = q.kind === "wake" ? [] : [
    `From this phone to Linx: ${s.in_packets} packets of sound, ${s.in_lost.toFixed(1)}% lost`,
    `From Linx to this phone: ${s.out_packets} packets of sound, ${s.out_lost.toFixed(1)}% lost`,
    `How unevenly its sound arrived: ${Math.round(s.jitter_ms)} ms`,
    `Round trip: ${Math.round(s.rtt_ms)} ms`,
  ];

  return (
    <Sheet open onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle className="break-words">{q.device_name} · {when(q.ended_at)}</SheetTitle>
          <SheetDescription>
            <span className={cn("font-medium", VERDICT[q.verdict].colour)}>{VERDICT[q.verdict].word}</span>
            {q.reasons.length > 0 && `: ${q.reasons.join("; ")}`}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4 text-sm">
          {lines.length > 0 && (
            <section>
              <h2 className="font-medium">What Linx measured</h2>
              <ul className="mt-1 list-disc ps-5 text-muted-foreground">{lines.map((l) => <li key={l}>{l}</li>)}</ul>
            </section>
          )}
          <section>
            <h2 className="font-medium">What the phone said</h2>
            {report === undefined && <p className="mt-1 text-muted-foreground">Loading…</p>}
            {report === null && (
              <p className="mt-1 text-muted-foreground">
                {q.kind === "wake" ? "It never connected, so it had nothing to say." : "It sent no report of this call."}
              </p>
            )}
            {report && (
              <>
                <pre className="mt-1 whitespace-pre-wrap break-words rounded bg-muted p-2 font-mono text-xs">{report.report}</pre>
                <div className="mt-2 flex items-center gap-2">
                  <Button size="sm" variant="outline" onClick={() => {
                    void navigator.clipboard.writeText(report.report).then(() => setCopied(true), () => setCopied(false));
                  }}>Copy</Button>
                  {copied && <span className="text-xs text-muted-foreground" role="status">Copied.</span>}
                </div>
              </>
            )}
          </section>
        </div>
      </SheetContent>
    </Sheet>
  );
}
