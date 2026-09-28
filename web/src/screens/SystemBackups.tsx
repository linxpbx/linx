// System → Backups (docs/BACKUP.md §8 step 5, docs/ui/ADMIN_SCREENS_PHASE1E.md
// §10.5): the schedule, "back up now", a backup file to download (with its
// password, shown on request), where backups go and the history. The server
// never runs a backup itself: its backup helper (linx-backup-agent) checks
// once a minute and reports back, so everything here waits on that.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { CircleAlert, Copy, Download, LoaderCircle } from "lucide-react";
import type { ColumnDef } from "@tanstack/react-table";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { DataTable } from "@/components/DataTable";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Guarded as GuardedWith, SystemCard as Card, SystemHeader } from "@/components/SystemPage";
import { plainReason } from "@/lib/backupErrors";
import { formatSize } from "@/lib/backupFile";
import { hasScope } from "@/lib/roles";
import { cn } from "@/lib/utils";

type BackupSettings = components["schemas"]["BackupSettings"];
type BackupRun = components["schemas"]["BackupRun"];
type BackupDownload = components["schemas"]["BackupDownload"];
type Frequency = BackupSettings["frequency"];

/** While something is waiting on the server's backup helper, check this often. */
const POLL_MS = 5000;
const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const FREQUENCIES: [Frequency, string][] = [["off", "Off"], ["daily", "Every day"], ["weekly", "Every week"], ["monthly", "Every month"]];
const NO_WRITE = "Only an admin can change backups";

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
const clock = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
const toHHMM = (minutes: number) => `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
const fromHHMM = (s: string) => { const [h, m] = s.split(":").map(Number); return (h ?? 0) * 60 + (m ?? 0); };

/** A button greyed with its reason when the caller can't use it (§0). */
function Guarded({ allowed, children }: { allowed: boolean; children: ReactNode }) {
  return <GuardedWith allowed={allowed} reason={NO_WRITE}>{children}</GuardedWith>;
}

// --- The screen ---

export function SystemBackupsScreen({ me }: { me: Me }) {
  const canWrite = hasScope(me, "backups:write");
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [runs, setRuns] = useState<BackupRun[] | null>(null);
  const [download, setDownload] = useState<BackupDownload | null>(null);
  const [error, setError] = useState("");
  const timer = useRef<number | undefined>(undefined);

  const load = useCallback(async () => {
    const [s, r, d] = await Promise.all([
      api.GET("/api/v1/backup-settings"),
      api.GET("/api/v1/backups", { params: { query: { limit: 50 } } }),
      api.GET("/api/v1/backup-download"),
    ]);
    if (s.data) setSettings(s.data);
    if (r.data) setRuns(r.data.items);
    if (d.data) setDownload(d.data);
    if (s.error) setError(problemMessage(s.error));
  }, []);
  useEffect(() => { void load(); }, [load]);

  // Keep checking while the backup helper has something to do.
  const waiting = !!settings?.requested_at || download?.status === "pending" || download?.status === "preparing";
  useEffect(() => {
    window.clearTimeout(timer.current);
    if (waiting) timer.current = window.setTimeout(() => void load(), POLL_MS);
    return () => window.clearTimeout(timer.current);
  }, [waiting, settings, download, load]);

  if (!settings || !runs || !download) {
    return (
      <div className="w-full max-w-5xl px-4 py-6 md:px-6" aria-busy={!error}>
        {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
      </div>
    );
  }

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <SystemHeader current="/admin/system/backups" />
      <div className="mt-6 flex flex-col gap-4">
        <Card title="Backups" action={<BackUpNow canWrite={canWrite} settings={settings} onAsked={setSettings} />}>
          <Schedule canWrite={canWrite} settings={settings} onSaved={setSettings} />
        </Card>
        <Card title="Download a copy">
          <DownloadPanel me={me} canWrite={canWrite} download={download} onChange={setDownload} />
        </Card>
        <Card title="Where backups go">
          <Destinations runs={runs} />
        </Card>
        <Card title="History">
          <History runs={runs} />
        </Card>
      </div>
    </div>
  );
}

// --- Back up now ---

function BackUpNow({ canWrite, settings, onAsked }: { canWrite: boolean; settings: BackupSettings; onAsked: (s: BackupSettings) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const queued = !!settings.requested_at;
  const go = async () => {
    setBusy(true);
    setError("");
    const { error: err } = await api.POST("/api/v1/backups");
    setBusy(false);
    if (err) { setError(problemMessage(err)); return; }
    onAsked({ ...settings, requested_at: new Date().toISOString() });
  };
  return (
    <div className="flex items-center gap-3">
      {queued && (
        <span role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircle aria-hidden="true" className="size-4 animate-spin" />Starts within a minute
        </span>
      )}
      {error && <span role="alert" className="text-sm font-medium text-destructive">{error}</span>}
      <Guarded allowed={canWrite}>
        <Button onClick={() => void go()} disabled={!canWrite || busy || queued}>Back up now</Button>
      </Guarded>
    </div>
  );
}

// --- The schedule ---

function Schedule({ canWrite, settings, onSaved }: { canWrite: boolean; settings: BackupSettings; onSaved: (s: BackupSettings) => void }) {
  const [freq, setFreq] = useState<Frequency>(settings.frequency);
  const [time, setTime] = useState(toHHMM(settings.time_of_day));
  const [dow, setDow] = useState(settings.day_of_week);
  const [dom, setDom] = useState(settings.day_of_month);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const changed = freq !== settings.frequency || fromHHMM(time) !== settings.time_of_day
    || dow !== settings.day_of_week || dom !== settings.day_of_month;

  const save = async () => {
    setBusy(true);
    setNote("");
    const { data, error } = await api.PATCH("/api/v1/backup-settings", {
      body: { frequency: freq, time_of_day: fromHHMM(time), day_of_week: dow, day_of_month: dom },
    });
    setBusy(false);
    if (!data) { setNote(problemMessage(error)); return; }
    onSaved(data);
    setNote("Saved.");
  };

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        A backup holds everyone's accounts, extensions and phones, your phone lines, settings and the keys that protect
        them. Nothing about calls is stored, so there's none of that in it.
      </p>
      <fieldset disabled={!canWrite} className="flex flex-col gap-3">
        <legend className="text-sm font-medium">Back up automatically</legend>
        <div role="radiogroup" aria-label="How often" className="mt-2 grid w-full grid-cols-2 overflow-hidden rounded-md border sm:inline-flex sm:w-fit">
          {FREQUENCIES.map(([v, label]) => (
            <button key={v} type="button" role="radio" aria-checked={freq === v} onClick={() => setFreq(v)}
              className={cn("px-3 py-1.5 text-sm", freq === v ? "bg-primary text-primary-foreground" : "hover:bg-muted")}>
              {label}{v === "daily" && <span className="sr-only"> (recommended)</span>}
            </button>
          ))}
        </div>
        {freq !== "off" && (
          <div className="flex flex-wrap items-end gap-3">
            {freq === "weekly" && (
              <div className="grid gap-1.5">
                <Label htmlFor="backup-dow">On</Label>
                <Select value={String(dow)} onValueChange={(v) => setDow(Number(v))}>
                  <SelectTrigger id="backup-dow" className="w-36"><SelectValue /></SelectTrigger>
                  <SelectContent>{DAYS.map((d, i) => <SelectItem key={d} value={String(i)}>{d}</SelectItem>)}</SelectContent>
                </Select>
              </div>
            )}
            {freq === "monthly" && (
              <div className="grid gap-1.5">
                <Label htmlFor="backup-dom">On day</Label>
                <Select value={String(dom)} onValueChange={(v) => setDom(Number(v))}>
                  <SelectTrigger id="backup-dom" className="w-24"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {Array.from({ length: 28 }, (_, i) => i + 1).map((d) => <SelectItem key={d} value={String(d)}>{d}</SelectItem>)}
                  </SelectContent>
                </Select>
              </div>
            )}
            <div className="grid gap-1.5">
              <Label htmlFor="backup-time">At</Label>
              <Input id="backup-time" type="time" value={time} onChange={(e) => setTime(e.target.value)} className="w-32" />
            </div>
            <p className="pb-2 text-sm text-muted-foreground">Server time. A quiet hour, like 03:00, is best.</p>
          </div>
        )}
        {freq === "off" && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <CircleAlert aria-hidden="true" className="size-4 text-status-away" />
            Off: nothing is backed up unless you press Back up now. Every day is recommended.
          </p>
        )}
      </fieldset>
      {canWrite && (
        <div className="flex items-center gap-3">
          <Button variant="outline" onClick={() => void save()} disabled={!changed || busy}>Save schedule</Button>
          {note && <span role="status" className="text-sm text-muted-foreground">{note}</span>}
        </div>
      )}
    </div>
  );
}

// --- Download a copy, and its password ---

function DownloadPanel({ me, canWrite, download, onChange }: {
  me: Me; canWrite: boolean; download: BackupDownload; onChange: (d: BackupDownload) => void;
}) {
  const confirm = useConfirmIdentity(me);
  const [error, setError] = useState("");
  const [password, setPassword] = useState("");
  const [copied, setCopied] = useState(false);

  const ask = async () => {
    setError("");
    const { data, error: err } = await api.POST("/api/v1/backup-download");
    if (needsConfirm(err)) return { confirm: true as const };
    if (!data) setError(problemMessage(err));
    else { setPassword(""); onChange(data); }
    return { confirm: false as const };
  };
  const reveal = async () => {
    setError("");
    const { data, error: err } = await api.POST("/api/v1/backup-download/password");
    if (needsConfirm(err)) return { confirm: true as const };
    if (!data) setError(problemMessage(err));
    else setPassword(data.password);
    return { confirm: false as const };
  };

  const intro = (
    <p className="text-sm text-muted-foreground">
      A single file to keep somewhere safe, away from this server — your computer, a USB drive. It's encrypted: it
      needs its password, which you can see here once it's ready. Keep the two apart. To use it, choose "A backup file
      on my computer" when setting up a new server.
    </p>
  );
  const askButton = (label: string) => (
    <Guarded allowed={canWrite}>
      <Button variant="outline" onClick={() => void confirm.run(ask)} disabled={!canWrite}>
        <Download aria-hidden="true" />{label}
      </Button>
    </Guarded>
  );

  let body: ReactNode;
  switch (download.status) {
    case "pending":
    case "preparing":
      body = (
        <p role="status" className="flex items-center gap-2 text-sm">
          <LoaderCircle aria-hidden="true" className="size-4 animate-spin text-primary" />
          {download.status === "pending"
            ? "Waiting for the server to start (it checks once a minute)…"
            : "Backing up now and making the file. This takes a minute or two…"}
          {!download.mine && " Another admin asked for this one."}
        </p>
      );
      break;
    case "ready":
      body = download.mine ? (
        <div className="flex flex-col gap-3">
          <p className="text-sm">
            Your backup file is ready: {formatSize(download.size ?? 0)}, newest backup in it from{" "}
            {download.snapshot_time ? when(download.snapshot_time) : "—"}. You can download it until{" "}
            {download.expires_at ? clock(download.expires_at) : "later"}.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button asChild><a href="/api/v1/backup-download/file" download><Download aria-hidden="true" />Download file</a></Button>
            {!password && <Button variant="outline" onClick={() => void confirm.run(reveal)}>Show its password</Button>}
          </div>
          {password && (
            <div className="rounded-md border-2 border-status-away p-3">
              <p className="text-sm font-medium">The file's password</p>
              <p className="mt-2 flex flex-wrap items-center gap-2">
                <code className="break-all rounded bg-background px-2 py-1 font-mono text-sm">{password}</code>
                <Button size="sm" variant="outline" onClick={() => { void navigator.clipboard?.writeText(password); setCopied(true); }}>
                  <Copy aria-hidden="true" />{copied ? "Copied" : "Copy"}
                </Button>
              </p>
              <p className="mt-2 text-sm text-muted-foreground">
                Anyone with the file and this password has your whole phone system. Save it in a password manager, not
                next to the file. It's the same for every backup file from this server.
              </p>
            </div>
          )}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">Another admin has a backup file ready. You can make your own once theirs has been downloaded or after an hour.</p>
      );
      break;
    case "failed":
      body = (
        <div className="flex flex-col gap-3">
          <p role="alert" className="flex items-start gap-2 text-sm font-medium text-destructive">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            The backup file couldn't be made: {download.error ?? "no reason given"}
          </p>
          <div>{askButton("Try again")}</div>
        </div>
      );
      break;
    default:
      body = <div>{askButton("Make a backup file")}</div>;
  }

  return (
    <div className="flex flex-col gap-3">
      {intro}
      {body}
      {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
      {confirm.dialog}
    </div>
  );
}

// --- Where backups go (from the newest run: the places live on the server) ---

function Destinations({ runs }: { runs: BackupRun[] }) {
  const latest = runs[0];
  const [copied, setCopied] = useState(false);
  const cmd = "sudo linx backup destination add";
  return (
    <div className="flex flex-col gap-3 text-sm">
      {!latest || latest.destinations.length === 0 ? (
        <p className="text-muted-foreground">
          No backup has run yet. Until you add a place, backups are kept in /var/backups/linx on this server — a
          second drive or another machine is better protection.
        </p>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {latest.destinations.map((d) => (
            <li key={d.name} className="flex items-start gap-2">
              <Dot tone={d.ok ? "good" : "bad"} className="mt-1.5" />
              <span className="min-w-0">
                <span className="font-medium">{d.name}</span>{" "}
                <span className="text-muted-foreground">
                  {d.ok ? `worked ${when(latest.finished_at)}` : `failed ${when(latest.finished_at)}`}
                </span>
                {!d.ok && d.error && <Reason error={d.error} />}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="flex flex-wrap items-center gap-2 text-muted-foreground">
        Add a network drive (SFTP) or cloud storage (S3) on the server, where their keys stay:
        <code className="rounded bg-background px-1.5 py-0.5 font-mono">{cmd}</code>
        <Button size="sm" variant="ghost" onClick={() => { void navigator.clipboard?.writeText(cmd); setCopied(true); }}>
          <Copy aria-hidden="true" />{copied ? "Copied" : "Copy"}
        </Button>
      </p>
    </div>
  );
}

// --- A failure's reason: plain words first, restic's own text under Details ---

function Reason({ name, error }: { name?: string; error: string }) {
  const [open, setOpen] = useState(false);
  const plain = plainReason(error);
  return (
    <span className="block whitespace-normal text-muted-foreground [overflow-wrap:anywhere]">
      {name && <span className="font-medium">{name}: </span>}
      {plain ?? error}
      {plain && (
        <>
          {" "}
          <button type="button" className="text-link underline-offset-4 hover:underline" aria-expanded={open}
            onClick={(e) => { e.stopPropagation(); setOpen(!open); }}>
            {open ? "Hide details" : "Details"}
          </button>
          {open && <span className="mt-1 block rounded bg-background p-2 font-mono text-xs">{error}</span>}
        </>
      )}
    </span>
  );
}

// --- History ---

const resultWords: Record<BackupRun["status"], [string, "good" | "warn" | "bad"]> = {
  success: ["Worked", "good"], partial: ["Partly worked", "warn"], failure: ["Failed", "bad"],
};

function History({ runs }: { runs: BackupRun[] }) {
  const columns: ColumnDef<BackupRun>[] = [
    { id: "when", header: "When", accessorFn: (r) => r.started_at, cell: ({ row }) => when(row.original.started_at) },
    { id: "how", header: "How", meta: { wide: true }, accessorFn: (r) => (r.trigger === "scheduled" ? "On schedule" : "Back up now") },
    {
      id: "result", header: "Result", accessorFn: (r) => resultWords[r.status][0],
      cell: ({ row }) => {
        const [words, tone] = resultWords[row.original.status];
        const run = row.original;
        const failed = run.destinations.filter((d) => !d.ok);
        return (
          <span className="flex items-start gap-2">
            <Dot tone={tone} className="mt-1.5" />
            <span className="min-w-0 max-w-md whitespace-normal">
              {words}
              {run.error && <Reason error={run.error} />}
              {!run.error && failed.map((d) => <Reason key={d.name} name={d.name} error={d.error ?? "failed"} />)}
            </span>
          </span>
        );
      },
    },
    {
      // What was backed up (the database and the keys), from the first
      // place it reached; the same for every place in one run.
      id: "size", header: "Size", meta: { wide: true },
      accessorFn: (r) => r.destinations.find((d) => d.ok && d.size)?.size ?? 0,
      cell: ({ getValue }) => {
        const size = Number(getValue() ?? 0);
        return size > 0 ? formatSize(size) : <span className="text-muted-foreground">—</span>;
      },
    },
    {
      id: "id", header: "Backup ID", meta: { wide: true },
      accessorFn: (r) => r.destinations.find((d) => d.ok && d.snapshot_id)?.snapshot_id?.slice(0, 8) ?? "",
      cell: ({ getValue }) => <span className="font-mono">{String(getValue() ?? "")}</span>,
    },
  ];
  return (
    <DataTable columns={columns} data={runs}
      emptyState={<p className="text-sm text-muted-foreground">No backups yet. Press Back up now, or turn on a schedule.</p>} />
  );
}
