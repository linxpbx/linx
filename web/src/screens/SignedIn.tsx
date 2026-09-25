// The signed-in app: the phone line and the Dialer, Team and Settings
// screens (loaded after sign-in; see App.tsx).
import { useEffect, useMemo, useState } from "react";
import { api, type Me, type Presence } from "@/api/client";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useTeam } from "@/hooks/useTeam";
import { navigate } from "@/hooks/useRoute";
import { PhoneContext } from "@/phone/context";
import { PhoneLine } from "@/phone/line";
import { DialerScreen } from "./Dialer";
import { SettingsScreen } from "./Settings";
import { Shell, type Screen } from "./Shell";
import { TeamScreen } from "./Team";

export default function SignedIn({ me, path, onSignedOut }: { me: Me; path: string; onSignedOut: () => void }) {
  const line = useMemo(() => new PhoneLine(), []);
  const [presence, setPresence] = useState<Presence>(me.presence ?? "available");
  const team = useTeam(true);

  useEffect(() => {
    void line.start();
    return () => line.stop();
  }, [line]);

  useEffect(() => {
    const members = team.members;
    line.setDirectory((n) => members?.find((m) => m.extension === n)?.name);
  }, [line, team.members]);

  const screen: Screen = path === "/team" ? "team" : path === "/settings" ? "settings" : "dialer";

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

  return (
    <TooltipProvider delayDuration={300}>
    <PhoneContext.Provider value={line}>
      <Shell me={me} screen={screen} members={team.members} presence={presence}
        onPresence={(p) => void changePresence(p)} onSignOut={() => void signOut()}>
        {(query) =>
          screen === "team" ? <TeamScreen members={team.members} query={query} />
            : screen === "settings" ? <SettingsScreen />
              : <DialerScreen members={team.members} />}
      </Shell>
    </PhoneContext.Provider>
    </TooltipProvider>
  );
}
