// The active call panel (WEB_SCREENS_PHASE1C.md §5) and the incoming call
// overlay (§4).
import { useState } from "react";
import { Grid3x3, Lock, Mic, MicOff, Phone, PhoneOff } from "lucide-react";
import { Keypad } from "@/components/Keypad";
import { Avatar, useElapsed } from "@/components/presence";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { usePhoneLine } from "@/phone/context";
import type { Call } from "@/phone/line";
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
  variant?: "plain" | "end" | "call"; children: React.ReactNode;
}) {
  return (
    <button type="button" aria-label={label} title={label} onClick={onClick} disabled={disabled}
      aria-pressed={pressed}
      className={cn(
        "inline-flex size-12 items-center justify-center rounded-full transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-40 [&_svg]:size-5",
        variant === "end" && "bg-hangup text-hangup-foreground hover:bg-hangup/90",
        variant === "call" && "bg-call text-call-foreground hover:bg-call/90",
        variant === "plain" && (pressed ? "bg-sidebar-foreground text-sidebar"
          : "bg-sidebar-foreground/10 text-sidebar-foreground hover:bg-sidebar-foreground/20"),
      )}>
      {children}
    </button>
  );
}

export function CallPanel({ call }: { call: Call }) {
  const line = usePhoneLine();
  const elapsed = useElapsed(call.answeredAt);
  const [keypadOpen, setKeypadOpen] = useState(false);
  const live = call.phase === "active" || call.phase === "reconnecting";
  return (
    <section aria-label="Active call" data-testid="call-panel" data-phase={call.phase}
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
        <p id="incoming-what" className="text-sm text-sidebar-foreground/70">Incoming call</p>
        <Avatar name={call.peer.name} size="xl" className="mx-auto mt-5 border-sidebar-foreground/20 bg-sidebar-foreground/10 text-sidebar-foreground" />
        <p id="incoming-name" className="mt-4 font-display text-2xl font-semibold">{call.peer.name}</p>
        {call.peer.name !== call.peer.number && <p className="mt-1 text-sidebar-foreground/70">Ext {call.peer.number}</p>}
        <div className="mt-8 flex justify-center gap-10">
          <div className="flex flex-col items-center gap-2">
            <RoundButton label="Decline" variant="end" onClick={() => line.decline()}><PhoneOff /></RoundButton>
            <span className="text-sm" aria-hidden="true">Decline</span>
          </div>
          <div className="flex flex-col items-center gap-2">
            <RoundButton label="Answer" variant="call" onClick={() => line.answer()}><Phone /></RoundButton>
            <span className="text-sm" aria-hidden="true">Answer</span>
          </div>
        </div>
      </div>
    </div>
  );
}
