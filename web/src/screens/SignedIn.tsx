// The signed-in app: the phone line and the Dialer, Team and Settings
// screens (loaded after sign-in; see App.tsx).
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import { api, type Me, type Presence } from "@/api/client";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useTeam } from "@/hooks/useTeam";
import { navigate } from "@/hooks/useRoute";
import { useSystemStatus } from "@/hooks/useSystemStatus";
import { hasScope, seesAdminArea } from "@/lib/roles";
import { PhoneContext } from "@/phone/context";
import { PhoneLine } from "@/phone/line";
import { holdLine, type LineHolder } from "@/phone/tabs";
import { AccountScreen } from "./Account";
import { DialerScreen } from "./Dialer";
import { SettingsScreen } from "./Settings";
import { Shell, type Screen } from "./Shell";
import { TeamScreen } from "./Team";

// The admin area and the setup wizard load only when opened: most people
// never see them, and every page load counts on a slow link (the
// low-bandwidth rule, CLAUDE.md).
const AdminHomeScreen = lazy(() => import("./AdminHome").then((m) => ({ default: m.AdminHomeScreen })));
const PeopleScreen = lazy(() => import("./People").then((m) => ({ default: m.PeopleScreen })));
const ExtensionsScreen = lazy(() => import("./Extensions").then((m) => ({ default: m.ExtensionsScreen })));
const PhoneLinesScreen = lazy(() => import("./PhoneLines").then((m) => ({ default: m.PhoneLinesScreen })));
const IncomingCallsScreen = lazy(() => import("./IncomingCalls").then((m) => ({ default: m.IncomingCallsScreen })));
const OutgoingCallsScreen = lazy(() => import("./OutgoingCalls").then((m) => ({ default: m.OutgoingCallsScreen })));
const CallSimulatorScreen = lazy(() => import("./CallSimulator").then((m) => ({ default: m.CallSimulatorScreen })));
const ConnectionsScreen = lazy(() => import("./Connections").then((m) => ({ default: m.ConnectionsScreen })));
const SystemStatusScreen = lazy(() => import("./SystemStatus").then((m) => ({ default: m.SystemStatusScreen })));
const SystemBackupsScreen = lazy(() => import("./SystemBackups").then((m) => ({ default: m.SystemBackupsScreen })));
const SystemServerScreen = lazy(() => import("./SystemServer").then((m) => ({ default: m.SystemServerScreen })));
const SetupWizardScreen = lazy(() => import("./SetupWizard").then((m) => ({ default: m.SetupWizardScreen })));
const loading = <div className="p-6" aria-busy="true" />;

export default function SignedIn({ me, path, onSignedOut }: { me: Me; path: string; onSignedOut: () => void }) {
  const line = useMemo(() => new PhoneLine(), []);
  const [presence, setPresence] = useState<Presence>(me.presence ?? "available");
  const team = useTeam(true);
  const admin = seesAdminArea(me) && !me.admin_network_restricted;
  const systemStatus = useSystemStatus(admin);
  // Simple mode (docs/ui/ADMIN_SCREENS_PHASE1E.md §1): on by default, so the
  // sidebar doesn't flash expert items in while settings load.
  const [simpleMode, setSimpleMode] = useState(true);

  // One tab holds the phone line (phone/tabs.ts).
  const holder = useRef<LineHolder | null>(null);
  useEffect(() => {
    const h = holdLine(line);
    holder.current = h;
    return () => h.release();
  }, [line]);

  useEffect(() => {
    const members = team.members;
    line.setDirectory((n) => members?.find((m) => m.extension === n)?.name);
  }, [line, team.members]);

  useEffect(() => {
    if (!admin || !hasScope(me, "settings:read")) return;
    void api.GET("/api/v1/settings").then(({ data }) => { if (data) setSimpleMode(data.simple_mode); });
  }, [admin, me]);

  const changeSimpleMode = async (v: boolean) => {
    setSimpleMode(v);
    const { error } = await api.PATCH("/api/v1/settings", { body: { simple_mode: v } });
    if (error) setSimpleMode(!v);
  };

  const changePresence = async (p: Presence) => {
    const before = presence;
    setPresence(p);
    const { response } = await api.PUT("/api/v1/me/presence", { body: { presence: p } });
    if (!response.ok) setPresence(before);
  };

  const signOut = async () => {
    line.stop();
    await api.DELETE("/api/v1/session");
    navigate("/", true);
    onSignedOut();
  };

  // The setup wizard is a full-page flow with no sidebar
  // (docs/ui/ADMIN_SCREENS_PHASE1E.md §3.2).
  if (path === "/setup") {
    return (
      <PhoneContext.Provider value={line}>
        <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}>
          <SetupWizardScreen me={me} onExit={() => navigate("/admin", true)} />
        </Suspense>
      </PhoneContext.Provider>
    );
  }

  const screen: Screen = path === "/team" ? "team" : path === "/settings" ? "settings" : path === "/account" ? "account"
    : path === "/admin" ? "admin-home" : path === "/admin/people" ? "admin-people" : path === "/admin/extensions" ? "admin-extensions"
      : path === "/admin/system" || path === "/admin/system/status" ? "admin-system-status"
      : path === "/admin/system/backups" ? "admin-system-backups"
      : path === "/admin/system/server" && me.role === "system_admin" ? "admin-system-server"
      : path === "/admin/lines" ? "admin-lines" : path === "/admin/incoming" ? "admin-incoming"
      : path === "/admin/outgoing" ? "admin-outgoing" : path === "/admin/simulator" ? "admin-simulator"
      : path === "/admin/connections" ? "admin-connections"
      : "dialer";

  return (
    <TooltipProvider delayDuration={300}>
    <PhoneContext.Provider value={line}>
      <Shell me={me} screen={screen} members={team.members} presence={presence}
        systemStatus={systemStatus} simpleMode={simpleMode} onSimpleModeChange={(v) => void changeSimpleMode(v)}
        onPresence={(p) => void changePresence(p)} onSignOut={() => void signOut()}>
        {(query) => <Suspense fallback={loading}>{
          screen === "team" ? <TeamScreen members={team.members} query={query} />
            : screen === "settings" ? <SettingsScreen />
              : screen === "account" ? <AccountScreen />
              : screen === "admin-home" ? <AdminHomeScreen me={me} systemStatus={systemStatus} members={team.members} />
              : screen === "admin-people" ? <PeopleScreen me={me} />
              : screen === "admin-extensions" ? <ExtensionsScreen me={me} />
              : screen === "admin-system-status" ? <SystemStatusScreen me={me} />
              : screen === "admin-system-backups" ? <SystemBackupsScreen me={me} />
              : screen === "admin-system-server" ? <SystemServerScreen me={me} />
              : screen === "admin-lines" ? <PhoneLinesScreen me={me} />
              : screen === "admin-incoming" ? <IncomingCallsScreen me={me} />
              : screen === "admin-outgoing" ? <OutgoingCallsScreen me={me} simpleMode={simpleMode} />
              : screen === "admin-simulator" ? <CallSimulatorScreen me={me} />
              : screen === "admin-connections" ? <ConnectionsScreen me={me} />
              : <DialerScreen members={team.members} />}</Suspense>}
      </Shell>
    </PhoneContext.Provider>
    </TooltipProvider>
  );
}
