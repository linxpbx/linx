// The Call history badge (docs/ui/SCREENS_PHASE1F.md §7, §16 item 4):
// calls I missed since I last opened Call history. Fetched once, then
// again only when the Team websocket says call history changed (no
// polling). While the tab is open (open), it's cleared instead, so a call
// missed while looking at the list doesn't count.
import { useEffect, useState } from "react";
import { api } from "@/api/client";

export function useMissedCalls(stamp: number | undefined, connected: boolean, open: boolean): number {
  const [missed, setMissed] = useState(0);
  useEffect(() => {
    let live = true;
    if (open) {
      setMissed(0);
      void api.DELETE("/api/v1/me/missed-calls");
    } else {
      void api.GET("/api/v1/me/missed-calls").then(({ data }) => { if (live && data) setMissed(data.missed); });
    }
    return () => { live = false; };
  }, [stamp, connected, open]);
  return missed;
}
