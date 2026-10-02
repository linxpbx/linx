// The app shell (WEB_SCREENS_PHASE1C.md §2): sidebar, top search bar, the
// screen, and the call panel on the right while a call is on.
import { lazy, Suspense, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Activity, BarChart3, CalendarClock, Check, CircleHelp, CircleUser, Clock, FlaskConical, Grid3x3, Hash, History, IdCard, Inbox, KeyRound, LogOut, Network,
  Phone, PhoneCall, PhoneIncoming, PhoneOutgoing, Search, Settings as SettingsIcon, Users, UsersRound, Video, Voicemail, Webhook,
  Home as HomeIcon,
} from "lucide-react";
import type { Me, Presence, TeamMember } from "@/api/client";
import { LogoMark, Wordmark } from "@/components/brand";
import { Avatar, presenceLabel, statusLabel } from "@/components/presence";
import { ThemeMenu } from "@/components/ThemeMenu";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { navigate } from "@/hooks/useRoute";
import { anyLineDown, type SystemStatus } from "@/hooks/useSystemStatus";
import { guideForScreen, HELP_PATH, loadGuides } from "@/lib/help";
import { hasScope, seesAdminArea } from "@/lib/roles";
import { usePhoneLine, usePhoneState } from "@/phone/context";
import { cn } from "@/lib/utils";
import { CallPanel, IncomingCall } from "./CallPanel";
import { DIALABLE, matchTeam } from "./Dialer";

// "Saved. Undo" for routing changes: only for those who can change routing,
// loaded on its own (it brings "confirm it's you" with it).
const UndoToast = lazy(() => import("@/components/UndoToast").then((m) => ({ default: m.UndoToast })));

export type Screen = "dialer" | "team" | "calls" | "voicemail" | "settings" | "account" | "help" | "admin-home" | "admin-people" | "admin-extensions"
  | "admin-system-status" | "admin-system-backups" | "admin-system-server"
  | "admin-lines" | "admin-incoming" | "admin-ring-groups" | "admin-office-hours" | "admin-outgoing" | "admin-simulator" | "admin-connections"
  | "admin-system-alerts" | "admin-system-activity" | "admin-system-routing-changes" | "admin-system-settings" | "admin-webhooks" | "admin-api-keys" | "admin-calls";

const NAV: { id: Screen; label: string; path: string; icon: typeof Users }[] = [
  { id: "dialer", label: "Dialer", path: "/", icon: Grid3x3 },
  { id: "team", label: "Team", path: "/team", icon: Users },
];
// Everyday tabs still to come are greyed; Voicemail (Phase 1F step 14) and
// Call history (step 15) are live.
const LATER: { label: string; icon: typeof Users; id?: Screen; path?: string }[] = [
  { label: "Call history", icon: Clock, id: "calls", path: "/calls" },
  { label: "Voicemail", icon: Voicemail, id: "voicemail", path: "/voicemail" },
  { label: "Meetings", icon: Video },
  { label: "Inbox", icon: Inbox },
  { label: "Reports", icon: BarChart3 },
];

// The admin group (docs/ui/ADMIN_SCREENS_PHASE1E.md §1), in checklist order.
// Every item has its screen (steps 5-7); System's tabs arrive in step 8.
const ADMIN_NAV: { label: string; icon: typeof Users; path?: string; scope?: string }[] = [
  { label: "Home", icon: HomeIcon, path: "/admin" },
  { label: "People", icon: IdCard, path: "/admin/people" },
  { label: "Extensions", icon: Hash, path: "/admin/extensions" },
  { label: "Phone lines", icon: PhoneCall, path: "/admin/lines" },
  { label: "Incoming", icon: PhoneIncoming, path: "/admin/incoming" },
  { label: "Ring groups", icon: UsersRound, path: "/admin/ring-groups" },
  { label: "Office hours", icon: CalendarClock, path: "/admin/office-hours" },
  { label: "Outgoing", icon: PhoneOutgoing, path: "/admin/outgoing" },
  { label: "Simulator", icon: FlaskConical, path: "/admin/simulator" },
  { label: "Calls", icon: History, path: "/admin/calls", scope: "calls:read" },
  { label: "System", icon: Activity, path: "/admin/system/status" },
];
const ADMIN_SCREEN_FOR_PATH: Record<string, Screen> = {
  "/admin": "admin-home", "/admin/people": "admin-people", "/admin/extensions": "admin-extensions",
  "/admin/system/status": "admin-system-status", "/admin/system/backups": "admin-system-backups",
  "/admin/system/server": "admin-system-server",
  "/admin/lines": "admin-lines", "/admin/incoming": "admin-incoming",
  "/admin/ring-groups": "admin-ring-groups", "/admin/office-hours": "admin-office-hours", "/admin/outgoing": "admin-outgoing",
  "/admin/simulator": "admin-simulator", "/admin/connections": "admin-connections",
  "/admin/webhooks": "admin-webhooks", "/admin/api-keys": "admin-api-keys", "/admin/calls": "admin-calls",
};

const ADMIN_EXPERT_NAV: { label: string; icon: typeof Users; path?: string }[] = [
  { label: "Connections", icon: Network, path: "/admin/connections" },
  { label: "Webhooks", icon: Webhook, path: "/admin/webhooks" },
  { label: "API keys", icon: KeyRound, path: "/admin/api-keys" },
];

function NavItem({ active, label, icon: Icon, onClick, disabled, badge, badgeWord = "open", dot }:
  { active?: boolean; label: string; icon: typeof Users; onClick?: () => void; disabled?: boolean; badge?: number; badgeWord?: string; dot?: boolean }) {
  // The admin list is longer than a short window: keep the page's own row in view.
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (active) ref.current?.scrollIntoView({ block: "nearest" }); }, [active]);
  const item = (
    <button ref={ref} type="button" onClick={onClick} aria-current={active ? "page" : undefined}
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
        {(dot || !!badge) && (
          <span aria-hidden="true" className={cn("absolute -right-0.5 -top-0.5 size-2 rounded-full bg-status-busy ring-2 ring-sidebar", !dot && "md:hidden")} />
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
  const detail = badge ? `${label} · ${badge} ${badgeWord}` : dot ? `${label} · needs attention` : disabled ? `${label} · Coming soon` : label;
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
              {me.extension ? `Ext ${me.extension} · ` : ""}{status === "ready" ? presenceLabel[presence] : status === "unavailable" ? "No phone line" : status === "elsewhere" ? "Phone open in another tab" : "Connecting…"}
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

/**
 * The ? button (docs/HELP.md §3): the guide for the screen you're on, or
 * Help's front page when no guide is about it.
 */
function ScreenHelp() {
  const open = async () => {
    const guide = guideForScreen(await loadGuides(), window.location.pathname);
    navigate(guide ? `${HELP_PATH}/${guide.name}` : HELP_PATH);
  };
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" onClick={() => void open()} aria-label="Help for this page"
          className="flex size-10 shrink-0 items-center justify-center rounded-full text-muted-foreground outline-none hover:bg-card hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50">
          <CircleHelp aria-hidden="true" className="size-5" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="bottom">Help for this page</TooltipContent>
    </Tooltip>
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
      {ADMIN_NAV.filter((n) => !n.scope || hasScope(me, n.scope)).map((n) => (
        <NavItem key={n.label} label={n.label} icon={n.icon} disabled={!n.path}
          active={!!n.path && (screen === ADMIN_SCREEN_FOR_PATH[n.path]
            || (n.label === "System" && screen.startsWith("admin-system-")))}
          onClick={n.path ? () => navigate(n.path!) : undefined}
          badge={n.label === "Home" ? openAlerts : undefined}
          dot={n.label === "Phone lines" ? linesDown : undefined} />
      ))}
      {!simpleMode && (
        <>
          <p className="px-3 py-1 text-xs font-medium tracking-wide text-sidebar-foreground/50 max-md:sr-only">EXPERT</p>
          {ADMIN_EXPERT_NAV.map((n) => (
            <NavItem key={n.label} label={n.label} icon={n.icon} disabled={!n.path}
              active={!!n.path && screen === ADMIN_SCREEN_FOR_PATH[n.path]}
              onClick={n.path ? () => navigate(n.path!) : undefined} />
          ))}
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

export function Shell({ me, screen, members, presence, voicemailNew = 0, callsMissed = 0, systemStatus, simpleMode, onSimpleModeChange, onPresence, onSignOut, children }: {
  me: Me; screen: Screen; members: TeamMember[] | null; presence: Presence; voicemailNew?: number; callsMissed?: number;
  systemStatus: SystemStatus | null; simpleMode: boolean; onSimpleModeChange: (v: boolean) => void;
  onPresence: (p: Presence) => void; onSignOut: () => void; children: (query: string) => ReactNode;
}) {
  const { call, status, problem } = usePhoneState();
  const line = usePhoneLine();
  const [query, setQuery] = useState("");
  const ringing = call?.direction === "incoming" && call.phase === "ringing" ? call : null;

  // Ringing in a background tab: say so in the tab's title and icon.
  useEffect(() => {
    const icon = document.querySelector<HTMLLinkElement>("link[rel=icon]");
    if (ringing) {
      document.title = `Ringing: ${ringing.peer.name} · Linx`;
      if (icon) icon.href = "/favicon-ringing.svg";
    } else {
      document.title = voicemailNew > 0 ? `(${voicemailNew}) Linx` : "Linx";
      if (icon) icon.href = "/favicon.svg";
    }
  }, [ringing, voicemailNew]);

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
          {LATER.map((n) => (
            <NavItem key={n.label} label={n.label} icon={n.icon} disabled={!n.path} active={!!n.id && screen === n.id}
              onClick={n.path ? () => navigate(n.path!) : undefined}
              badge={n.id === "voicemail" ? voicemailNew : n.id === "calls" ? callsMissed : undefined}
              badgeWord={n.id === "calls" ? "missed" : "new"} />
          ))}
          <AdminNav me={me} systemStatus={systemStatus} simpleMode={simpleMode} onSimpleModeChange={onSimpleModeChange} screen={screen} />
        </div>
        <div className="mt-auto flex flex-col gap-2">
          <NavItem label="Help" icon={CircleHelp} active={screen === "help"} onClick={() => navigate(HELP_PATH)} />
          <NavItem label="Settings" icon={SettingsIcon} active={screen === "settings"} onClick={() => navigate("/settings")} />
          <AccountMenu me={me} presence={presence} onPresence={onPresence} onSignOut={onSignOut} />
        </div>
      </nav>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-16 shrink-0 items-center gap-3 border-b px-4 md:px-6">
          <SearchBar members={members} query={query} setQuery={setQuery} onTeam={() => { if (screen !== "team") navigate("/team"); }} />
          <div className="ms-auto flex shrink-0 items-center gap-1">
            {screen !== "help" && <ScreenHelp />}
            <ThemeMenu />
          </div>
        </header>
        {status === "elsewhere" && (
          <p role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b bg-card px-4 py-2.5 text-sm md:px-6">
            <span>Your phone line is open in another tab of this browser.</span>
            <button type="button" className="font-medium text-link underline-offset-4 hover:underline" onClick={() => line.takeOverHere()}>Use it here</button>
          </p>
        )}
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
      {hasScope(me, "routing:write") && <Suspense fallback={null}><UndoToast me={me} /></Suspense>}
    </div>
  );
}
