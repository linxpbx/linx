// System → Status (docs/ui/ADMIN_SCREENS_PHASE1E.md §10.1, docs/ADMIN.md §9):
// every Linx service's state, its recent log and Restart, the certificate,
// phone lines, WireGuard connections and open alerts. Services, logs and
// Restart come from the server helper (linx-ops-agent), which only knows a
// fixed list of Linx services; checks only the server itself can run stay
// in `sudo linx doctor`.
import { useCallback, useEffect, useRef, useState } from "react";
import { ChevronDown, Copy, LoaderCircle, RefreshCw, RotateCw, TriangleAlert } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { CheckIt } from "@/components/CheckIt";
import { Dot } from "@/components/presence";
import { Guarded, SystemCard, SystemHeader } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { SystemStatus } from "@/hooks/useSystemStatus";
import { trunkDot, trunkWords, tunnelDot, tunnelWords, type Tone } from "@/lib/lines";
import { hasScope } from "@/lib/roles";
import { cn } from "@/lib/utils";

type ServiceHealth = components["schemas"]["ServiceHealth"];
type LogLine = components["schemas"]["ServiceLog"]["lines"][number];

const REFRESH_MS = 10_000;
const DAY = 86_400_000;

const stateTone: Record<ServiceHealth["state"], Tone> = {
  running: "good", starting: "warn", restarting: "warn", unhealthy: "bad", stopped: "bad", missing: "bad",
};
const stateWords: Record<ServiceHealth["state"], string> = {
  running: "Running", starting: "Starting…", restarting: "Restarting…", unhealthy: "Running, not answering",
  stopped: "Stopped", missing: "Not installed",
};

/** What restarting each service does, said before it happens. */
const restartEffect: Record<string, string> = {
  "control-plane": "This page and everyone's browser phones disconnect for a few seconds, then reconnect by themselves. Calls between desk phones carry on.",
  asterisk: "Calls in progress end. Phones and browsers reconnect by themselves within a minute.",
  coturn: "Browser calls from outside your network may drop and reconnect. Other calls carry on.",
  certd: "Nothing you'd notice: it only renews the certificate.",
  wireguard: "Phone lines through WireGuard drop for a few seconds and calls on them end.",
  sni: "Everyone reaching Linx from outside is cut off for a few seconds.",
};

const restartTitle: Record<string, string> = {
  "control-plane": "Restart the web and API service?", asterisk: "Restart the phone system?",
  coturn: "Restart the calls-from-outside relay?", certd: "Restart the certificate service?",
  wireguard: "Restart the WireGuard connections?", sni: "Restart the front door?",
};

const time = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const when = (iso: string) => {
  const d = new Date(iso);
  return Date.now() - d.getTime() < DAY
    ? d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
};
function ago(iso: string | undefined, now: number): string {
  if (!iso) return "never";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return when(iso);
}

export function SystemStatusScreen({ me }: { me: Me }) {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(false);
  const [now, setNow] = useState(Date.now());

  const load = useCallback(async (check = false) => {
    const { data, error: err } = await api.GET("/api/v1/system/status", { params: { query: check ? { check: true } : {} } });
    if (data) { setStatus(data); setError(""); }
    else if (err) setError(problemMessage(err));
    else setError("The server didn't answer. It may be restarting.");
    setNow(Date.now());
  }, []);
  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), REFRESH_MS);
    const tick = window.setInterval(() => setNow(Date.now()), 1000);
    return () => { window.clearInterval(t); window.clearInterval(tick); };
  }, [load]);

  const checkNow = async () => { setChecking(true); await load(true); setChecking(false); };

  if (!status) {
    return (
      <div className="w-full max-w-5xl px-4 py-6 md:px-6" aria-busy={!error}>
        <SystemHeader current="/admin/system/status" systemAdmin={me.role === "system_admin"} />
        {error && <p role="alert" className="mt-6 text-sm font-medium text-destructive">{error}</p>}
      </div>
    );
  }

  const services = status.containers.filter((c) => !(c.optional && c.state === "missing"));
  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <SystemHeader current="/admin/system/status" systemAdmin={me.role === "system_admin"} />
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground" role="status">
          {status.helper.checked_at ? `Checked ${ago(status.helper.checked_at, now)}` : "Not checked yet"}
          {error && <span className="ms-2 text-destructive">{error}</span>}
        </p>
        <Button variant="outline" onClick={() => void checkNow()} disabled={checking}>
          <RefreshCw aria-hidden="true" className={cn(checking && "animate-spin")} />Check now
        </Button>
      </div>
      <div className="mt-4 flex flex-col gap-4">
        {!status.helper.connected && <HelperMissing />}
        <SystemCard title="Linx services">
          {services.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              The server helper hasn't reported yet, so the services can't be shown. The page answering at all means the
              web service and the database are working.
            </p>
          ) : (
            <ul className="-mx-2 flex flex-col">
              {services.map((s) => (
                <ServiceRow key={s.service} me={me} service={s} helper={status.helper.connected} onChanged={() => void load(true)} />
              ))}
            </ul>
          )}
        </SystemCard>
        <SystemCard title="Certificate">
          <Certificate expires={status.certificate?.expires_at} now={now} />
        </SystemCard>
        <SystemCard title="Reachable from outside">
          {hasScope(me, "system:write")
            ? <CheckIt />
            : <p className="text-sm text-muted-foreground">Only an admin can check it: it makes a link to open on a phone.</p>}
        </SystemCard>
        <SystemCard title="Phone lines">
          {status.trunks.length === 0 ? (
            <p className="text-sm text-muted-foreground">No phone lines yet.</p>
          ) : (
            <ul className="flex flex-col gap-2 text-sm">
              {status.trunks.map((t) => (
                <li key={t.id} className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                  <Dot tone={trunkDot[t.status] ?? "neutral"} />
                  <span className="font-medium">{t.name}</span>
                  <span className="text-muted-foreground">
                    {trunkWords[t.status] ?? t.status}
                    {t.status_since && trunkDot[t.status] === "bad" && ` since ${when(t.status_since)}`}
                  </span>
                </li>
              ))}
            </ul>
          )}
          {status.wireguard_profiles.length > 0 && (
            <>
              <h3 className="mt-4 text-sm font-medium">Connections (WireGuard)</h3>
              <ul className="mt-2 flex flex-col gap-2 text-sm">
                {status.wireguard_profiles.map((w) => (
                  <li key={w.id} className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                    <Dot tone={tunnelDot[w.status] ?? "neutral"} />
                    <span className="font-medium">{w.name}</span>
                    <span className="text-muted-foreground">{tunnelWords[w.status] ?? w.status}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </SystemCard>
        {status.open_alerts.length > 0 && (
          <SystemCard title="Needs attention">
            <ul className="flex flex-col gap-3 text-sm">
              {status.open_alerts.map((a, i) => (
                <li key={i} className="flex items-start gap-2.5">
                  <Dot tone={a.severity === "critical" ? "bad" : "warn"} className="mt-1.5" />
                  <span className="min-w-0 [overflow-wrap:anywhere]">
                    <span className="font-medium">{a.title}</span>
                    <span className="text-muted-foreground"> · since {when(a.since)}</span>
                    <span className="block text-muted-foreground">{a.message}</span>
                  </span>
                </li>
              ))}
            </ul>
          </SystemCard>
        )}
        <p className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          Some checks can only run on the server itself (the firewall, start-up services, DNS). Run this there:
          <CopyCommand command="sudo linx doctor" />
        </p>
      </div>
    </div>
  );
}

function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className="inline-flex items-center gap-1">
      <code className="rounded bg-background px-1.5 py-0.5 font-mono">{command}</code>
      <Button size="sm" variant="ghost" onClick={() => { void navigator.clipboard?.writeText(command); setCopied(true); }}>
        <Copy aria-hidden="true" />{copied ? "Copied" : "Copy"}
      </Button>
    </span>
  );
}

function HelperMissing() {
  return (
    <div role="alert" className="rounded-lg border-2 border-status-away bg-card p-4 text-sm">
      <p className="flex items-center gap-2 font-medium">
        <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-status-away" />
        The server helper isn't running
      </p>
      <p className="mt-1 text-muted-foreground">
        It's what lets this page see each service, read its log and restart it. Calls and everything else work
        without it. To put it back, run this on the server:
      </p>
      <p className="mt-2"><CopyCommand command="sudo linx setup" /></p>
    </div>
  );
}

function Certificate({ expires, now }: { expires?: string; now: number }) {
  if (!expires) {
    return <p className="flex items-center gap-2.5 text-sm"><Dot tone="neutral" />No certificate yet, or it hasn't been read yet.</p>;
  }
  const days = Math.floor((new Date(expires).getTime() - now) / DAY);
  const tone: Tone = days < 7 ? "bad" : days < 21 ? "warn" : "good";
  const date = new Date(expires).toLocaleDateString(undefined, { day: "numeric", month: "long", year: "numeric" });
  return (
    <div className="flex items-start gap-2.5 text-sm">
      <Dot tone={tone} className="mt-1.5" />
      <span>
        Valid until {date} ({days} days).{" "}
        <span className="text-muted-foreground">
          {tone === "good"
            ? "It renews by itself 30 days before."
            : "It should have renewed by now: look at the Certificates service's log below."}
        </span>
      </span>
    </div>
  );
}

// --- One service: its state, and opened, its log and Restart ---

function ServiceRow({ me, service, helper, onChanged }: {
  me: Me; service: ServiceHealth; helper: boolean; onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const bodyId = `service-${service.service}`;
  return (
    <li className="border-b last:border-b-0">
      <button type="button" aria-expanded={open} aria-controls={bodyId} onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2.5 rounded-md px-2 py-2.5 text-start text-sm hover:bg-muted">
        <Dot tone={stateTone[service.state]} label={stateWords[service.state]} />
        <span className="min-w-0 flex-1">
          <span className="font-medium">{service.label}</span>
          <span className="block text-muted-foreground sm:ms-2 sm:inline">
            {stateWords[service.state]}
            {service.started_at && service.state === "running" && ` since ${when(service.started_at)}`}
            {service.restarts > 0 && ` · restarted by itself ${service.restarts === 1 ? "once" : `${service.restarts} times`}`}
          </span>
        </span>
        <ChevronDown aria-hidden="true" className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")} />
      </button>
      {open && (
        <div id={bodyId} className="px-2 pb-4">
          <ServiceDetail me={me} service={service} helper={helper} onChanged={onChanged} />
        </div>
      )}
    </li>
  );
}

function ServiceDetail({ me, service, helper, onChanged }: {
  me: Me; service: ServiceHealth; helper: boolean; onChanged: () => void;
}) {
  const canLogs = hasScope(me, "logs:read");
  const canRestart = hasScope(me, "system:write");
  const [lines, setLines] = useState<LogLine[] | null>(null);
  const [logError, setLogError] = useState("");
  const [loading, setLoading] = useState(false);
  const [copied, setCopied] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  const loadLog = useCallback(async () => {
    setLoading(true);
    setLogError("");
    const { data, error } = await api.GET("/api/v1/system/services/{service}/log", {
      params: { path: { service: service.service }, query: { lines: 200 } },
    });
    setLoading(false);
    if (data) setLines(data.lines);
    else setLogError(problemMessage(error));
  }, [service.service]);
  useEffect(() => { if (canLogs && helper) void loadLog(); }, [canLogs, helper, loadLog]);
  // The newest lines are at the bottom: start there.
  useEffect(() => { if (box.current) box.current.scrollTop = box.current.scrollHeight; }, [lines]);

  const text = (lines ?? []).map((l) => (l.time ? `${l.time} ${l.text}` : l.text)).join("\n");
  const logsReason = !helper ? "The server helper isn't running" : "Only an admin can read logs";

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-medium">Recent log</h3>
        <div className="flex flex-wrap gap-2">
          <Guarded allowed={canLogs && helper} reason={logsReason}>
            <Button size="sm" variant="ghost" onClick={() => void loadLog()} disabled={!canLogs || !helper || loading}>
              <RefreshCw aria-hidden="true" className={cn(loading && "animate-spin")} />Refresh
            </Button>
          </Guarded>
          {lines && lines.length > 0 && (
            <Button size="sm" variant="ghost" onClick={() => { void navigator.clipboard?.writeText(text); setCopied(true); }}>
              <Copy aria-hidden="true" />{copied ? "Copied" : "Copy"}
            </Button>
          )}
          <RestartButton service={service} helper={helper} canRestart={canRestart} onRestarted={onChanged} />
        </div>
      </div>
      {!canLogs || !helper ? (
        <p className="text-sm text-muted-foreground">{logsReason}.</p>
      ) : logError ? (
        <p role="alert" className="text-sm font-medium text-destructive">{logError}</p>
      ) : !lines ? (
        <p className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
          <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Reading the log…
        </p>
      ) : lines.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing in the log yet.</p>
      ) : (
        <div ref={box} role="log" aria-label={`${service.label} log`} tabIndex={0}
          className="max-h-80 overflow-y-auto rounded-md border bg-background p-3 font-mono text-xs leading-relaxed">
          {lines.map((l, i) => (
            <div key={i} className="whitespace-pre-wrap [overflow-wrap:anywhere]">
              {l.time && <span className="me-2 text-muted-foreground">{time(l.time)}</span>}
              {l.text}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function RestartButton({ service, helper, canRestart, onRestarted }: {
  service: ServiceHealth; helper: boolean; canRestart: boolean; onRestarted: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ text: string; error?: boolean } | null>(null);

  const reason = !service.can_restart
    ? "This one can't be restarted from here. If it's stopped, run sudo linx doctor on the server."
    : !canRestart ? "Only an admin can restart services"
    : !helper ? "The server helper isn't running" : "";

  const restart = async () => {
    setConfirming(false);
    setBusy(true);
    setNote(null);
    const { data, error } = await api.POST("/api/v1/system/services/{service}/restart", {
      params: { path: { service: service.service } },
    });
    if (data?.result === "restarting") {
      // This page's own server: wait until it answers again.
      setNote({ text: "Restarting… this page reconnects by itself." });
      for (let i = 0; i < 60; i++) {
        await new Promise((r) => window.setTimeout(r, 2000));
        const { data: back } = await api.GET("/api/v1/system/status");
        if (back) break;
      }
      setBusy(false);
      setNote({ text: "Restarted." });
      onRestarted();
      return;
    }
    setBusy(false);
    if (!data) { setNote({ text: problemMessage(error), error: true }); return; }
    setNote({ text: "Restarted." });
    onRestarted();
  };

  return (
    <>
      <Guarded allowed={!reason} reason={reason}>
        <Button size="sm" variant="outline" onClick={() => setConfirming(true)} disabled={!!reason || busy}>
          {busy ? <LoaderCircle aria-hidden="true" className="animate-spin" /> : <RotateCw aria-hidden="true" />}
          {busy ? "Restarting…" : "Restart"}
        </Button>
      </Guarded>
      {note && (
        <span role={note.error ? "alert" : "status"}
          className={cn("basis-full text-sm", note.error ? "font-medium text-destructive" : "text-muted-foreground")}>
          {note.text}
        </span>
      )}
      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{restartTitle[service.service] ?? `Restart ${service.label}?`}</DialogTitle>
            <DialogDescription>
              {restartEffect[service.service] ?? "It's back within a minute."} It takes about half a minute.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirming(false)}>Cancel</Button>
            <Button onClick={() => void restart()}>Restart</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
