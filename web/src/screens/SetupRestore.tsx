// The setup wizard's first screen for a system admin on a fresh install —
// set up fresh or restore from a backup — and the restore itself
// (docs/BACKUP.md §4, docs/ui/ADMIN_SCREENS_PHASE1E.md §3.2). The browser
// only asks: the server's backup helper (linx-backup-agent) picks the
// request up within a minute and does the restoring. A finished restore
// replaces the whole database, this session included, so the page learns it
// worked when the API starts answering 401.
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { CircleAlert, DatabaseBackup, LoaderCircle, Sparkles } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { Wordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";

export type RestoreStatus = components["schemas"]["RestoreStatus"];

/** The default local backup folder (`linx backup` with no destination set up). */
const DEFAULT_FOLDER = "/var/backups/linx";

/** How often the page asks how the restore is going. */
const POLL_MS = 3000;

function Frame({ onFinishLater, children }: { onFinishLater?: () => void; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh flex-col bg-background">
      <header className="flex items-center justify-between gap-4 border-b px-4 py-4 md:px-8">
        <div className="flex items-center gap-6">
          <Wordmark className="text-2xl" />
          <h1 className="hidden font-display text-lg font-semibold sm:block">Set up Linx</h1>
        </div>
        {onFinishLater && (
          <button type="button" className="text-sm text-link underline-offset-4 hover:underline" onClick={onFinishLater}>
            Finish later →
          </button>
        )}
      </header>
      <div className="flex flex-1 items-start justify-center overflow-y-auto px-4 py-10 md:px-8">
        <div className="w-full max-w-xl">{children}</div>
      </div>
    </main>
  );
}

function Recommended() {
  return <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>;
}

/** "How do you want to start?": fresh, or from a backup. */
export function StartChoice({ onFresh, onRestore, onFinishLater }: { onFresh: () => void; onRestore: () => void; onFinishLater: () => void }) {
  return (
    <Frame onFinishLater={onFinishLater}>
      <h2 className="font-display text-2xl font-semibold tracking-tight">How do you want to start?</h2>
      <div className="mt-6 grid gap-3">
        <button type="button" onClick={onFresh} className="flex items-start gap-4 rounded-lg border p-5 text-start transition-colors hover:bg-card">
          <Sparkles aria-hidden="true" className="mt-1 size-5 shrink-0 text-primary" />
          <span>
            <span className="font-display text-lg font-semibold">Set up fresh<Recommended /></span>
            <span className="mt-1 block text-sm text-muted-foreground">
              A few questions: where you'll use Linx, your extension numbers, your people. About five minutes.
            </span>
          </span>
        </button>
        <button type="button" onClick={onRestore} className="flex items-start gap-4 rounded-lg border p-5 text-start transition-colors hover:bg-card">
          <DatabaseBackup aria-hidden="true" className="mt-1 size-5 shrink-0 text-primary" />
          <span>
            <span className="font-display text-lg font-semibold">Restore from a backup</span>
            <span className="mt-1 block text-sm text-muted-foreground">
              Moving to a new server, or starting again after a problem? Bring back everyone, their phones, your lines and settings.
            </span>
          </span>
        </button>
      </div>
    </Frame>
  );
}

type Where = "folder" | "destination";
type Which = "latest" | "chosen";

function Choice({ id, value, title, hint, children }: { id: string; value: string; title: ReactNode; hint?: string; children?: ReactNode }) {
  return (
    <div className="rounded-md border p-3 has-[[data-state=checked]]:border-primary">
      <label htmlFor={id} className="flex cursor-pointer items-start gap-3">
        <RadioGroupItem id={id} value={value} className="mt-0.5" />
        <span className="flex flex-col gap-0.5">
          <span className="text-sm font-medium">{title}</span>
          {hint && <span className="text-sm text-muted-foreground">{hint}</span>}
        </span>
      </label>
      {children && <div className="mt-3 ps-7">{children}</div>}
    </div>
  );
}

/**
 * The restore form, then its progress. `initial` is a request already under
 * way (the page was reloaded); onBack returns to the start choice.
 */
export function RestoreFromBackup({ me, initial, onBack, onFinishLater }: {
  me: Me; initial?: RestoreStatus; onBack: () => void; onFinishLater: () => void;
}) {
  const confirm = useConfirmIdentity(me);
  const underWay = initial && (initial.status === "pending" || initial.status === "running");
  const [phase, setPhase] = useState<"form" | "waiting" | "done">(underWay ? "waiting" : "form");
  const [status, setStatus] = useState<RestoreStatus | undefined>(initial);
  const [restarting, setRestarting] = useState(false);
  const [where, setWhere] = useState<Where>(initial?.source ?? "folder");
  const [folder, setFolder] = useState(initial?.source === "folder" && initial.location ? initial.location : DEFAULT_FOLDER);
  const [destination, setDestination] = useState(initial?.source === "destination" ? initial.location ?? "" : "");
  const [which, setWhich] = useState<Which>(initial?.snapshot && initial.snapshot !== "latest" ? "chosen" : "latest");
  const [snapshot, setSnapshot] = useState(initial?.snapshot && initial.snapshot !== "latest" ? initial.snapshot : "");
  const [password, setPassword] = useState("");
  const [understood, setUnderstood] = useState(false);
  const [busy, setBusy] = useState(false);
  // failure: what the server reported about the last attempt (shown at the
  // top); error: why this form couldn't be sent (shown by its button).
  const [failure, setFailure] = useState(initial?.status === "failed" ? failedMessage(initial) : "");
  const [error, setError] = useState("");
  const timer = useRef<number | undefined>(undefined);

  const poll = useCallback(async () => {
    const answer = await api.GET("/api/v1/backup-restore").catch(() => null);
    if (!answer) {
      // Linx is restarting with the backup in place: keep waiting.
      setRestarting(true);
      timer.current = window.setTimeout(() => void poll(), POLL_MS);
      return;
    }
    if (answer.response.status === 401) { setPhase("done"); return; }
    if (answer.data?.status === "failed") {
      setStatus(answer.data);
      setFailure(failedMessage(answer.data));
      setPassword("");
      setUnderstood(false);
      setPhase("form");
      return;
    }
    if (answer.data) setStatus(answer.data);
    setRestarting(!answer.data);
    timer.current = window.setTimeout(() => void poll(), POLL_MS);
  }, []);

  useEffect(() => {
    if (phase === "waiting") void poll();
    return () => window.clearTimeout(timer.current);
  }, [phase, poll]);

  const location = where === "folder" ? folder.trim().replace(/(.)\/+$/, "$1") : destination.trim();
  const ready = !!location && !!password && understood && (which === "latest" || snapshot.trim() !== "");

  const send = async () => {
    setBusy(true);
    setError("");
    setFailure("");
    const { data, error: err } = await api.POST("/api/v1/backup-restore", {
      body: { source: where, location, snapshot: which === "latest" ? "latest" : snapshot.trim().toLowerCase(), password },
    });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true as const };
    if (!data) { setError(problemMessage(err)); return { confirm: false as const }; }
    setPassword("");
    setStatus(data);
    setPhase("waiting");
    return { confirm: false as const };
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (ready && !busy) void confirm.run(send);
  };

  if (phase === "done") {
    return (
      <main className="flex min-h-dvh flex-col items-center justify-center gap-6 bg-background px-4 text-center">
        <Wordmark className="text-3xl" />
        <div>
          <h1 className="font-display text-2xl font-semibold">Restored</h1>
          <p className="mt-2 max-w-sm text-sm text-muted-foreground">
            Linx is back as it was in your backup. Everyone signs in again, with their accounts from the backup — you too.
          </p>
        </div>
        <Button onClick={() => window.location.assign("/")}>Sign in</Button>
      </main>
    );
  }

  if (phase === "waiting") {
    const running = status?.status === "running" || restarting;
    return (
      <Frame>
        <div role="status" className="flex flex-col items-center gap-4 py-10 text-center">
          <LoaderCircle aria-hidden="true" className="size-8 animate-spin text-primary" />
          <h2 className="font-display text-2xl font-semibold tracking-tight">
            {running ? "Restoring your backup" : "Starting the restore"}
          </h2>
          <p className="max-w-md text-sm text-muted-foreground">
            {restarting
              ? "Linx is restarting with your backup in place. This page moves on by itself."
              : running
                ? "Reading the backup and putting it in place. This usually takes a few minutes; Linx restarts at the end. Keep this page open."
                : "Waiting for the server to pick this up. It checks once a minute."}
          </p>
        </div>
      </Frame>
    );
  }

  return (
    <Frame onFinishLater={onFinishLater}>
      <form onSubmit={onSubmit} noValidate>
        <h2 className="font-display text-2xl font-semibold tracking-tight">Restore from a backup</h2>
        <p className="mt-2 text-sm text-muted-foreground">
          This puts back everything from a Linx backup: people, extensions and phones, phone lines, settings and the
          encryption keys that protect them.
        </p>
        {failure && (
          <p role="alert" className="mt-4 flex items-start gap-2 rounded-md border border-destructive p-3 text-sm font-medium text-destructive">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            {failure}
          </p>
        )}

        <fieldset className="mt-6">
          <legend className="text-sm font-medium">Where is the backup?</legend>
          <RadioGroup className="mt-2" value={where} onValueChange={(v) => setWhere(v as Where)} aria-label="Where is the backup?">
            <Choice id="where-folder" value="folder" title={<>A folder on this server<Recommended /></>}
              hint="Copy the old server's backup folder here first, or plug in the drive it's on.">
              {where === "folder" && (
                <div className="grid gap-1.5">
                  <Label htmlFor="restore-folder">Folder</Label>
                  <Input id="restore-folder" value={folder} onChange={(e) => setFolder(e.target.value)} spellCheck={false} autoComplete="off" />
                </div>
              )}
            </Choice>
            <Choice id="where-destination" value="destination" title="A backup place set up on this server"
              hint="For a network drive or cloud storage: add it on this server first, with the same details as before (sudo linx backup destination add).">
              {where === "destination" && (
                <div className="grid gap-1.5">
                  <Label htmlFor="restore-destination">Its name</Label>
                  <Input id="restore-destination" value={destination} onChange={(e) => setDestination(e.target.value)}
                    placeholder="office-nas" spellCheck={false} autoComplete="off" />
                </div>
              )}
            </Choice>
          </RadioGroup>
        </fieldset>

        <fieldset className="mt-6">
          <legend className="text-sm font-medium">Which backup?</legend>
          <RadioGroup className="mt-2" value={which} onValueChange={(v) => setWhich(v as Which)} aria-label="Which backup?">
            <Choice id="which-latest" value="latest" title={<>The newest<Recommended /></>} />
            <Choice id="which-chosen" value="chosen" title="An older one">
              {which === "chosen" && (
                <div className="grid gap-1.5">
                  <Label htmlFor="restore-snapshot">Backup ID</Label>
                  <Input id="restore-snapshot" value={snapshot} onChange={(e) => setSnapshot(e.target.value)}
                    placeholder="4f2a9c1e" spellCheck={false} autoComplete="off" />
                  <p className="text-sm text-muted-foreground">From the old server's backup history (System → Backups).</p>
                </div>
              )}
            </Choice>
          </RadioGroup>
        </fieldset>

        <div className="mt-6 grid gap-1.5">
          <Label htmlFor="restore-password">The backup's password</Label>
          <Input id="restore-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" />
          <p className="text-sm text-muted-foreground">
            Shown once when the backup place was set up. Linx keeps it only until the restore starts.
          </p>
        </div>

        <div role="note" className="mt-6 rounded-md border-2 border-status-away p-4 text-sm">
          <p className="flex items-start gap-2 font-medium">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
            This replaces everything on this server
          </p>
          <p className="mt-2 text-muted-foreground">
            Everything set up here so far, your own account included, is replaced by the backup. Afterwards everyone
            signs in again with their accounts from the backup.
          </p>
          <label className="mt-3 flex items-center gap-3">
            <Checkbox checked={understood} onCheckedChange={(v) => setUnderstood(v === true)} />
            I understand, replace everything
          </label>
        </div>

        {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}

        <div className="mt-8 flex items-center justify-between gap-3">
          <Button type="button" variant="outline" onClick={onBack} disabled={busy}>Back</Button>
          <Button type="submit" disabled={!ready || busy} aria-busy={busy}>Restore</Button>
        </div>
      </form>
      {confirm.dialog}
    </Frame>
  );
}

// A restore that got as far as changing anything replaced this request
// along with the rest of the database, so a failure shown here always means
// the server is as it was.
function failedMessage(s: RestoreStatus): string {
  const reason = (s.error ?? "no reason given").replace(/[.!?]?$/, ".");
  return `The restore didn't work: ${reason} Nothing on this server was changed.`;
}
