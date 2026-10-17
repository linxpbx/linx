// The active call panel (WEB_SCREENS_PHASE1C.md §5) and the incoming call
// overlay (§4).
import { useEffect, useRef, useState } from "react";
import { Expand, Grid3x3, Lock, Mic, MicOff, Phone, PhoneOff, UserPlus, Video, VideoOff } from "lucide-react";
import { Keypad } from "@/components/Keypad";
import { Avatar, useElapsed } from "@/components/presence";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { usePhoneLine } from "@/phone/context";
import type { Call } from "@/phone/line";
import type { GroupPerson } from "@/phone/group";
import { useTeam } from "@/hooks/useTeam";
import { cn } from "@/lib/utils";

function ConnectionChip({ call }: { call: Call }) {
  const c = call.connection;
  let text = "Connecting…";
  if (call.phase === "reconnecting") text = "Reconnecting…";
  else if (call.phase === "calling") text = "Calling…";
  else if (c) text = `${c.mode === "relayed" ? "Relayed" : "Direct"}${c.rttMs !== undefined ? ` · ${c.rttMs} ms` : ""}`;
  return (
    <span data-testid="connection" data-mode={c?.mode ?? ""} data-relay-protocol={c?.relayProtocol ?? ""}
      data-audio-in={c?.audioBytesIn ?? 0}
      className="inline-flex items-center gap-1.5 rounded-full bg-sidebar-foreground/10 px-2.5 py-1 text-xs text-sidebar-foreground"
      title="Audio is encrypted end to end between your browser and Linx.">
      <Lock aria-hidden="true" className="size-3" />
      <span aria-live="polite">{text}</span>
    </span>
  );
}

function RoundButton({ label, onClick, pressed, disabled, variant = "plain", children }: {
  label: string; onClick?: () => void; pressed?: boolean; disabled?: boolean;
  variant?: "plain" | "end" | "call" | "video"; children: React.ReactNode;
}) {
  return (
    <button type="button" aria-label={label} title={label} onClick={onClick} disabled={disabled}
      aria-pressed={pressed}
      className={cn(
        "inline-flex size-12 items-center justify-center rounded-full transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40 [&_svg]:size-5",
        variant === "end" && "bg-hangup text-hangup-foreground hover:bg-hangup/90",
        variant === "call" && "bg-call text-call-foreground hover:bg-call/90",
        variant === "video" && "bg-primary text-primary-foreground hover:bg-primary/90",
        variant === "plain" && (pressed ? "bg-sidebar-foreground text-sidebar"
          : "bg-sidebar-foreground/10 text-sidebar-foreground hover:bg-sidebar-foreground/20"),
      )}>
      {children}
    </button>
  );
}

function Stream({ stream, label, mirrored, className }: {
  stream: MediaStream | null; label: string; mirrored?: boolean; className?: string;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const v = ref.current;
    if (v && v.srcObject !== stream) v.srcObject = stream;
  }, [stream]);
  return (
    <video ref={ref} autoPlay playsInline muted aria-label={label}
      className={cn("bg-black object-contain", mirrored && "-scale-x-100", className)} />
  );
}

/** The pictures (ADR-087): theirs large, this browser's own in a corner. */
function Pictures({ call }: { call: Call }) {
  const line = usePhoneLine();
  const box = useRef<HTMLDivElement>(null);
  const p = call.picture;
  if (!p || (!p.mine && !p.theirs)) return null;
  const { mine, theirs } = line.pictureStreams();
  const fullScreen = () => void box.current?.requestFullscreen?.().catch(() => undefined);
  return (
    <div ref={box} data-testid="pictures" data-theirs={p.theirs} data-mine={p.mine}
      className="relative mt-4 aspect-video w-full overflow-hidden rounded-md bg-black">
      {p.theirs ? (
        <Stream stream={theirs} label={`${call.peer.name}'s picture`} className="size-full" />
      ) : (
        <Stream stream={mine} label="Your picture" mirrored className="size-full" />
      )}
      {p.theirs && p.mine && (
        <Stream stream={mine} label="Your picture" mirrored
          className="absolute end-2 bottom-2 w-1/3 rounded border border-white/30 shadow-md" />
      )}
      <button type="button" aria-label="Full screen" title="Full screen" onClick={fullScreen}
        className="absolute end-2 top-2 inline-flex size-8 items-center justify-center rounded-full bg-black/50 text-white hover:bg-black/70 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 [&_svg]:size-4">
        <Expand />
      </button>
    </div>
  );
}

/** One person's tile in a group call: their picture, or their initials. */
function PersonTile({ person }: { person: GroupPerson }) {
  const line = usePhoneLine();
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    if (person.video) line.attachGroupVideo(person.id, ref.current);
  }, [line, person.id, person.video]);
  return (
    <div data-testid="group-person" data-me={person.me} data-video={person.video}
      className={cn("relative flex aspect-video items-center justify-center overflow-hidden rounded-md bg-black",
        person.speaking && "ring-2 ring-call")}>
      {person.video ? (
        <video ref={ref} autoPlay playsInline muted aria-label={`${person.name}'s picture`}
          className={cn("size-full object-cover", person.me && "-scale-x-100")} />
      ) : (
        <Avatar name={person.name} size="lg" className="border-white/20 bg-white/10 text-white" />
      )}
      <span className="absolute start-1.5 bottom-1.5 max-w-[85%] truncate rounded bg-black/60 px-1.5 py-0.5 text-xs text-white">
        {person.me ? "You" : person.name}
      </span>
    </div>
  );
}

/** Everyone in a group call. */
function GroupGrid({ call }: { call: Call }) {
  const g = call.group;
  if (!g) return null;
  return (
    <div className="mt-4">
      <p role="status" className="text-sm text-sidebar-foreground/80">
        {g.phase === "joining" ? "Joining the group call…" : g.phase === "failed" ? (g.problem ?? "The group call dropped.")
          : `Group call · ${g.people.length} ${g.people.length === 1 ? "person" : "people"}`}
      </p>
      <div data-testid="group-grid" className="mt-2 grid grid-cols-2 gap-2">
        {g.people.map((p) => <PersonTile key={p.id} person={p} />)}
      </div>
    </div>
  );
}

/** Add person (ADR-088): someone from the Team list, rung into the call. */
function AddPerson({ call, disabled }: { call: Call; disabled: boolean }) {
  const line = usePhoneLine();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [problem, setProblem] = useState<string>();
  const [busy, setBusy] = useState(false);
  const { members } = useTeam(open);
  const q = query.trim().toLowerCase();
  const choices = (members ?? []).filter((m) => m.extension !== call.peer.number
    && (!q || m.name.toLowerCase().includes(q) || m.extension.startsWith(q))).slice(0, 6);
  const add = async (extension: string) => {
    setBusy(true);
    setProblem(undefined);
    const why = await line.addPerson(extension);
    setBusy(false);
    if (why) setProblem(why);
    else {
      setOpen(false);
      setQuery("");
    }
  };
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <span title={disabled ? "Only a call between two Linx apps or browsers can become a group call." : "Add person"}>
          <RoundButton label="Add person" pressed={open} disabled={disabled}><UserPlus /></RoundButton>
        </span>
      </PopoverTrigger>
      <PopoverContent className="w-72 border-sidebar-foreground/20 bg-sidebar p-3 text-sidebar-foreground" align="center">
        <label htmlFor="add-person" className="text-sm font-medium">Add someone to this call</label>
        <input id="add-person" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Name or extension"
          className="mt-2 h-9 w-full rounded-md border border-sidebar-foreground/20 bg-transparent px-2 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50" />
        <ul className="mt-2 flex flex-col">
          {choices.map((m) => (
            <li key={m.extension}>
              <button type="button" disabled={busy} onClick={() => void add(m.extension)}
                className="flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-start text-sm hover:bg-sidebar-foreground/10 disabled:opacity-50">
                <span className="truncate">{m.name}</span>
                <span className="font-mono text-xs text-sidebar-foreground/70">{m.extension}</span>
              </button>
            </li>
          ))}
        </ul>
        {problem && <p role="alert" className="mt-2 text-sm">{problem}</p>}
      </PopoverContent>
    </Popover>
  );
}

export function CallPanel({ call }: { call: Call }) {
  const line = usePhoneLine();
  const elapsed = useElapsed(call.answeredAt);
  const [keypadOpen, setKeypadOpen] = useState(false);
  const live = call.phase === "active" || call.phase === "reconnecting";
  const p = call.picture;
  const g = call.group;
  const cameraOn = g ? g.camera : !!p?.mine;
  const cameraLabel = cameraOn ? "Turn camera off" : "Turn camera on";
  const cameraTitle = !g && p?.available === false ? "Their phone can't show a picture, so this call is sound only." : cameraLabel;
  const canCamera = g ? g.phase === "in" : !!p?.available && !p.starting;
  return (
    <section aria-label="Active call" data-testid="call-panel" data-phase={call.phase}
      data-picture={p?.available === undefined || p.available === null ? "" : String(p.available)}
      className="rounded-lg border border-sidebar-foreground/15 bg-sidebar p-5 text-sidebar-foreground shadow-lg">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs text-sidebar-foreground/70">{call.phase === "calling" ? "Calling" : "Active call"}</span>
        <ConnectionChip call={call} />
      </div>
      <div className="mt-4 flex items-center gap-3">
        <Avatar name={call.peer.name} size="lg" className="border-sidebar-foreground/20 bg-sidebar-foreground/10 text-sidebar-foreground" />
        <div className="min-w-0 flex-1">
          <p className="truncate font-display text-lg font-semibold">{call.peer.name}</p>
          {call.peer.name !== call.peer.number && <p className="text-sm text-sidebar-foreground/70">Ext {call.peer.number}</p>}
        </div>
        <span className="font-mono text-xl tabular-nums" data-testid="call-timer">
          {live ? elapsed : "Calling…"}
        </span>
      </div>
      {call.group ? <GroupGrid call={call} /> : <Pictures call={call} />}
      {p?.problem && <p role="status" className="mt-3 text-sm text-sidebar-foreground/80">{p.problem}</p>}
      <div className="mt-5 flex items-center gap-3">
        <RoundButton label={call.muted ? "Unmute" : "Mute"} pressed={call.muted} disabled={!live} onClick={() => line.toggleMute()}>
          {call.muted ? <MicOff /> : <Mic />}
        </RoundButton>
        <Popover open={keypadOpen} onOpenChange={setKeypadOpen}>
          <PopoverTrigger asChild>
            <span>
              <RoundButton label="Keypad" pressed={keypadOpen} disabled={!live}><Grid3x3 /></RoundButton>
            </span>
          </PopoverTrigger>
          <PopoverContent className="w-auto border-sidebar-foreground/20 bg-sidebar p-4" align="center">
            <Keypad size="sm" dark onKey={(k) => line.sendTone(k)} />
          </PopoverContent>
        </Popover>
        <span title={cameraTitle}>
          <RoundButton label={cameraLabel} pressed={cameraOn} disabled={!live || !canCamera}
            onClick={() => line.toggleVideo()}>
            {cameraOn ? <Video /> : <VideoOff />}
          </RoundButton>
        </span>
        {!g && p?.mine && (
          <RoundButton label={p.hd ? "Turn HD off" : "Turn HD on"} pressed={!!p.hd} disabled={!live}
            onClick={() => line.toggleHD()}>
            <span className="text-sm font-semibold">HD</span>
          </RoundButton>
        )}
        <AddPerson call={call} disabled={!live || (!g && !p?.available) || (!!g && g.phase !== "in")} />
      </div>
      <button type="button" onClick={() => line.hangUp()}
        className="mt-4 inline-flex h-11 w-full items-center justify-center gap-2 rounded-md bg-hangup font-medium text-hangup-foreground hover:bg-hangup/90 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">
        <PhoneOff aria-hidden="true" className="size-4" />
        End call
      </button>
    </section>
  );
}

export function IncomingCall({ call }: { call: Call }) {
  const line = usePhoneLine();
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-sidebar/60 p-4" role="alertdialog"
      aria-modal="true" aria-labelledby="incoming-name" aria-describedby="incoming-what" data-testid="incoming-call">
      <div className="w-full max-w-xs rounded-lg border border-sidebar-foreground/15 bg-sidebar p-8 text-center text-sidebar-foreground shadow-xl">
        <p id="incoming-what" className="text-sm text-sidebar-foreground/70">{call.invitation ? "Invites you to a group call" : call.videoCall ? "Incoming video call" : "Incoming call"}</p>
        <Avatar name={call.peer.name} size="xl" className="mx-auto mt-5 border-sidebar-foreground/20 bg-sidebar-foreground/10 text-sidebar-foreground" />
        <p id="incoming-name" className="mt-4 font-display text-2xl font-semibold">{call.peer.name}</p>
        {call.peer.name !== call.peer.number && <p className="mt-1 text-sidebar-foreground/70">Ext {call.peer.number}</p>}
        <div className="mt-8 flex justify-center gap-10">
          <div className="flex flex-col items-center gap-2">
            <RoundButton label="Decline" variant="end" onClick={() => line.decline()}><PhoneOff /></RoundButton>
            <span className="text-sm" aria-hidden="true">Decline</span>
          </div>
          <div className="flex flex-col items-center gap-2">
            <RoundButton label="Answer" variant={call.videoCall ? "video" : "call"} onClick={() => line.answer()}>
              {call.videoCall ? <Video /> : <Phone />}
            </RoundButton>
            <span className="text-sm" aria-hidden="true">Answer</span>
          </div>
        </div>
      </div>
    </div>
  );
}
