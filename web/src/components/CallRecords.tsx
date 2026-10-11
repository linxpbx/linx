// What an iPhone or iPad saw of its recent calls (internal/callreport): the
// Linx app sends one after every call, kept seven days. For an admin
// working out why a call went wrong, without a cable to the phone or
// someone pasting text from its Call details screen.
import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";

type Device = components["schemas"]["Device"];
type CallReport = components["schemas"]["CallReport"];

const when = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" });

/** "Incoming from 201 · 11 Oct, 16:06:10 · 42 s", or "The app crashed · …". */
export function callReportTitle(r: CallReport): string {
  if (r.kind === "crash") return `The app crashed · ${when(r.started_at)}`;
  const way = r.direction === "incoming" ? `Incoming from ${r.peer || "a hidden number"}` : `Outgoing to ${r.peer}`;
  const secs = r.ended_at ? Math.round((new Date(r.ended_at).getTime() - new Date(r.started_at).getTime()) / 1000) : null;
  return [way, when(r.started_at), secs === null ? null : `${secs} s`].filter(Boolean).join(" · ");
}

export function CallRecordsDialog({ device, onClose }: { device: Device | null; onClose: () => void }) {
  const [reports, setReports] = useState<CallReport[] | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  useEffect(() => {
    if (!device) return;
    let live = true;
    setReports(null);
    void (async () => {
      const { data } = await api.GET("/api/v1/devices/{id}/call-reports", { params: { path: { id: device.id } } });
      if (live) setReports(data?.items ?? []);
    })();
    return () => { live = false; };
  }, [device]);

  const copy = async (r: CallReport) => {
    try {
      await navigator.clipboard.writeText(r.report);
      setCopied(r.id);
    } catch {
      setCopied(null);
    }
  };

  return (
    <Dialog open={!!device} onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Call records: {device?.name}</DialogTitle>
          <DialogDescription>
            What this phone saw of each call it made or took in the last seven days, newest first. It sends one after
            every call: how the call was set up, the route it took and the sound in and out. Never the sound itself.
            If the app ever crashes, the report iOS makes of it is here too.
          </DialogDescription>
        </DialogHeader>
        <div className="flex max-h-[60vh] flex-col gap-2 overflow-y-auto">
          {reports === null && <p className="text-sm text-muted-foreground">Loading…</p>}
          {reports?.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No calls yet. A phone sends its record when a call ends, from Linx app build 19 on.
            </p>
          )}
          {reports?.map((r) => (
            <details key={r.id} className="rounded-md border p-2.5 text-sm">
              <summary className={`cursor-pointer font-medium ${r.kind === "crash" ? "text-destructive" : ""}`}>
                {callReportTitle(r)}
              </summary>
              <pre className="mt-2 whitespace-pre-wrap break-words rounded bg-muted p-2 font-mono text-xs">{r.report}</pre>
              <div className="mt-2 flex items-center gap-2">
                <Button size="sm" variant="outline" onClick={() => void copy(r)}>Copy</Button>
                {copied === r.id && <span className="text-xs text-muted-foreground" role="status">Copied.</span>}
              </div>
            </details>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
