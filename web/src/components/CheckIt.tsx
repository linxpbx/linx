// "Check it" (docs/ui/SCREENS_PHASE1F.md §2, docs/SIMPLER.md §2.3): can people
// reach Linx from outside? From this server, the checks in plain words; from
// outside, a one-time link to open on a phone with Wi-Fi off. The page
// waits for the phone with one held-open request at a time (the server
// answers the moment something changes), never by asking over and over.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Check, Info, LoaderCircle, TriangleAlert, X } from "lucide-react";
import { navigate } from "@/hooks/useRoute";
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { CopyButton } from "@/components/InstallFrame";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type Line = components["schemas"]["ReachCheck"]["lines"][number];
type ReachLink = components["schemas"]["ReachLink"];

/** A link inside the app. */
function Link({ to, className, children }: { to: string; className?: string; children: ReactNode }) {
  return <a href={to} className={className} onClick={(e) => { e.preventDefault(); navigate(to); }}>{children}</a>;
}

/** Where each fix is explained. */
const fixes: Record<NonNullable<Line["fix"]>, { to: string; label: string }> = {
  steps: { to: "/help/front-doors", label: "Show the steps" },
  records: { to: "/help/cant-open-the-address", label: "Show what to check" },
  router: { to: "/help/front-doors", label: "Show the steps" },
};

export function CheckIt({ className }: { className?: string }) {
  const [running, setRunning] = useState(false);
  const [lines, setLines] = useState<Line[] | null>(null);
  const [link, setLink] = useState<ReachLink | null>(null);
  const [error, setError] = useState("");
  // Bumped to stop an older wait when a new check starts or the panel goes.
  const run = useRef(0);

  const follow = useCallback(async (first: ReachLink, mine: number) => {
    let k = first;
    // Until the phone has come and gone (its relay test in), or the link runs out.
    while (run.current === mine && k.state !== "expired" && !(k.state === "reached" && k.relay)) {
      const { data, error } = await api.GET("/api/v1/system/reach-links/{id}", {
        params: { path: { id: k.id }, query: { after: k.version } },
      });
      if (run.current !== mine) return;
      if (!data) {
        setError(problemMessage(error));
        return;
      }
      k = data;
      setLink(data);
    }
  }, []);

  const check = useCallback(async () => {
    const mine = ++run.current;
    setRunning(true);
    setError("");
    setLines(null);
    setLink(null);
    const [server, phone] = await Promise.all([
      api.POST("/api/v1/system/reach-check"),
      api.POST("/api/v1/system/reach-links"),
    ]);
    if (run.current !== mine) return;
    setRunning(false);
    if (server.data) setLines(server.data.lines);
    else setError(problemMessage(server.error));
    if (phone.data) {
      setLink(phone.data);
      void follow(phone.data, mine);
    }
  }, [follow]);

  useEffect(() => () => { run.current++; }, []);

  return (
    <div className={cn("flex min-w-0 flex-col gap-4 text-sm", className)}>
      {!lines && !running && !error && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-muted-foreground">Checks the names, the front door and call audio, from this server and from your phone.</p>
          <Button variant="outline" onClick={() => void check()}>Check it</Button>
        </div>
      )}
      {running && (
        <p role="status" className="flex items-center gap-2 text-muted-foreground">
          <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Checking… this takes up to 10 seconds.
        </p>
      )}
      {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
      {lines && (
        <section aria-labelledby="check-server" className="flex flex-col gap-2">
          <h3 id="check-server" className="font-medium">From this server</h3>
          <ul className="flex flex-col gap-2">
            {lines.map((l) => <CheckLine key={l.text} line={l} />)}
          </ul>
        </section>
      )}
      {link && lines && <Phone link={link} onNew={() => void check()} />}
      {lines && (
        <div className="flex justify-end">
          <Button variant="outline" onClick={() => void check()} disabled={running}>Check again</Button>
        </div>
      )}
    </div>
  );
}

function Mark({ state }: { state: Line["state"] | "wait" }) {
  const c = "mt-0.5 size-4 shrink-0";
  switch (state) {
    case "ok": return <Check aria-label="Works" className={cn(c, "text-status-available")} />;
    case "fail": return <X aria-label="Doesn't work" className={cn(c, "text-destructive")} />;
    case "warn": return <TriangleAlert aria-label="Look at this" className={cn(c, "text-status-away")} />;
    case "wait": return <LoaderCircle aria-label="Waiting" className={cn(c, "animate-spin text-muted-foreground")} />;
    default: return <Info aria-label="Note" className={cn(c, "text-muted-foreground")} />;
  }
}

function CheckLine({ line }: { line: Line }) {
  const fix = line.fix && line.state !== "ok" ? fixes[line.fix] : undefined;
  return (
    <li className="flex min-w-0 items-start gap-2">
      <Mark state={line.state} />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="break-words">{line.text}</span>
        {line.meaning && <span className="break-words text-muted-foreground">{line.meaning}</span>}
        {fix && <Link to={fix.to} className="w-fit text-link hover:underline">{fix.label}</Link>}
      </span>
    </li>
  );
}

/** The phone half: the link, its picture, and what Linx saw. */
function Phone({ link, onNew }: { link: ReachLink; onNew: () => void }) {
  const [qr, setQr] = useState("");
  useEffect(() => {
    let live = true;
    void (async () => {
      const url = await (await import("qrcode")).default.toDataURL(link.url, { margin: 1, width: 144 });
      if (live) setQr(url);
    })();
    return () => { live = false; };
  }, [link.url]);

  return (
    <section aria-labelledby="check-phone" className="flex flex-col gap-2">
      <h3 id="check-phone" className="font-medium">From outside</h3>
      {link.state === "waiting" && (
        <>
          <p>Open this on your phone with Wi-Fi turned off:</p>
          <div className="flex min-w-0 flex-wrap items-center gap-4">
            {qr && <img src={qr} alt="The link as a picture to scan" width={144} height={144} className="rounded-md border bg-white" />}
            <div className="flex min-w-0 flex-col gap-2">
              <span className="flex min-w-0 items-center gap-1">
                <code className="min-w-0 font-mono break-all">{link.url}</code>
                <CopyButton text={link.url} label="link" />
              </span>
              <p role="status" className="flex items-center gap-2 text-muted-foreground">
                <Mark state="wait" />Waiting for your phone… (works for 10 minutes)
              </p>
            </div>
          </div>
        </>
      )}
      {link.state === "expired" && (
        <p className="flex items-start gap-2">
          <Mark state="info" />
          <span>The link ran out before a phone opened it. <button type="button" className="text-link hover:underline" onClick={onNew}>Make a new one</button></span>
        </p>
      )}
      {link.state === "reached" && <Seen link={link} onNew={onNew} />}
    </section>
  );
}

function Seen({ link, onNew }: { link: ReachLink; onNew: () => void }) {
  const again = <button type="button" className="text-link hover:underline" onClick={onNew}>Try again with a new link</button>;
  if (link.seen === "home" || link.seen === "local") {
    return (
      <p role="status" className="flex items-start gap-2">
        <Mark state="warn" />
        <span>Your phone came from your own network ({link.address}), so this doesn't show whether people outside can reach Linx. Turn Wi-Fi off on the phone. {again}</span>
      </p>
    );
  }
  return (
    <ul role="status" className="flex flex-col gap-2">
      {link.seen === "proxy" ? (
        <li className="flex items-start gap-2">
          <Mark state="warn" />
          <span>Linx saw your front door's address ({link.address}), not your phone's. It isn't telling Linx who's visiting: turn on “PROXY protocol, version 2” for your domain (step 3 of the front-door card).</span>
        </li>
      ) : (
        <li className="flex items-start gap-2">
          <Mark state="ok" />
          <span>Your phone reached Linx from {link.address}. People outside can open Linx.</span>
        </li>
      )}
      {!link.relay ? (
        <li className="flex items-start gap-2"><Mark state="wait" /><span>Testing call audio from your phone…</span></li>
      ) : link.relay.ok ? (
        <li className="flex items-start gap-2"><Mark state="ok" /><span>Calls from outside will have audio.</span></li>
      ) : (
        <li className="flex items-start gap-2">
          <Mark state="fail" />
          <span className="flex flex-col gap-0.5">
            <span>Call audio from your phone didn't get through{link.relay.detail ? ` (${link.relay.detail})` : ""}.</span>
            <span className="text-muted-foreground">Calls from outside will have no audio. Check that turn.{new URL(link.url).hostname} reaches Linx.</span>
            <Link to="/help/front-doors" className="w-fit text-link hover:underline">Show the steps</Link>
          </span>
        </li>
      )}
    </ul>
  );
}
