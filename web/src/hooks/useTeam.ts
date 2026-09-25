// The Team list, kept live over /api/v1/team/live (docs/WEB.md §6). The
// server sends the whole list whenever it changes; this reconnects with a
// growing pause if the connection drops.
import { useEffect, useState } from "react";
import type { TeamList, TeamMember } from "@/api/client";

export const TEAM_LIVE_PATH = "/api/v1/team/live";
export const TEAM_SUBPROTOCOL = "linx.team.v1";

export interface TeamState {
  members: TeamMember[] | null; // null until the first list arrives
  connected: boolean;
}

export function useTeam(enabled: boolean): TeamState {
  const [state, setState] = useState<TeamState>({ members: null, connected: false });
  useEffect(() => {
    if (!enabled) return;
    let ws: WebSocket | null = null;
    let retry: number | undefined;
    let delay = 1000;
    let closed = false;
    const open = () => {
      ws = new WebSocket(`wss://${window.location.host}${TEAM_LIVE_PATH}`, TEAM_SUBPROTOCOL);
      ws.onmessage = (ev) => {
        delay = 1000;
        try {
          const list = JSON.parse(String(ev.data)) as TeamList;
          setState({ members: list.items, connected: true });
        } catch {
          // Ignore a message we can't read; the next one replaces it.
        }
      };
      ws.onclose = () => {
        setState((s) => ({ ...s, connected: false }));
        if (closed) return;
        retry = window.setTimeout(open, delay);
        delay = Math.min(delay * 2, 30_000);
      };
    };
    open();
    return () => {
      closed = true;
      window.clearTimeout(retry);
      ws?.close();
    };
  }, [enabled]);
  return state;
}
