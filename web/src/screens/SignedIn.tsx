// The signed-in app: the phone line and the Dialer, Team and Settings
// screens (loaded after sign-in; see App.tsx).
import { useEffect, useMemo, useState } from "react";
import { api, type Me, type Presence } from "@/api/client";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useTeam } from "@/hooks/useTeam";
import { navigate } from "@/hooks/useRoute";
import { useSystemStatus } from "@/hooks/useSystemStatus";
import { hasScope, seesAdminArea } from "@/lib/roles";
import { PhoneContext } from "@/phone/context";
import { PhoneLine } from "@/phone/line";
import { AccountScreen } from "./Account";
import { AdminHomeScreen } from "./AdminHome";
import { DialerScreen } from "./Dialer";
import { ExtensionsScreen } from "./Extensions";
import { PeopleScreen } from "./People";
import { SystemBackupsScreen } from "./SystemBackups";
import { SettingsScreen } from "./Settings";
import { SetupWizardScreen } from "./SetupWizard";
import { Shell, type Screen } from "./Shell";
import { TeamScreen } from "./Team";

export default function SignedIn({ me, path, onSignedOut }: { me: Me; path: string; onSignedOut: () => void }) {
  const line = useMemo(() => new PhoneLine(), []);
  const [presence, setPresence] = useState<Presence>(me.presence ?? "available");
  const team = useTeam(true);
  const admin = seesAdminArea(me) && !me.admin_network_restricted;
  const systemStatus = useSystemStatus(admin);
  // Simple mode (docs/ui/ADMIN_SCREENS_PHASE1E.md §1): on by default, so the
  // sidebar doesn't flash expert items in while settings load.
  const [simpleMode, setSimpleMode] = useState(true);

  useEffect(() => {
    void line.start();
    return () => line.stop();
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
        <SetupWizardScreen me={me} onExit={() => navigate("/admin", true)} />
      </PhoneContext.Provider>
    );
  }

  const screen: Screen = path === "/team" ? "team" : path === "/settings" ? "settings" : path === "/account" ? "account"
    : path === "/admin" ? "admin-home" : path === "/admin/people" ? "admin-people" : path === "/admin/extensions" ? "admin-extensions"
      : path === "/admin/system" || path === "/admin/system/backups" ? "admin-system-backups"
      : "dialer";

  return (
    <TooltipProvider delayDuration={300}>
    <PhoneContext.Provider value={line}>
      <Shell me={me} screen={screen} members={team.members} presence={presence}
        systemStatus={systemStatus} simpleMode={simpleMode} onSimpleModeChange={(v) => void changeSimpleMode(v)}
        onPresence={(p) => void changePresence(p)} onSignOut={() => void signOut()}>
        {(query) =>
          screen === "team" ? <TeamScreen members={team.members} query={query} />
            : screen === "settings" ? <SettingsScreen />
              : screen === "account" ? <AccountScreen />
              : screen === "admin-home" ? <AdminHomeScreen me={me} systemStatus={systemStatus} members={team.members} />
              : screen === "admin-people" ? <PeopleScreen me={me} />
              : screen === "admin-extensions" ? <ExtensionsScreen me={me} />
              : screen === "admin-system-backups" ? <SystemBackupsScreen me={me} />
              : <DialerScreen members={team.members} />}
      </Shell>
    </PhoneContext.Provider>
    </TooltipProvider>
  );
}
