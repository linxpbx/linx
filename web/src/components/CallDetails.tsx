// A call's detail (docs/ui/SCREENS_PHASE1F.md §13): its way through Linx
// in plain sentences, the line it used, and the voicemail left, which
// plays here when the viewer may hear it (the server decides: their own
// box, their ring group's, or any box for an admin).
import { useEffect, useRef, useState } from "react";
import { Pause, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { talkClock, type CallRecord } from "@/lib/calls";
import { clock } from "@/lib/voicemail";

function Listen({ id, label }: { id: string; label: string }) {
  const audio = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => () => audio.current?.pause(), []);
  const toggle = () => {
    if (!audio.current) {
      const a = new Audio(`/api/v1/voicemail/${id}/audio`);
      a.onplay = () => setPlaying(true);
      a.onpause = () => setPlaying(false);
      a.onended = () => setPlaying(false);
      a.onerror = () => { setPlaying(false); setFailed(true); };
      audio.current = a;
    }
    if (playing) audio.current.pause();
    else void audio.current.play().catch(() => setPlaying(false));
  };
  if (failed) return <span className="text-muted-foreground">Only whoever the voicemail is for can hear it.</span>;
  return (
    <Button size="sm" variant="outline" onClick={toggle} aria-label={playing ? "Pause the voicemail" : `Listen to ${label}`}>
      {playing ? <Pause aria-hidden="true" /> : <Play aria-hidden="true" />} {playing ? "Pause" : "Listen"}
    </Button>
  );
}

export function CallDetails({ c }: { c: CallRecord }) {
  return (
    <div className="flex flex-col gap-2 text-sm">
      {c.steps.length > 0 && (
        <ol className="flex flex-col gap-1">
          {c.steps.map((s, i) => (
            <li key={i} className="flex gap-2"><span aria-hidden="true" className="text-muted-foreground">{i + 1}.</span><span className="min-w-0 break-words">{s}</span></li>
          ))}
        </ol>
      )}
      <dl className="grid grid-cols-[7rem_minmax(0,1fr)] gap-y-1 text-muted-foreground">
        <dt>Started</dt><dd className="text-foreground">{new Date(c.started_at).toLocaleString()}</dd>
        {c.answered_by && <><dt>Answered by</dt><dd className="break-words text-foreground">{c.answered_by}</dd></>}
        {c.result === "answered" && <><dt>Talked</dt><dd className="font-mono text-foreground">{talkClock(c.talk_seconds)}</dd></>}
        {c.line && <><dt>Line</dt><dd className="break-words text-foreground">{c.line}</dd></>}
        {c.to.number && c.direction === "inbound" && <><dt>Number called</dt><dd className="font-mono text-foreground">{c.to.number}</dd></>}
      </dl>
      {c.voicemail?.id && (
        <div className="flex flex-wrap items-center gap-2">
          <span>Voicemail for {c.voicemail.box} ({clock((c.voicemail.duration_ms ?? 0) / 1000)})</span>
          <Listen id={c.voicemail.id} label={`the voicemail for ${c.voicemail.box}`} />
        </div>
      )}
    </div>
  );
}
