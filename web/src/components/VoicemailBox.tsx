// A voicemail box's settings (docs/ui/SCREENS_PHASE1F.md §12.3-12.5):
// on or off, its greetings (Linx's own or a recording made here), and
// email. The same panel is Voicemail → Settings for my own box, a ring
// group's Voicemail section, and People's Voicemail line opens it too.
import { useCallback, useEffect, useRef, useState } from "react";
import { Mic, Pause, Play, Square } from "lucide-react";
import { api, csrfToken, CSRF_HEADER, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { navigate } from "@/hooks/useRoute";
import { loadAudioSettings } from "@/phone/settings";
import { clock } from "@/lib/voicemail";
import { GREETING_MAX_SECONDS, greetingWav } from "@/lib/wav";
import { cn } from "@/lib/utils";

export type BoxSettings = components["schemas"]["VoicemailBoxSettings"];
type Kind = "unavailable" | "closed";

const LINX_OWN: Record<Kind, string> = {
  unavailable: "“The person you called isn’t available. Please leave a message after the tone.”",
  closed: "“We’re closed right now. Please leave a message after the tone.”",
};

function shortDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

/** One audio file played from a button (a greeting's ▶). */
function usePlayer() {
  const audio = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState("");
  const stop = useCallback(() => { audio.current?.pause(); setPlaying(""); }, []);
  const play = useCallback((key: string, src: string) => {
    audio.current?.pause();
    const a = new Audio(src);
    audio.current = a;
    a.onended = () => setPlaying("");
    setPlaying(key);
    void a.play().catch(() => setPlaying(""));
  }, []);
  useEffect(() => () => audio.current?.pause(), []);
  return { playing, play, stop };
}

// --- Recording a greeting (§12.4) ---

type RecState = { step: "idle" } | { step: "recording"; started: number } | { step: "recorded"; blob: Blob; url: string; seconds: number }
  | { step: "saving"; blob: Blob; url: string; seconds: number } | { step: "saved" };

export function GreetingRecorder({ open, onOpenChange, boxId, kind, group, onSaved }: {
  open: boolean; onOpenChange: (o: boolean) => void; boxId: string; kind: Kind; group?: string; onSaved: () => void;
}) {
  const [state, setState] = useState<RecState>({ step: "idle" });
  const [error, setError] = useState("");
  const [levels, setLevels] = useState<number[]>(() => Array(16).fill(0));
  const [now, setNow] = useState(0);
  const recorder = useRef<MediaRecorder | null>(null);
  const stream = useRef<MediaStream | null>(null);
  const meter = useRef<{ ctx: AudioContext; frame: number } | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const preview = usePlayer();

  const release = useCallback(() => {
    window.clearInterval(timer.current);
    if (meter.current) { cancelAnimationFrame(meter.current.frame); void meter.current.ctx.close(); meter.current = null; }
    stream.current?.getTracks().forEach((t) => t.stop());
    stream.current = null;
  }, []);
  useEffect(() => {
    if (!open) {
      release();
      preview.stop();
      if (recorder.current?.state === "recording") recorder.current.stop();
      setState({ step: "idle" });
      setError("");
    }
  }, [open, release, preview]);
  useEffect(() => release, [release]);

  const stop = () => { if (recorder.current?.state === "recording") recorder.current.stop(); };

  const start = async () => {
    setError("");
    preview.stop();
    const mic = loadAudioSettings().microphoneId;
    let s: MediaStream;
    try {
      s = await navigator.mediaDevices.getUserMedia({ audio: mic ? { deviceId: { exact: mic } } : true });
    } catch {
      setError("This browser isn't letting Linx use the microphone. Allow it in the address bar, then try again.");
      return;
    }
    stream.current = s;
    const chunks: Blob[] = [];
    const r = new MediaRecorder(s);
    recorder.current = r;
    r.ondataavailable = (e) => { if (e.data.size) chunks.push(e.data); };
    r.onstop = () => {
      const seconds = Math.min((performance.now() - started) / 1000, GREETING_MAX_SECONDS);
      release();
      const blob = new Blob(chunks, { type: r.mimeType });
      setState({ step: "recorded", blob, url: URL.createObjectURL(blob), seconds });
    };
    const started = performance.now();
    r.start();
    setState({ step: "recording", started });
    setNow(0);
    timer.current = window.setInterval(() => {
      const t = (performance.now() - started) / 1000;
      setNow(t);
      if (t >= GREETING_MAX_SECONDS) stop();
    }, 200);
    // The level meter: how loud, so people see the microphone hears them.
    const ctx = new AudioContext();
    const analyser = ctx.createAnalyser();
    analyser.fftSize = 512;
    ctx.createMediaStreamSource(s).connect(analyser);
    const buf = new Uint8Array(analyser.fftSize);
    let last = 0;
    const tick = (t: number) => {
      if (!meter.current) return;
      meter.current.frame = requestAnimationFrame(tick);
      if (t - last < 80) return;
      last = t;
      analyser.getByteTimeDomainData(buf);
      let sum = 0;
      for (const b of buf) sum += ((b - 128) / 128) ** 2;
      const level = Math.min(1, Math.sqrt(sum / buf.length) * 4);
      setLevels((l) => [...l.slice(1), level]);
    };
    meter.current = { ctx, frame: requestAnimationFrame(tick) };
  };

  const use = async () => {
    if (state.step !== "recorded") return;
    setState({ ...state, step: "saving" });
    setError("");
    try {
      const { wav } = await greetingWav(state.blob);
      const res = await fetch(`/api/v1/voicemail-boxes/${boxId}/greetings/${kind}`, {
        method: "PUT", credentials: "same-origin", body: new Blob([wav as BlobPart], { type: "audio/wav" }),
        headers: { "Content-Type": "audio/wav", [CSRF_HEADER]: csrfToken() },
      });
      if (!res.ok) {
        setError(problemMessage(await res.json().catch(() => null), "Linx couldn't keep the greeting. Try again."));
        setState({ ...state, step: "recorded" });
        return;
      }
      URL.revokeObjectURL(state.url);
      setState({ step: "saved" });
      onSaved();
    } catch {
      setError("This browser couldn't prepare the recording. Try again, or try another browser.");
      setState({ ...state, step: "recorded" });
    }
  };

  const example = kind === "closed"
    ? `\u201cThanks for calling${group ? ` ${group}` : ""}. We\u2019re closed right now. Leave a message and we\u2019ll call you back.\u201d`
    : group ? `\u201cYou\u2019ve reached ${group}. Leave a message and we\u2019ll call you back.\u201d`
      : `\u201cHi, this is Sara. Leave a message and I\u2019ll call you back.\u201d`;
  const recorded = state.step === "recorded" || state.step === "saving";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{kind === "closed" ? "Record your “we’re closed” greeting" : "Record your greeting"}</DialogTitle>
          <DialogDescription>Say something like: {example}</DialogDescription>
        </DialogHeader>
        {state.step === "saved" ? (
          <p role="status" className="rounded-md bg-card p-3 text-sm">Saved. Callers hear it from now on.</p>
        ) : (
          <div className="flex flex-col gap-4">
            <div className="flex flex-wrap items-center gap-3">
              {state.step === "recording" ? (
                <Button variant="destructive" onClick={stop}><Square aria-hidden="true" /> Stop</Button>
              ) : !recorded && (
                <Button onClick={() => void start()}><Mic aria-hidden="true" /> Record</Button>
              )}
              <span className="font-mono text-sm tabular-nums text-muted-foreground" aria-live="off">
                {clock(state.step === "recording" ? now : recorded ? state.seconds : 0)} / {clock(GREETING_MAX_SECONDS)}
              </span>
            </div>
            <div aria-hidden="true" className="flex h-8 items-end gap-1">
              {levels.map((l, i) => (
                <span key={i} className={cn("w-2 rounded-sm", state.step === "recording" ? "bg-primary" : "bg-muted-foreground/30")}
                  style={{ height: `${Math.max(8, Math.round((state.step === "recording" ? l : 0) * 100))}%` }} />
              ))}
            </div>
            {recorded && (
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" onClick={() => preview.playing ? preview.stop() : preview.play("take", state.url)}>
                  {preview.playing ? <Pause aria-hidden="true" /> : <Play aria-hidden="true" />} {preview.playing ? "Stop" : "Play"}
                </Button>
                <Button variant="outline" disabled={state.step === "saving"} onClick={() => { URL.revokeObjectURL(state.url); void start(); }}>Try again</Button>
              </div>
            )}
            {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
          </div>
        )}
        <DialogFooter>
          {state.step === "saved" ? (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          ) : (
            <>
              <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
              <Button disabled={!recorded || state.step === "saving"} aria-busy={state.step === "saving"} onClick={() => void use()}>Use this</Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// --- The settings (§12.3) ---

function GreetingChoice({ box, kind, onChange, onRecord }: {
  box: BoxSettings; kind: Kind; onChange: (own: boolean) => void; onRecord: () => void;
}) {
  const g = box.greetings[kind];
  const player = usePlayer();
  const id = `vm-${kind}`;
  const src = `/api/v1/voicemail-boxes/${box.box.id}/greetings/${kind}?at=${encodeURIComponent(g.recorded_at ?? "")}`;
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1 text-sm font-semibold">{kind === "closed" ? "Outside office hours" : "Greeting"}</legend>
      <RadioGroup value={g.in_use ? "own" : "linx"} onValueChange={(v) => onChange(v === "own")} className="gap-3">
        <div className="flex items-start gap-2">
          <RadioGroupItem id={`${id}-linx`} value="linx" className="mt-0.5" />
          <Label htmlFor={`${id}-linx`} className="flex-col items-start gap-0.5 font-normal">
            <span>Linx&apos;s own</span>
            <span className="text-sm text-muted-foreground">{LINX_OWN[kind]}</span>
          </Label>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <RadioGroupItem id={`${id}-own`} value="own" disabled={!g.recorded} />
          <Label htmlFor={`${id}-own`} className={cn("font-normal", !g.recorded && "text-muted-foreground")}>
            {box.box.kind === "ring_group" ? "Our own" : "My own"}
            {g.recorded && g.recorded_at ? <span className="text-muted-foreground">· recorded {shortDate(g.recorded_at)}, {clock((g.duration_ms ?? 0) / 1000)}</span> : <span>· not recorded yet</span>}
          </Label>
          <span className="ms-auto flex gap-1">
            {g.recorded && (
              <Button size="sm" variant="ghost" aria-label={player.playing ? "Stop" : `Play ${kind === "closed" ? "the closed greeting" : "the greeting"}`}
                onClick={() => player.playing ? player.stop() : player.play(kind, src)}>
                {player.playing ? <Pause aria-hidden="true" /> : <Play aria-hidden="true" />}
              </Button>
            )}
            <Button size="sm" variant="outline" onClick={onRecord}>{g.recorded ? "Record again" : "Record"}</Button>
          </span>
        </div>
      </RadioGroup>
    </fieldset>
  );
}

/** The settings of box id, loaded and saved here. */
export function VoicemailBoxPanel({ boxId, groupName, onChanged }: { boxId: string; groupName?: string; onChanged?: (b: BoxSettings) => void }) {
  const [box, setBox] = useState<BoxSettings | null>(null);
  const [error, setError] = useState("");
  const [recording, setRecording] = useState<Kind | null>(null);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/api/v1/voicemail-boxes/{id}", { params: { path: { id: boxId } } });
    if (data) { setBox(data); onChanged?.(data); } else setError(problemMessage(error, "Linx couldn't load this voicemail box."));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [boxId]);
  useEffect(() => { void load(); }, [load]);

  const save = async (body: components["schemas"]["VoicemailBoxPatch"]) => {
    setError("");
    const { data, error } = await api.PATCH("/api/v1/voicemail-boxes/{id}", { params: { path: { id: boxId } }, body });
    if (data) { setBox(data); onChanged?.(data); } else setError(problemMessage(error));
  };

  if (!box) return error ? <p role="alert" className="text-sm text-destructive">{error}</p> : <div className="h-40" aria-busy="true" />;
  const group = box.box.kind === "ring_group";
  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <Label htmlFor="vm-on" className="text-sm font-semibold">Voicemail</Label>
          <p className="text-sm text-muted-foreground">
            {box.box.enabled
              ? group ? "Callers who aren't answered can leave a message." : "Callers who don't reach you can leave a message."
              : "Off: callers hear “not available” instead."}
          </p>
        </div>
        <Switch id="vm-on" checked={box.box.enabled} disabled={!box.can_switch}
          title={box.can_switch ? undefined : "Only an admin can turn a ring group's voicemail off"}
          onCheckedChange={(v) => void save({ enabled: v })} />
      </div>
      <GreetingChoice box={box} kind="unavailable" onRecord={() => setRecording("unavailable")}
        onChange={(own) => void save({ greetings: { unavailable: own ? "own" : "linx" } })} />
      <GreetingChoice box={box} kind="closed" onRecord={() => setRecording("closed")}
        onChange={(own) => void save({ greetings: { closed: own ? "own" : "linx" } })} />
      {!group && (
        <div className="flex items-start justify-between gap-4">
          <div>
            <Label htmlFor="vm-email" className={cn("text-sm font-semibold", !box.email_ready && !box.box.email && "text-muted-foreground")}>Email me new messages</Label>
            {box.email_ready || box.box.email ? (
              <p className="text-sm text-muted-foreground break-words">
                {box.email_to ? <>To <span className="text-foreground">{box.email_to}</span>, with the audio.</> : "Nobody signs in to this box, so there's no address to email."}
              </p>
            ) : (
              <p className="text-sm text-muted-foreground">
                Set up email first (<button type="button" className="text-link underline-offset-4 hover:underline" onClick={() => navigate("/admin/system/settings")}>System → Settings</button>).
              </p>
            )}
          </div>
          <Switch id="vm-email" checked={box.box.email} disabled={!box.can_switch || (!box.email_ready && !box.box.email)}
            onCheckedChange={(v) => void save({ email: v })} />
        </div>
      )}
      {group && <p className="text-sm text-muted-foreground">Everyone in the group sees its messages in Voicemail. A group&apos;s messages aren&apos;t emailed.</p>}
      {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
      <GreetingRecorder open={!!recording} onOpenChange={(o) => { if (!o) setRecording(null); }} boxId={boxId} kind={recording ?? "unavailable"}
        group={group ? groupName ?? box.box.owner : undefined} onSaved={() => void load()} />
    </div>
  );
}

/** Voicemail → Settings: the sheet around the panel. */
export function VoicemailSettingsSheet({ boxId, title, open, onOpenChange }: { boxId: string; title: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>What callers hear, and where new messages go.</SheetDescription>
        </SheetHeader>
        <div className="overflow-y-auto px-4 pb-4">
          {open && <VoicemailBoxPanel boxId={boxId} />}
        </div>
      </SheetContent>
    </Sheet>
  );
}

/** People → person detail's Voicemail line (§12.5): "On · 3 messages · email on", with Turn off. */
export function VoicemailLine({ boxId }: { boxId: string }) {
  const [box, setBox] = useState<BoxSettings | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void api.GET("/api/v1/voicemail-boxes/{id}", { params: { path: { id: boxId } } }).then(({ data }) => setBox(data ?? null));
  }, [boxId]);
  if (!box) return null;
  const turn = async (enabled: boolean) => {
    const { data, error } = await api.PATCH("/api/v1/voicemail-boxes/{id}", { params: { path: { id: boxId } }, body: { enabled } });
    if (data) { setBox(data); setError(""); } else setError(problemMessage(error));
  };
  const b = box.box;
  return (
    <section>
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">Voicemail</h3>
        {box.can_switch && <Button size="sm" variant="outline" onClick={() => void turn(!b.enabled)}>{b.enabled ? "Turn off" : "Turn on"}</Button>}
      </div>
      <p className="mt-2 text-sm text-muted-foreground">
        {b.enabled ? "On" : "Off (callers hear \u201cnot available\u201d)"} · {b.messages} {b.messages === 1 ? "message" : "messages"}
        {b.new > 0 && `, ${b.new} new`} · email {b.email ? "on" : "off"}
        {box.greetings.unavailable.in_use && " · own greeting"}
      </p>
      {error && <p role="alert" className="mt-1 text-sm font-medium text-destructive">{error}</p>}
    </section>
  );
}
