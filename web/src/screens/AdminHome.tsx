// Admin home (docs/ui/ADMIN_SCREENS_PHASE1E.md §2): the getting-started
// checklist, open alerts, phone lines, who's on a call, and a system
// summary. Ticks come from real state, not from the wizard alone.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { CircleCheck, Phone, TriangleAlert } from "lucide-react";
import { api, type Me, type TeamMember } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { MovedChecklist } from "@/components/MovedChecklist";
import { Dot, StatusDot, statusLabel } from "@/components/presence";
import { navigate } from "@/hooks/useRoute";
import { trunkDot, trunkWords } from "@/lib/lines";
import { hasScope } from "@/lib/roles";
import type { SystemStatus } from "@/hooks/useSystemStatus";

type SetupProgress = components["schemas"]["SetupProgress"];
type Settings = components["schemas"]["Settings"];
type Did = components["schemas"]["Did"];
type ActiveCall = components["schemas"]["ActiveCall"];

const HIDE_KEY = "linx.admin.hideChecklist";

function readHidden(): boolean {
  try {
    return localStorage.getItem(HIDE_KEY) === "1";
  } catch {
    return false;
  }
}
function writeHidden(v: boolean) {
  try {
    if (v) localStorage.setItem(HIDE_KEY, "1"); else localStorage.removeItem(HIDE_KEY);
  } catch {
    // Best effort only; the checklist still works, just always expanded.
  }
}

type ChecklistItem = { id: string; label: string; done: boolean; action: string; onAction: () => void };

function Checklist({ items }: { items: ChecklistItem[] }) {
  const [hidden, setHidden] = useState(readHidden);
  const done = items.filter((i) => i.done).length;
  if (done === items.length) return null; // Disappears by itself once everything's done.
  const setHiddenAndSave = (v: boolean) => { setHidden(v); writeHidden(v); };
  return (
    <section aria-labelledby="checklist-title" className="rounded-lg border bg-card p-5 md:p-6">
      <div className="flex items-center justify-between gap-3">
        <h2 id="checklist-title" className="font-display text-lg font-semibold">Getting started</h2>
        <span className="text-sm text-muted-foreground">{done} of {items.length} done</span>
      </div>
      {!hidden && (
        <ul className="mt-4 flex flex-col gap-2.5">
          {items.map((i) => (
            <li key={i.id} className="flex items-center gap-3 text-sm">
              {i.done
                ? <CircleCheck aria-hidden="true" className="size-4 shrink-0 text-status-available" />
                : <span aria-hidden="true" className="size-4 shrink-0 rounded-full border-2 border-muted-foreground/40" />}
              <span className={i.done ? "text-muted-foreground line-through" : "flex-1"}>{i.label}</span>
              {!i.done && <Button size="sm" variant="outline" onClick={i.onAction}>{i.action}</Button>}
            </li>
          ))}
        </ul>
      )}
      <button type="button" className="mt-4 text-sm text-link underline-offset-4 hover:underline"
        onClick={() => setHiddenAndSave(!hidden)}>
        {hidden ? "Show the list" : "Hide this list"}
      </button>
    </section>
  );
}

function Card({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section aria-labelledby={`${title}-title`} className="flex flex-col rounded-lg border bg-card p-5">
      <div className="flex items-center justify-between gap-3">
        <h2 id={`${title}-title`} className="font-display text-base font-semibold">{title}</h2>
      </div>
      <div className="mt-3 flex flex-1 flex-col gap-2.5 text-sm">{children}</div>
      {action && <div className="mt-3">{action}</div>}
    </section>
  );
}


type ServiceHealth = components["schemas"]["ServiceHealth"];

/** Every service in one line when all's well; otherwise each one that isn't. */
function ServicesSummary({ containers }: { containers: ServiceHealth[] }) {
  const shown = containers.filter((c) => !(c.optional && c.state === "missing"));
  const trouble = shown.filter((c) => c.state !== "running");
  if (trouble.length === 0) {
    return (
      <div className="flex items-center gap-2.5">
        <Dot tone="good" />
        <span>All {shown.length} Linx services are running</span>
      </div>
    );
  }
  return trouble.map((c) => (
    <div key={c.service} className="flex items-center gap-2.5">
      <Dot tone={c.state === "starting" || c.state === "restarting" ? "warn" : "bad"} />
      <span>{c.label}</span>
      <span className="ms-auto text-muted-foreground">
        {c.state === "starting" ? "Starting" : c.state === "restarting" ? "Restarting" : c.state === "unhealthy" ? "Not answering"
          : c.state === "missing" ? "Not installed" : "Stopped"}
      </span>
    </div>
  ));
}

function callPeerLabel(party: ActiveCall["from"], members: TeamMember[] | null): string {
  if (party.extension) {
    const m = members?.find((x) => x.extension === party.extension);
    return `${m?.name ?? (party.name || "Extension")} (${party.extension})`;
  }
  return party.number || party.name || "Unknown";
}

function calleeLabel(to: string, members: TeamMember[] | null): string {
  const m = members?.find((x) => x.extension === to);
  return m ? `${m.name} (${to})` : to;
}

export function AdminHomeScreen({ me, systemStatus, members }: { me: Me; systemStatus: SystemStatus | null; members: TeamMember[] | null }) {
  const canSettings = hasScope(me, "settings:read");
  const canCalls = hasScope(me, "calls:read");
  const canRouting = hasScope(me, "routing:read");
  const [setup, setSetup] = useState<SetupProgress | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [dids, setDids] = useState<Did[] | null>(null);
  const [active, setActive] = useState<ActiveCall[] | null>(null);

  const load = useCallback(async () => {
    const calls: Promise<void>[] = [];
    if (canSettings) {
      calls.push(api.GET("/api/v1/setup").then(({ data }) => { if (data) setSetup(data); }));
      calls.push(api.GET("/api/v1/settings").then(({ data }) => { if (data) setSettings(data); }));
    }
    if (canRouting) {
      calls.push(api.GET("/api/v1/inbound-routes").then(({ data }) => { if (data) setDids(data.items); }));
    }
    if (canCalls) {
      calls.push(api.GET("/api/v1/calls/active").then(({ data }) => { if (data) setActive(data.items); }));
    }
    await Promise.all(calls);
  }, [canSettings, canRouting, canCalls]);

  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 15_000);
    return () => window.clearInterval(t);
  }, [load]);

  const trunks = systemStatus?.trunks ?? [];
  const hasLine = trunks.length > 0;

  const items: ChecklistItem[] = [];
  if (canSettings && setup) {
    items.push({
      id: "numbering", label: "Choose how extension numbers look", done: setup.step > 3,
      action: "Choose", onAction: () => navigate("/setup"),
    });
    items.push({
      id: "people", label: "Add the people who'll use Linx", done: setup.step > 4,
      action: "Add", onAction: () => navigate("/setup"),
    });
    items.push({
      id: "test-call", label: "Test a call from your browser", done: setup.completed,
      action: "Test", onAction: () => navigate("/setup"),
    });
  }
  items.push({
    id: "second-step", label: "Add a second way to sign in (passkey)",
    done: !!me.mfa_enabled || (me.passkeys ?? 0) > 0, action: "Add", onAction: () => navigate("/account"),
  });
  items.push({
    id: "line", label: "Connect a phone line", done: hasLine,
    action: "Connect", onAction: () => navigate("/admin/lines"),
  });
  if (hasLine && canRouting) {
    items.push({
      id: "did", label: "Send your phone number to someone", done: !!dids?.some((d) => d.extension_id),
      action: "Choose", onAction: () => navigate("/admin/incoming"),
    });
  }
  if (canSettings && settings) {
    items.push({
      id: "calls", label: "Decide what your phones can call", done: !!settings.default_call_permission_level_id,
      action: "Review", onAction: () => navigate(settings.default_call_permission_level_id ? "/admin/outgoing" : "/setup"),
    });
  }

  const openAlerts = systemStatus?.open_alerts ?? [];
  const onCall = canCalls ? (active ?? []).filter((c) => c.state !== "system") : null;

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <h1 className="font-display text-3xl font-semibold tracking-tight">Good day, {me.name ?? "there"}</h1>

      <div className="mt-6 flex flex-col gap-6">
        {canSettings && <MovedChecklist canWrite={hasScope(me, "settings:write")} />}
        {items.length > 0 && <Checklist items={items} />}

        <div className="grid gap-6 sm:grid-cols-2">
          <Card title="Needs attention" action={<Button variant="outline" size="sm" onClick={() => navigate("/admin/system/alerts")}>All alerts</Button>}>
            {openAlerts.length === 0 && <p className="text-muted-foreground">Nothing needs you right now.</p>}
            {openAlerts.slice(0, 4).map((a, i) => (
              <div key={i} className="flex items-start gap-2.5">
                <TriangleAlert aria-hidden="true"
                  className={a.severity === "critical" ? "mt-0.5 size-4 shrink-0 text-status-busy" : "mt-0.5 size-4 shrink-0 text-status-away"} />
                <span>{a.message}</span>
              </div>
            ))}
          </Card>

          <Card title="Phone lines" action={<Button variant="outline" size="sm" onClick={() => navigate("/admin/lines")}>All lines</Button>}>
            {trunks.length === 0 && <p className="text-muted-foreground">No phone line yet — Linx can call between extensions only.</p>}
            {trunks.map((t) => (
              <div key={t.id} className="flex items-center gap-2.5">
                <Dot tone={trunkDot[t.status] ?? "neutral"} />
                <span className="flex-1">{t.name}</span>
                <span className="text-muted-foreground">{trunkWords[t.status] ?? t.status}</span>
              </div>
            ))}
          </Card>

          <Card title="On a call now" action={<Button variant="outline" size="sm" onClick={() => navigate("/team")}>Team</Button>}>
            {onCall !== null ? (
              onCall.length === 0
                ? <p className="text-muted-foreground">Nobody's on a call.</p>
                : onCall.map((c) => (
                  <div key={c.id} className="flex items-center gap-2.5">
                    <Phone aria-hidden="true" className="size-4 shrink-0 text-call" />
                    <span>{callPeerLabel(c.from, members)} <span aria-hidden="true">↔</span> {calleeLabel(c.to, members)}</span>
                  </div>
                ))
            ) : (
              (members ?? []).filter((m) => m.status === "on_call" || m.status === "ringing").length === 0
                ? <p className="text-muted-foreground">Nobody's on a call.</p>
                : (members ?? []).filter((m) => m.status === "on_call" || m.status === "ringing").map((m) => (
                  <div key={m.extension} className="flex items-center gap-2.5">
                    <StatusDot status={m.status} />
                    <span>{m.name} <span className="text-muted-foreground">· {statusLabel[m.status]}</span></span>
                  </div>
                ))
            )}
          </Card>

          <Card title="System" action={
            <Button variant="outline" size="sm" onClick={() => navigate("/admin/system/status")}>Status</Button>
          }>
            {!systemStatus ? <p className="text-muted-foreground">Loading…</p>
              : systemStatus.containers.length > 0 ? <ServicesSummary containers={systemStatus.containers} />
              : Object.entries(systemStatus.services).map(([name, s]) => (
                <div key={name} className="flex items-center gap-2.5">
                  <Dot tone={s === "ok" ? "good" : s === "degraded" ? "warn" : "bad"} />
                  <span className="capitalize">{name.replace(/[-_]/g, " ")}</span>
                  <span className="ms-auto text-muted-foreground">{s === "ok" ? "Running" : s === "degraded" ? "Degraded" : "Down"}</span>
                </div>
              ))}
          </Card>
        </div>
      </div>
    </div>
  );
}
