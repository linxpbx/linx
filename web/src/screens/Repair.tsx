// The repair page (docs/INSTALL.md §7, docs/ui/INSTALL_SCREENS.md §5.3):
// when https://<domain> is broken, `sudo linx setup` opens the Server
// settings page on port 6464 too, behind a one-time link. A system admin
// signs in with a password and authenticator app or recovery code
// (passkeys belong to the domain); a link made with --no-sign-in skips the
// sign-in. Then the Server settings panel, to fix the domain, front door or
// DNS token.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { CircleAlert } from "lucide-react";
import { api, type Me } from "@/api/client";
import { Wordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Countdown, Frame, LinkUnusable, Title, useSecondsLeft } from "@/components/InstallFrame";
import { repairClient, sessionClient } from "@/lib/serverSettings";
import { SignInScreen } from "@/screens/SignIn";
import { ServerSettingsPanel } from "@/screens/SystemServer";

type RepairState = { no_sign_in: boolean; problem?: string; domain: string; expires_in: number };

export default function RepairScreen() {
  const [state, setState] = useState<RepairState | "loading" | "closed">("loading");
  useEffect(() => {
    void fetch("/repair/api/state", { credentials: "same-origin" })
      .then(async (r) => setState(r.ok ? ((await r.json()) as RepairState) : "closed"))
      .catch(() => setState("closed"));
  }, []);
  if (state === "loading") return <div className="min-h-dvh bg-background" aria-busy="true" />;
  if (state === "closed") return <LinkUnusable />;
  return <Repair state={state} />;
}

function Repair({ state }: { state: RepairState }) {
  const left = useSecondsLeft(state.expires_in);
  const [me, setMe] = useState<Me | null | "loading">(state.no_sign_in ? null : "loading");
  const [setupNeeded, setSetupNeeded] = useState(false);
  const loadMe = useCallback(async () => {
    const { data } = await api.GET("/api/v1/me");
    setMe(data && data.type === "user" && !data.pending ? data : null);
  }, []);
  useEffect(() => { if (!state.no_sign_in) void loadMe(); }, [state.no_sign_in, loadMe]);
  if (left <= 0) return <LinkUnusable />;

  const header = (
    <>
      <Title lead={<>Change the domain, what's in front of this server or its DNS token, so <span className="break-all">https://{state.domain}</span> works again.</>}>
        Fix this server's address
      </Title>
      {state.problem && <Problem domain={state.domain} problem={state.problem} />}
    </>
  );
  const footer = <Countdown left={left} repair />;

  if (state.no_sign_in) {
    return (
      <Wide footer={footer}>
        {header}
        <ServerSettingsPanel me={null} client={repairClient} />
      </Wide>
    );
  }
  if (me === "loading") return <div className="min-h-dvh bg-background" aria-busy="true" />;
  if (!me) {
    if (setupNeeded) {
      return (
        <Frame footer={footer}>
          {header}
          <p className="text-sm">
            Your account isn't finished yet: it needs a second sign-in step first. Finish it at https://{state.domain} if that opens, or
            run <code className="font-mono">sudo linx setup --new-link --no-sign-in</code> on the server.
          </p>
        </Frame>
      );
    }
    return (
      <SignInScreen onSignedIn={() => void loadMe()} onSetupNeeded={() => setSetupNeeded(true)}
        aside={<>{state.problem && <Problem domain={state.domain} problem={state.problem} />}{footer}<PasskeyOnly /></>} />
    );
  }
  if (me.role !== "system_admin") {
    return (
      <Frame footer={footer}>
        {header}
        <p className="text-sm">Only a system admin can change this server's settings. You're signed in as {me.email}.</p>
        <Button variant="outline" className="mt-4" onClick={() => void api.DELETE("/api/v1/session").then(() => setMe(null))}>Sign out</Button>
      </Frame>
    );
  }
  return (
    <Wide footer={footer}>
      {header}
      <ServerSettingsPanel me={me} client={sessionClient} />
    </Wide>
  );
}

/** The frame, wide enough for the settings. */
function Wide({ footer, children }: { footer: ReactNode; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh justify-center bg-background px-4 py-10">
      <div className="w-full max-w-3xl min-w-0">
        <div className="mb-8 flex justify-center"><Wordmark className="text-5xl" /></div>
        {children}
        {footer}
      </div>
    </main>
  );
}

function Problem({ domain, problem }: { domain: string; problem: string }) {
  return (
    <p className="my-4 flex items-start gap-2 rounded-md border bg-card px-4 py-3 text-sm">
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      <span className="min-w-0 break-words">
        https://{domain} didn't answer from the server: <span className="font-mono text-xs">{problem}</span>
      </span>
    </p>
  );
}

function PasskeyOnly() {
  return (
    <p className="mt-4 px-4 text-sm text-muted-foreground">
      Passkeys only work at your Linx's own address. Sign in with a passkey only? Run{" "}
      <code className="font-mono text-foreground">sudo linx setup --new-link --no-sign-in</code> on the server instead.
    </p>
  );
}
