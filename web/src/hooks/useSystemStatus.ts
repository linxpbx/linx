// System status (docs/ADMIN.md §9): open alerts and phone lines, used by
// the admin shell's sidebar (badge, "line down" dot) and the admin home
// page. Refreshed every 15 s while an admin is signed in
// (docs/ui/ADMIN_SCREENS_PHASE1E.md §0 "Refresh").
import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";

export type SystemStatus = components["schemas"]["SystemStatus"];

const REFRESH_MS = 15_000;

export function useSystemStatus(enabled: boolean): SystemStatus | null {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  useEffect(() => {
    if (!enabled) {
      setStatus(null);
      return;
    }
    let stale = false;
    const load = async () => {
      const { data } = await api.GET("/api/v1/system/status");
      if (!stale && data) setStatus(data);
    };
    void load();
    const t = window.setInterval(() => void load(), REFRESH_MS);
    return () => { stale = true; window.clearInterval(t); };
  }, [enabled]);
  return status;
}

/** Whether any phone line needs attention, for the sidebar's dot. */
export function anyLineDown(status: SystemStatus | null): boolean {
  return !!status?.trunks.some((t) => t.status === "unreachable" || t.status === "rejected");
}
