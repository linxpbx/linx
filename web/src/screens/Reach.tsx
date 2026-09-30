// The phone's side of "Check it" (docs/ui/SCREENS_PHASE1F.md §2): opened from a
// one-time link with Wi-Fi off, no sign-in. It says the address Linx saw and
// tests call audio from here (a relay address from Linx's call relay), and
// tells the admin's page both. It says nothing else about the server.
import { useEffect, useRef, useState } from "react";
import { Check, LoaderCircle, X } from "lucide-react";
import { api, problemMessage } from "@/api/client";
import { Wordmark } from "@/components/brand";
import { ThemeCorner } from "@/components/ThemeMenu";

/** How long the phone waits for a relay address before calling it a failure. */
const RELAY_WAIT_MS = 8000;

type Relay = "testing" | "ok" | "failed";

/** Asks the call relay for an address the way a call would (relay only). */
async function testRelay(turn: { urls: string[]; username: string; credential: string }): Promise<{ ok: boolean; detail?: string }> {
  if (typeof RTCPeerConnection === "undefined") return { ok: false, detail: "this browser can't make calls" };
  const pc = new RTCPeerConnection({ iceServers: [{ urls: turn.urls, username: turn.username, credential: turn.credential }], iceTransportPolicy: "relay" });
  try {
    const got = new Promise<boolean>((resolve) => {
      const timer = setTimeout(() => resolve(false), RELAY_WAIT_MS);
      pc.onicecandidate = (e) => {
        if (e.candidate?.type === "relay" || e.candidate?.candidate.includes(" typ relay")) { clearTimeout(timer); resolve(true); }
        if (!e.candidate) { clearTimeout(timer); resolve(false); }
      };
    });
    pc.createDataChannel("check");
    await pc.setLocalDescription(await pc.createOffer());
    return (await got) ? { ok: true } : { ok: false, detail: "no answer from the call relay" };
  } catch (e) {
    return { ok: false, detail: e instanceof Error ? e.message.slice(0, 200) : "the test couldn't run" };
  } finally {
    pc.close();
  }
}

export default function Reach({ code }: { code: string }) {
  const [state, setState] = useState<{ domain: string; address: string } | null>(null);
  const [error, setError] = useState("");
  const [relay, setRelay] = useState<Relay>("testing");
  // One claim, even when React runs the effect twice.
  const started = useRef(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void (async () => {
      const { data, error } = await api.POST("/api/v1/reach/{code}", { params: { path: { code } } });
      if (!data) {
        setError(problemMessage(error));
        return;
      }
      setState({ domain: data.domain, address: data.address });
      const r = await testRelay(data.turn);
      setRelay(r.ok ? "ok" : "failed");
      await api.POST("/api/v1/reach/{code}/relay", { params: { path: { code } }, body: r });
    })();
  }, [code]);

  return (
    <main className="flex min-h-dvh items-center justify-center px-4 py-10">
      <ThemeCorner />
      <div className="w-full max-w-sm">
        <div className="mb-8 flex justify-center"><Wordmark className="text-5xl" /></div>
        <section className="rounded-lg border bg-card p-6 text-sm shadow-xs" aria-labelledby="reach-title">
          {error ? (
            <>
              <h1 id="reach-title" className="font-display text-xl font-semibold">This link can't be used</h1>
              <p role="alert" className="mt-2 text-muted-foreground">{error}</p>
            </>
          ) : !state ? (
            <>
              <h1 id="reach-title" className="font-display text-xl font-semibold">Checking…</h1>
              <p className="mt-2 flex items-center gap-2 text-muted-foreground" role="status">
                <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Reaching Linx
              </p>
            </>
          ) : (
            <>
              <h1 id="reach-title" className="flex items-start gap-2 font-display text-xl font-semibold">
                <Check aria-hidden="true" className="mt-1 size-5 shrink-0 text-status-available" />
                <span className="min-w-0 break-words">You reached Linx at {state.domain}</span>
              </h1>
              <p className="mt-2">Linx saw you coming from {state.address}.</p>
              <p role="status" className="mt-4 flex items-start gap-2">
                {relay === "testing" && <><LoaderCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground" />Testing call audio from here…</>}
                {relay === "ok" && <><Check aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-available" />Calls from here will have audio.</>}
                {relay === "failed" && <><X aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />Calls from here would have no audio. The page that sent you here says what to check.</>}
              </p>
              {relay !== "testing" && <p className="mt-4 text-muted-foreground">You can close this page.</p>}
            </>
          )}
        </section>
      </div>
    </main>
  );
}
