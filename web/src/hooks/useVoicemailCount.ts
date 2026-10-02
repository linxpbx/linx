// The Voicemail badge (docs/ui/SCREENS_PHASE1F.md §12.2): new messages in
// my own and my ring groups' boxes. Fetched once, then again only when the
// Team websocket says voicemail changed (no polling), and when the page
// comes back from being reconnected.
import { useEffect, useState } from "react";
import { api } from "@/api/client";

export function useVoicemailCount(stamp: number | undefined, connected: boolean): number {
  const [count, setCount] = useState(0);
  useEffect(() => {
    let live = true;
    void api.GET("/api/v1/me/voicemail-count").then(({ data }) => { if (live && data) setCount(data.new); });
    return () => { live = false; };
  }, [stamp, connected]);
  return count;
}
