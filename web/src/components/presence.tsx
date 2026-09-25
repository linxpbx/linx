import { useEffect, useState } from "react";
import type { Presence, TeamMember } from "@/api/client";
import { cn } from "@/lib/utils";

type Status = TeamMember["status"];

const dotClass: Record<Status, string> = {
  on_call: "bg-status-busy",
  ringing: "bg-status-ringing",
  dnd: "bg-status-dnd",
  away: "bg-status-away",
  available: "bg-status-available",
  offline: "bg-status-offline",
};

export const statusLabel: Record<Status, string> = {
  on_call: "On a call",
  ringing: "Ringing",
  dnd: "Do not disturb",
  away: "Away",
  available: "Available",
  offline: "Offline",
};

export const presenceLabel: Record<Presence, string> = {
  available: "Available",
  away: "Away",
  dnd: "Do not disturb",
};

/** A status dot. Never colour alone: it carries its label as its name. */
export function StatusDot({ status, className }: { status: Status; className?: string }) {
  return (
    <span role="img" aria-label={statusLabel[status]}
      className={cn("inline-block size-2.5 shrink-0 rounded-full ring-2 ring-card", dotClass[status], className)} />
  );
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const first = parts[0]?.[0] ?? "";
  const last = parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? "") : "";
  return (first + last).toUpperCase() || "?";
}

export function Avatar({ name, status, size = "md", className }:
  { name: string; status?: Status; size?: "md" | "lg" | "xl"; className?: string }) {
  const sizes = { md: "size-9 text-xs", lg: "size-12 text-sm", xl: "size-20 text-2xl" };
  return (
    <span className={cn("relative inline-flex shrink-0 items-center justify-center rounded-full bg-background font-semibold text-muted-foreground border border-border", sizes[size], className)}>
      <span aria-hidden="true">{initials(name)}</span>
      {status && <StatusDot status={status} className="absolute -right-0.5 -bottom-0.5" />}
    </span>
  );
}

/** mm:ss (or h:mm:ss) since `since`, ticking every second. */
export function useElapsed(since: number | undefined): string {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (since === undefined) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [since]);
  if (since === undefined) return "";
  return formatDuration(Math.max(0, now - since));
}

export function formatDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${pad(m)}:${pad(s % 60)}`;
}
