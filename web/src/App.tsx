// The Linx web client (docs/WEB.md §6): sign in, then the Dialer, Team and
// Settings screens with the browser's own phone line.
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { api, type Me } from "@/api/client";
import { navigate, usePath } from "@/hooks/useRoute";
import { SignInScreen, type SecondStepMethod, type SignInStep } from "@/screens/SignIn";
import { reportCompanyDone } from "@/lib/company";
import { REPAIR_PATH } from "@/lib/repair";
import { forgetGuides, guideInPath, HELP_PATH } from "@/lib/help";

// Everything after sign-in (the phone line, JsSIP, the screens) loads
// separately, so the sign-in page stays small (docs/WEB.md §6).
const SignedIn = lazy(() => import("@/screens/SignedIn"));
// The web install's pages (docs/INSTALL.md): the plain page on port 6464,
// then the secure one on https://<domain>, before the rest of Linx
// exists.
const Install = lazy(() => import("@/screens/Install"));
// The repair page on port 6464 (docs/INSTALL.md §7).
const Repair = lazy(() => import("@/screens/Repair"));
// Help without signing in: the sign-in guides (docs/HELP.md §8 item 3).
const Help = lazy(() => import("@/screens/Help"));
// The phone's side of Check it (docs/SIMPLER.md §2.3), no sign-in.
const Reach = lazy(() => import("@/screens/Reach"));
const REACH = /^\/reach\/([A-Za-z0-9]{1,32})$/;

type Auth =
  | { state: "loading" }
  | { state: "signed-out"; step: SignInStep; methods?: SecondStepMethod[] }
  | { state: "signed-in"; me: Me };

const SETUP = /^\/setup\/([^/]+)$/;
// An emailed "Forgot your password?" link (ADR-067).
const RESET = /^\/reset\/([^/]+)$/;

/**
 * Where a completed sign-in lands: the first system_admin from a setup link
 * (docs/ADMIN.md §4) goes to the setup wizard if it isn't finished yet;
 * everyone else (an ordinary invited person, or any later sign-in) goes to
 * the app (docs/ui/ADMIN_SCREENS_PHASE1E.md §3.1).
 */
async function finishSignIn(setupToken: string | undefined, loadMe: () => Promise<void>) {
  if (setupToken) {
    const { data: me } = await api.GET("/api/v1/me");
    if (me?.type === "user" && !me.pending && (me.role === "system_admin" || me.role === "admin")) {
      const { data: setup } = await api.GET("/api/v1/setup");
      if (setup && !setup.completed) {
        forgetGuides();
        navigate("/setup", true);
        void loadMe();
        return;
      }
    }
  }
  forgetGuides();
  navigate("/", true);
  void loadMe();
}

export function App() {
  const path = usePath();
  if (path === "/company-done") return <CompanyDonePage />;
  if (path === "/install" || path === "/install/continue") {
    return <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}><Install /></Suspense>;
  }
  const reach = REACH.exec(path)?.[1];
  if (reach) {
    return <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}><Reach code={reach} /></Suspense>;
  }
  if (path === REPAIR_PATH) {
    return <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}><Repair /></Suspense>;
  }
  return <Main path={path} />;
}

/**
 * Where a "confirm it's you" company sign-in lands, in the small window the
 * dialog opened: it tells the dialog and closes itself.
 */
function CompanyDonePage() {
  useEffect(() => { reportCompanyDone(); }, []);
  return (
    <main className="flex min-h-dvh items-center justify-center px-4 text-center text-sm text-muted-foreground">
      Done. You can close this window.
    </main>
  );
}

function Main({ path }: { path: string }) {
  const setupToken = SETUP.exec(path)?.[1];
  const resetToken = RESET.exec(path)?.[1];
  const [auth, setAuth] = useState<Auth>({ state: "loading" });

  const loadMe = useCallback(async () => {
    const { data } = await api.GET("/api/v1/me");
    if (!data || data.type !== "user") {
      setAuth({ state: "signed-out", step: "password" });
    } else if (data.pending) {
      // Waiting on the second step, or (an admin with none) on setting one up.
      const methods: SecondStepMethod[] = [];
      if (data.mfa_enabled) methods.push("authenticator");
      if (data.passkeys) methods.push("passkey");
      if (data.recovery_codes_left) methods.push("recovery_code");
      setAuth({ state: "signed-out", step: methods.length ? "code" : "enroll", methods });
    } else {
      setAuth({ state: "signed-in", me: data });
    }
  }, []);

  useEffect(() => {
    if (setupToken) setAuth({ state: "signed-out", step: "choose-password" });
    else if (resetToken) setAuth({ state: "signed-out", step: "reset" });
    else void loadMe();
  }, [setupToken, resetToken, loadMe]);

  if (auth.state === "loading") return <div className="min-h-dvh" aria-busy="true" />;
  if (auth.state === "signed-out") {
    if (path === HELP_PATH || guideInPath(path)) {
      return <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}><Help path={path} signedIn={false} /></Suspense>;
    }
    return (
      <SignInScreen key={setupToken ?? resetToken ?? auth.step} initialStep={auth.step} initialMethods={auth.methods} setupToken={setupToken}
        resetToken={resetToken} onSignedIn={() => void finishSignIn(setupToken, loadMe)} />
    );
  }
  return (
    <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}>
      <SignedIn me={auth.me} path={path} onSignedOut={() => { forgetGuides(); setAuth({ state: "signed-out", step: "password" }); }} />
    </Suspense>
  );
}
