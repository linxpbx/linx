// The app shell (WEB_SCREENS_PHASE1C.md §2): sidebar, top search bar, the
// screen, and the call panel on the right while a call is on.
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Activity, BarChart3, Check, CircleUser, Clock, FlaskConical, Grid3x3, Hash, IdCard, Inbox, KeyRound, LogOut, Network,
  Phone, PhoneCall, PhoneIncoming, PhoneOutgoing, Search, Settings as SettingsIcon, Users, Video, Voicemail, Webhook,
  Home as HomeIcon,
} from "lucide-react";
import type { Me, Presence, TeamMember } from "@/api/client";
import { LogoMark, Wordmark } from "@/components/brand";
import { Avatar, presenceLabel, statusLabel } from "@/components/presence";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { navigate } from "@/hooks/useRoute";
import { anyLineDown, type SystemStatus } from "@/hooks/useSystemStatus";
import { hasScope, seesAdminArea } from "@/lib/roles";
import { usePhoneLine, usePhoneState } from "@/phone/context";
import { cn } from "@/lib/utils";
import { CallPanel, IncomingCall } from "./CallPanel";
import { DIALABLE, matchTeam } from "./Dialer";

export type Screen = "dialer" | "team" | "settings" | "account" | "admin-home" | "admin-people" | "admin-extensions";

const NAV: { id: Screen; label: string; path: string; icon: typeof Users }[] = [
  { id: "dialer", label: "Dialer", path: "/", icon: Grid3x3 },
  { id: "team", label: "Team", path: "/team", icon: Users },
];
const LATER: { label: string; icon: typeof Users }[] = [
  { label: "Call history", icon: Clock },
  { label: "Voicemail", icon: Voicemail },
  { label: "Meetings", icon: Video },
  { label: "Inbox", icon: Inbox },
  { label: "Reports", icon: BarChart3 },
];

// The admin group (docs/ui/ADMIN_SCREENS_PHASE1E.md §1), in checklist order.
// Home, People and Extensions have screens (steps 5-6); the rest arrive in
// steps 7-8 and appear greyed "Coming soon" until then, like 1C's LATER.
const ADMIN_NAV: { label: string; icon: typeof Users; path?: string }[] = [
  { label: "Home", icon: HomeIcon, path: "/admin" },
  { label: "People", icon: IdCard, path: "/admin/people" },
  { label: "Extensions", icon: Hash, path: "/admin/extensions" },
  { label: "Phone lines", icon: PhoneCall },
  { label: "Incoming", icon: PhoneIncoming },
  { label: "Outgoing", icon: PhoneOutgoing },
  { label: "Simulator", icon: FlaskConical },
  { label: "System", icon: Activity },
];
const ADMIN_SCREEN_FOR_PATH: Record<string, Screen> = {
  "/admin": "admin-home", "/admin/people": "admin-people", "/admin/extensions": "admin-extensions",
};

const ADMIN_EXPERT_NAV: { label: string; icon: typeof Users }[] = [
  { label: "Connections", icon: Network },
  { label: "Webhooks", icon: Webhook },
  { label: "API keys", icon: KeyRound },
];

function NavItem({ active, label, icon: Icon, onClick, disabled, badge, dot }:
  { active?: boolean; label: string; icon: typeof Users; onClick?: () => void; disabled?: boolean; badge?: number; dot?: boolean }) {
  const item = (
    <button type="button" onClick={onClick} aria-current={active ? "page" : undefined}
      aria-disabled={disabled || undefined} tabIndex={disabled ? -1 : undefined}
      className={cn(
        "flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
        active && "bg-primary text-primary-foreground",
        !active && !disabled && "text-sidebar-foreground/85 hover:bg-sidebar-foreground/10 hover:text-sidebar-foreground",
        disabled && "cursor-default text-sidebar-foreground/40",
        "max-md:justify-center max-md:px-0",
      )}>
      <span className="relative shrink-0">
        <Icon aria-hidden="true" className="size-5" />
        {dot && (
          <span aria-hidden="true" className="absolute -right-0.5 -top-0.5 size-2 rounded-full bg-status-busy ring-2 ring-sidebar" />
        )}
      </span>
      <span className="max-md:sr-only">{label}</span>
      {!!badge && (
        <span aria-hidden="true" className="ms-auto max-md:sr-only rounded-full bg-status-busy px-1.5 py-0.5 text-xs font-medium text-white">
          {badge}
        </span>
      )}
    </button>
  );
  const detail = badge ? `${label} · ${badge} open` : dot ? `${label} · needs attention` : disabled ? `${label} · Coming soon` : label;
  if (!disabled && !badge && !dot) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>{item}</TooltipTrigger>
        <TooltipContent side="right" className="md:hidden">{label}</TooltipContent>
      </Tooltip>
    );
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>{item}</TooltipTrigger>
      <TooltipContent side="right">{detail}</TooltipContent>
    </Tooltip>
  );
}

function AccountMenu({ me, presence, onPresence, onSignOut }:
  { me: Me; presence: Presence; onPresence: (p: Presence) => void; onSignOut: () => void }) {
  const { status } = usePhoneState();
  const shown = presence === "available" && status !== "ready" ? "offline" : presence;
  const name = me.name ?? me.email ?? "Me";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" data-testid="account-menu"
          className="flex w-full items-center gap-3 rounded-md bg-sidebar-foreground/5 p-2.5 text-start hover:bg-sidebar-foreground/10 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 max-md:justify-center">
          <Avatar name={name} status={shown} className="border-sidebar-foreground/20 bg-sidebar-foreground/10 text-sidebar-foreground" />
          <span className="min-w-0 max-md:sr-only">
            <span className="block truncate text-sm font-medium text-sidebar-foreground">{name}</span>
            <span className="block truncate text-xs text-sidebar-foreground/70">
              {me.extension ? `Ext ${me.extension} · ` : ""}{status === "ready" ? presenceLabel[presence] : status === "unavailable" ? "No phone line" : "Connecting…"}
            </span>
          </span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="start" className="w-56">
        <DropdownMenuLabel>Set status</DropdownMenuLabel>
        {(["available", "away", "dnd"] as const).map((p) => (
          <DropdownMenuItem key={p} onSelect={() => onPresence(p)}>
            <Check aria-hidden="true" className={cn("size-4", presence !== p && "invisible")} />
            {presenceLabel[p]}
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => navigate("/account")}>
          <CircleUser aria-hidden="true" className="size-4" />
          My account
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={onSignOut}>
          <LogOut aria-hidden="true" className="size-4" />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function SearchBar({ members, query, setQuery, onTeam }: {
  members: TeamMember[] | null; query: string; setQuery: (q: string) => void; onTeam: () => void;
}) {
  const line = usePhoneLine();
  const { status, call, extension } = usePhoneState();
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const q = query.trim();
  const digits = DIALABLE.test(q);
  const matches = matchTeam(members, q, extension);
  const canCall = status === "ready" && !call;
  const callNow = (number: string, name?: string) => {
    line.call(number, name);
    setQuery("");
    setOpen(false);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canCall) return;
    if (digits) callNow(q, members?.find((m) => m.extension === q)?.name);
    else if (matches[0]) callNow(matches[0].extension, matches[0].name);
  };
  useEffect(() => {
    const close = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, []);
  return (
    <div ref={box} className="relative w-full max-w-xl">
      <form onSubmit={submit} role="search">
        <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
        <input value={query} onChange={(e) => { setQuery(e.target.value); setOpen(true); if (!DIALABLE.test(e.target.value.trim())) onTeam(); }}
          onFocus={() => setOpen(true)} placeholder="Search people or dial a number" aria-label="Search people or dial a number"
          className="h-10 w-full rounded-md border bg-card ps-9 pe-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50" />
      </form>
      {open && q && (digits || matches.length > 0) && (
        <ul className="absolute inset-x-0 top-full z-40 mt-1 overflow-hidden rounded-md border bg-popover shadow-md">
          {digits && (
            <li>
              <button type="button" disabled={!canCall} onClick={() => callNow(q, members?.find((m) => m.extension === q)?.name)}
                className="flex w-full items-center gap-3 px-3 py-2.5 text-start text-sm hover:bg-background disabled:opacity-50">
                <Phone aria-hidden="true" className="size-4 text-call" />
                Call <span className="font-mono">{q}</span>
              </button>
            </li>
          )}
          {matches.map((m) => (
            <li key={m.extension}>
              <button type="button" disabled={!canCall} onClick={() => callNow(m.extension, m.name)}
                className="flex w-full items-center gap-3 px-3 py-2 text-start text-sm hover:bg-background disabled:opacity-50">
                <Avatar name={m.name} status={m.status} />
                <span className="min-w-0 flex-1 truncate">{m.name} <span className="text-muted-foreground">· Ext <span className="font-mono">{m.extension}</span> · {statusLabel[m.status]}</span></span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function AdminNav({ me, systemStatus, simpleMode, onSimpleModeChange, screen }: {
  me: Me; systemStatus: SystemStatus | null; simpleMode: boolean; onSimpleModeChange: (v: boolean) => void; screen: Screen;
}) {
  if (!seesAdminArea(me)) return null;
  const openAlerts = systemStatus?.open_alerts.length ?? 0;
  const linesDown = anyLineDown(systemStatus);
  const canToggleSimpleMode = hasScope(me, "settings:write");

  if (me.admin_network_restricted) {
    return (
      <>
        <div className="my-2 border-t border-sidebar-foreground/10" />
        <p className="px-3 py-1 text-xs font-medium tracking-wide text-sidebar-foreground/50 max-md:sr-only">ADMIN</p>
        <NavItem label="Admin" icon={HomeIcon} disabled />
      </>
    );
  }

  return (
    <>
      <div className="my-2 border-t border-sidebar-foreground/10" />
      <p className="px-3 py-1 text-xs font-medium tracking-wide text-sidebar-foreground/50 max-md:sr-only">ADMIN</p>
      {ADMIN_NAV.map((n) => (
        <NavItem key={n.label} label={n.label} icon={n.icon} disabled={!n.path}
          active={!!n.path && screen === ADMIN_SCREEN_FOR_PATH[n.path]}
          onClick={n.path ? () => navigate(n.path!) : undefined}
          badge={n.label === "Home" ? openAlerts : undefined}
          dot={n.label === "Phone lines" ? linesDown : undefined} />
      ))}
      {!simpleMode && (
        <>
          <p className="px-3 py-1 text-xs font-medium tracking-wide text-sidebar-foreground/50 max-md:sr-only">EXPERT</p>
          {ADMIN_EXPERT_NAV.map((n) => <NavItem key={n.label} label={n.label} icon={n.icon} disabled />)}
        </>
      )}
      <button type="button" onClick={() => onSimpleModeChange(!simpleMode)} disabled={!canToggleSimpleMode}
        title={!canToggleSimpleMode ? "Only a system admin or admin can change this" : undefined}
        className="mt-1 px-3 py-1 text-start text-xs text-sidebar-foreground/60 hover:text-sidebar-foreground/90 disabled:cursor-not-allowed disabled:opacity-50 max-md:sr-only">
        {simpleMode ? "Show expert pages" : "Hide expert pages"}
      </button>
    </>
  );
}

export function Shell({ me, screen, members, presence, systemStatus, simpleMode, onSimpleModeChange, onPresence, onSignOut, children }: {
  me: Me; screen: Screen; members: TeamMember[] | null; presence: Presence;
  systemStatus: SystemStatus | null; simpleMode: boolean; onSimpleModeChange: (v: boolean) => void;
  onPresence: (p: Presence) => void; onSignOut: () => void; children: (query: string) => ReactNode;
}) {
  const { call, status, problem } = usePhoneState();
  const [query, setQuery] = useState("");
  const ringing = call?.direction === "incoming" && call.phase === "ringing" ? call : null;

  // Ringing in a background tab: say so in the tab's title and icon.
  useEffect(() => {
    const icon = document.querySelector<HTMLLinkElement>("link[rel=icon]");
    if (ringing) {
      document.title = `Ringing: ${ringing.peer.name} · Linx`;
      if (icon) icon.href = "/favicon-ringing.svg";
    } else {
      document.title = "Linx";
      if (icon) icon.href = "/favicon.svg";
    }
  }, [ringing]);

  return (
    <div className="flex h-dvh overflow-hidden">
      <nav aria-label="Main" className="flex w-16 shrink-0 flex-col bg-sidebar p-2 text-sidebar-foreground md:w-60 md:p-3.5">
        <div className="flex h-12 items-center px-1 max-md:justify-center md:px-2">
          <Wordmark onDark className="text-3xl text-sidebar-foreground max-md:hidden" />
          <LogoMark className="size-7 text-link-on-dark md:hidden" />
        </div>
        <div className="mt-4 flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto">
          {NAV.map((n) => (
            <NavItem key={n.id} label={n.label} icon={n.icon} active={screen === n.id} onClick={() => navigate(n.path)} />
          ))}
          <div className="my-2 border-t border-sidebar-foreground/10" />
          {LATER.map((n) => <NavItem key={n.label} label={n.label} icon={n.icon} disabled />)}
          <AdminNav me={me} systemStatus={systemStatus} simpleMode={simpleMode} onSimpleModeChange={onSimpleModeChange} screen={screen} />
        </div>
        <div className="mt-auto flex flex-col gap-2">
          <NavItem label="Settings" icon={SettingsIcon} active={screen === "settings"} onClick={() => navigate("/settings")} />
          <AccountMenu me={me} presence={presence} onPresence={onPresence} onSignOut={onSignOut} />
        </div>
      </nav>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-16 shrink-0 items-center gap-3 border-b px-4 md:px-6">
          <SearchBar members={members} query={query} setQuery={setQuery} onTeam={() => { if (screen !== "team") navigate("/team"); }} />
        </header>
        {problem && (
          <p role="status" className="border-b bg-card px-4 py-2.5 text-sm md:px-6">{problem}</p>
        )}
        {status === "reconnecting" && (
          <p role="status" className="border-b bg-card px-4 py-2.5 text-sm md:px-6">Reconnecting your phone line…</p>
        )}
        <div className="flex min-h-0 flex-1">
          <main className="min-w-0 flex-1 overflow-y-auto">{children(query)}</main>
          {call && !ringing && (
            <aside aria-label="Call" className="border-s bg-card p-4 max-lg:fixed max-lg:inset-0 max-lg:z-40 max-lg:flex max-lg:items-center max-lg:justify-center max-lg:bg-sidebar/60 lg:w-96 lg:shrink-0">
              <div className="w-full max-w-sm">
                <CallPanel call={call} />
              </div>
            </aside>
          )}
        </div>
      </div>
      {ringing && <IncomingCall call={ringing} />}
    </div>
  );
}
