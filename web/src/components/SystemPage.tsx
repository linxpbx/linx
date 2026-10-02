// The System page's frame (docs/ui/ADMIN_SCREENS_PHASE1E.md §10): its
// heading and tabs, and the cards each tab is made of. Server settings is
// a system admin's tab only.
import type { ReactNode } from "react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { navigate } from "@/hooks/useRoute";
import { cn } from "@/lib/utils";

const SERVER = "/admin/system/server";
const SYSTEM_TABS: { label: string; path?: string }[] = [
  { label: "Status", path: "/admin/system/status" }, { label: "Alerts", path: "/admin/system/alerts" },
  { label: "Activity", path: "/admin/system/activity" }, { label: "Routing changes", path: "/admin/system/routing-changes" },
  { label: "Settings", path: "/admin/system/settings" },
  { label: "Backups", path: "/admin/system/backups" }, { label: "Server settings", path: SERVER },
];

/** systemAdmin: Server settings is only a system admin's tab. */
export function SystemHeader({ current, systemAdmin = false }: { current: string; systemAdmin?: boolean }) {
  return (
    <>
      <h1 className="font-display text-3xl font-semibold tracking-tight">System</h1>
      <nav aria-label="System" className="mt-4 flex flex-wrap gap-x-1 border-b">
        {SYSTEM_TABS.filter((t) => t.path !== SERVER || systemAdmin).map((t) => {
          const active = t.path === current;
          const tab = (
            <button key={t.label} type="button" aria-current={active ? "page" : undefined} aria-disabled={!t.path || undefined}
              onClick={t.path ? () => navigate(t.path!) : undefined}
              className={cn("-mb-px border-b-2 px-3 py-2 text-sm",
                active ? "border-primary font-medium" : "border-transparent",
                t.path ? "hover:text-foreground" : "cursor-default text-muted-foreground/60")}>
              {t.label}
            </button>
          );
          return t.path ? tab : (
            <Tooltip key={t.label}>
              <TooltipTrigger asChild>{tab}</TooltipTrigger>
              <TooltipContent>Coming soon</TooltipContent>
            </Tooltip>
          );
        })}
      </nav>
    </>
  );
}

export function SystemCard({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="rounded-lg border bg-card p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="font-display text-lg font-semibold">{title}</h2>
        {action}
      </div>
      <div className="mt-3">{children}</div>
    </section>
  );
}

/** A button greyed with its reason when the caller can't use it (§0). */
export function Guarded({ allowed, reason, children }: { allowed: boolean; reason: string; children: ReactNode }) {
  if (allowed) return <>{children}</>;
  return (
    <Tooltip>
      <TooltipTrigger asChild><span tabIndex={0}>{children}</span></TooltipTrigger>
      <TooltipContent>{reason}</TooltipContent>
    </Tooltip>
  );
}
