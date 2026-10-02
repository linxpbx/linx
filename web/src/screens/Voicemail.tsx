// The Voicemail tab (docs/ui/SCREENS_PHASE1F.md §12.1, ADR-069): my
// messages and my ring groups', newest first, new ones on top. Play,
// call back, download, mark heard or new, delete. The audio loads only
// when played (a slow link pays for what it hears).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Download, MoreHorizontal, Pause, Phone, Play, Settings as SettingsIcon } from "lucide-react";
import { api, problemMessage, type Me, type TeamMember } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Slider } from "@/components/ui/slider";
import { VoicemailSettingsSheet } from "@/components/VoicemailBox";
import { hasScope } from "@/lib/roles";
import { clock, space } from "@/lib/voicemail";
import { cn } from "@/lib/utils";
import { usePhoneLine, usePhoneState } from "@/phone/context";

type List = components["schemas"]["VoicemailList"];
type Message = components["schemas"]["VoicemailMessage"];
type Box = components["schemas"]["VoicemailBox"];

const MINE = "mine";
const ALL = "all";

/** Who left it: "Aisha (103)", "050 123 4567", "Ahmed Ali (+971501234567)", "A withheld number". */
export function caller(m: Message): string {
  if (m.caller_extension_id) return `${m.caller_name} (${m.caller_number})`;
  if (m.caller_number && m.caller_name && m.caller_name !== m.caller_number) return `${m.caller_name} (${m.caller_number})`;
  return m.caller_number || m.caller_name || "A withheld number";
}

/** "10:42", "yesterday 16:05", "Mon 09:12", "3 Sep 09:12" */
export function when(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((day(now) - day(d)) / 86_400_000);
  if (days === 0) return time;
  if (days === 1) return `yesterday ${time}`;
  if (days > 1 && days < 7) return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${time}`;
  return `${d.toLocaleDateString(undefined, { day: "numeric", month: "short" })} ${time}`;
}

/** The player: ▶, a slider, the time; marks the message heard when it plays to the end. */
function Player({ m, onEnded }: { m: Message; onEnded: () => void }) {
  const audio = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState(false);
  const [at, setAt] = useState(0);
  const total = m.duration_ms / 1000;
  useEffect(() => () => audio.current?.pause(), []);
  const toggle = () => {
    if (!audio.current) {
      const a = new Audio(`/api/v1/voicemail/${m.id}/audio`);
      a.ontimeupdate = () => setAt(a.currentTime);
      a.onpause = () => setPlaying(false);
      a.onplay = () => setPlaying(true);
      a.onended = () => { setPlaying(false); setAt(0); onEnded(); };
      audio.current = a;
    }
    if (playing) audio.current.pause();
    else void audio.current.play().catch(() => setPlaying(false));
  };
  return (
    <div className="flex min-w-0 basis-full items-center gap-3 sm:basis-0 sm:flex-1">
      <Button size="icon" variant="outline" className="shrink-0 rounded-full" aria-label={playing ? "Pause" : `Play the message from ${caller(m)}`} onClick={toggle}>
        {playing ? <Pause aria-hidden="true" /> : <Play aria-hidden="true" />}
      </Button>
      <Slider aria-label="Position" className="min-w-16 flex-1" min={0} max={Math.max(1, Math.round(total * 10))} step={1}
        value={[Math.round(at * 10)]} onValueChange={([v]) => { const t = (v ?? 0) / 10; setAt(t); if (audio.current) audio.current.currentTime = t; }} />
      <span className="w-20 shrink-0 text-end font-mono text-xs tabular-nums text-muted-foreground">{clock(at)} / {clock(total)}</span>
    </div>
  );
}

function MessageRow({ m, box, showBox, canCall, onCall, onMark, onDelete }: {
  m: Message; box?: Box; showBox: boolean; canCall: boolean; onCall: () => void; onMark: (heard: boolean) => void; onDelete: () => void;
}) {
  const isNew = !m.heard_at;
  return (
    <li className="flex flex-col gap-3 rounded-lg border bg-card p-4" data-testid="voicemail-message">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span aria-hidden="true" className={cn("size-2 shrink-0 self-center rounded-full", isNew ? "bg-primary" : "bg-transparent")} />
        <span className="min-w-0 break-words font-medium">{caller(m)}{isNew && <span className="sr-only"> (new)</span>}</span>
        {showBox && box && <span className="text-sm text-muted-foreground">for {box.mine ? "me" : box.owner}</span>}
        <span className="ms-auto flex gap-3 text-sm text-muted-foreground">
          <span>{when(m.received_at)}</span>
          <span className="font-mono tabular-nums">{clock(m.duration_ms / 1000)}</span>
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Player m={m} onEnded={() => { if (isNew) onMark(true); }} />
        <div className="ms-auto flex gap-2">
          <Button size="sm" variant="outline" disabled={!canCall || !m.caller_number} onClick={onCall}
            title={!m.caller_number ? "The number was withheld" : undefined}>
            <Phone aria-hidden="true" /> Call back
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="icon" variant="ghost" aria-label={`More for the message from ${caller(m)}`}><MoreHorizontal aria-hidden="true" /></Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => onMark(isNew)}>{isNew ? "Mark as heard" : "Mark as new"}</DropdownMenuItem>
              <DropdownMenuItem asChild>
                <a href={`/api/v1/voicemail/${m.id}/audio?download=1`} download><Download aria-hidden="true" /> Download</a>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem className="text-destructive" onSelect={onDelete}>Delete…</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      {m.heard_at && m.heard_by && !m.heard_by_me && box && !box.mine && (
        <p className="text-xs text-muted-foreground">Heard by {m.heard_by}, {when(m.heard_at)}</p>
      )}
    </li>
  );
}

export function VoicemailScreen({ me, members, stamp }: { me: Me; members: TeamMember[] | null; stamp?: number }) {
  const line = usePhoneLine();
  const { status, call } = usePhoneState();
  const [view, setView] = useState(MINE);
  const [list, setList] = useState<List | null>(null);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<Message | null>(null);
  const [settings, setSettings] = useState(false);
  const admin = hasScope(me, "users:write");

  const load = useCallback(async () => {
    const query = view === ALL ? { all: true } : view === MINE ? {} : { box: view };
    const { data, error } = await api.GET("/api/v1/voicemail", { params: { query } });
    if (data) { setList(data); setError(""); } else setError(problemMessage(error, "Linx couldn't load your voicemail."));
  }, [view]);
  // A message arriving (or heard or deleted elsewhere) moves the Team
  // websocket's voicemail counter (stamp): load again with it.
  useEffect(() => { void load(); }, [load, stamp]);

  const boxes = useMemo(() => new Map((list?.boxes ?? []).map((b) => [b.id, b])), [list]);
  const own = list?.boxes.find((b) => b.mine);
  const groupBoxes = (list?.boxes ?? []).filter((b) => b.member);
  const canCall = status === "ready" && !call;

  const mark = async (m: Message, heard: boolean) => {
    const { error } = await api.PATCH("/api/v1/voicemail/{id}", { params: { path: { id: m.id } }, body: { heard } });
    if (error) setError(problemMessage(error)); else void load();
  };
  const remove = async () => {
    if (!deleting) return;
    const { error } = await api.DELETE("/api/v1/voicemail/{id}", { params: { path: { id: deleting.id } } });
    setDeleting(null);
    if (error) setError(problemMessage(error)); else void load();
  };
  const callBack = (m: Message) => {
    const name = m.caller_extension_id ? m.caller_name : members?.find((x) => x.extension === m.caller_number)?.name ?? (m.caller_name || undefined);
    line.call(m.caller_number, name);
  };

  const items = list?.items ?? [];
  const fresh = items.filter((m) => !m.heard_at);
  const heard = items.filter((m) => m.heard_at);
  const shownBoxes = view === ALL ? list?.boxes ?? [] : view === MINE ? (list?.boxes ?? []).filter((b) => b.mine || b.member) : (list?.boxes ?? []).filter((b) => b.id === view);
  const bytes = shownBoxes.reduce((n, b) => n + b.bytes, 0);
  const showBox = view !== MINE || groupBoxes.length > 0;
  const row = (m: Message) => (
    <MessageRow key={m.id} m={m} box={boxes.get(m.box_id)} showBox={showBox} canCall={canCall}
      onCall={() => callBack(m)} onMark={(h) => void mark(m, h)} onDelete={() => setDeleting(m)} />
  );

  return (
    <div className="w-full max-w-4xl px-4 py-6 md:px-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="me-auto font-display text-3xl font-semibold tracking-tight">Voicemail</h1>
        {(groupBoxes.length > 0 || admin) && (
          <Select value={view} onValueChange={setView}>
            <SelectTrigger aria-label="Whose voicemail" className="w-44"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={MINE}>Mine</SelectItem>
              {groupBoxes.map((b) => <SelectItem key={b.id} value={b.id}>{b.owner}</SelectItem>)}
              {admin && <SelectItem value={ALL}>All boxes</SelectItem>}
            </SelectContent>
          </Select>
        )}
        {own && (
          <Button variant="outline" onClick={() => setSettings(true)}><SettingsIcon aria-hidden="true" /> Settings</Button>
        )}
      </div>
      {own && !own.enabled && (
        <p role="status" className="mt-4 rounded-md border bg-card p-3 text-sm">
          Your voicemail is off: callers hear &ldquo;not available&rdquo;. Turn it on in <button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => setSettings(true)}>Settings</button>.
        </p>
      )}
      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}

      {!list ? <div className="mt-6 h-40" aria-busy="true" /> : items.length === 0 ? (
        <div className="mt-10 flex flex-col items-center gap-2 rounded-lg border border-dashed p-10 text-center">
          <p className="font-medium">No voicemail</p>
          <p className="max-w-md text-sm text-muted-foreground">When someone leaves you a message, it appears here and (if you like) in your email.</p>
        </div>
      ) : (
        <div className="mt-6 flex flex-col gap-6">
          {fresh.length > 0 && (
            <section aria-labelledby="vm-new" className="flex flex-col gap-2">
              <h2 id="vm-new" className="text-xs font-medium tracking-wide text-muted-foreground">NEW</h2>
              <ul className="flex flex-col gap-2">{fresh.map(row)}</ul>
            </section>
          )}
          {heard.length > 0 && (
            <section aria-labelledby="vm-heard" className="flex flex-col gap-2">
              <h2 id="vm-heard" className="text-xs font-medium tracking-wide text-muted-foreground">HEARD</h2>
              <ul className="flex flex-col gap-2">{heard.map(row)}</ul>
            </section>
          )}
        </div>
      )}
      {list && (
        <p className="mt-6 text-sm text-muted-foreground">
          Kept {list.keep_days} days · {shownBoxes.length === 1 ? "this box uses" : "these boxes use"} {space(bytes)}
        </p>
      )}

      <Dialog open={!!deleting} onOpenChange={(o) => { if (!o) setDeleting(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this voicemail?</DialogTitle>
            <DialogDescription>
              {deleting && `The message from ${caller(deleting)} (${clock(deleting.duration_ms / 1000)}) is deleted for everyone who sees this box. It can't be brought back.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleting(null)}>Cancel</Button>
            <Button variant="destructive" onClick={() => void remove()}>Delete</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {own && <VoicemailSettingsSheet boxId={own.id} title="Voicemail settings" open={settings} onOpenChange={(o) => { setSettings(o); if (!o) void load(); }} />}
    </div>
  );
}
