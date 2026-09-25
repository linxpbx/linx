// The Linx web client (docs/WEB.md §6): sign in, then the Dialer, Team and
// Settings screens with the browser's own phone line.
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { api, type Me } from "@/api/client";
import { navigate, usePath } from "@/hooks/useRoute";
import { SignInScreen, type SignInStep } from "@/screens/SignIn";

// Everything after sign-in (the phone line, JsSIP, the screens) loads
// separately, so the sign-in page stays small (docs/WEB.md §6).
const SignedIn = lazy(() => import("@/screens/SignedIn"));

type Auth =
  | { state: "loading" }
  | { state: "signed-out"; step: SignInStep }
  | { state: "signed-in"; me: Me };

const SETUP = /^\/setup\/([^/]+)$/;

export function App() {
  const path = usePath();
  const setupToken = SETUP.exec(path)?.[1];
  const [auth, setAuth] = useState<Auth>({ state: "loading" });

  const loadMe = useCallback(async () => {
    const { data } = await api.GET("/api/v1/me");
    if (!data || data.type !== "user") {
      setAuth({ state: "signed-out", step: "password" });
    } else if (data.pending) {
      setAuth({ state: "signed-out", step: data.mfa_enabled ? "code" : "enroll" });
    } else {
      setAuth({ state: "signed-in", me: data });
    }
  }, []);

  useEffect(() => {
    if (setupToken) setAuth({ state: "signed-out", step: "choose-password" });
    else void loadMe();
  }, [setupToken, loadMe]);

  if (auth.state === "loading") return <div className="min-h-dvh" aria-busy="true" />;
  if (auth.state === "signed-out") {
    return (
      <SignInScreen key={setupToken ?? auth.step} initialStep={auth.step} setupToken={setupToken}
        onSignedIn={() => { navigate("/", true); void loadMe(); }} />
    );
  }
  return (
    <Suspense fallback={<div className="min-h-dvh bg-background" aria-busy="true" />}>
      <SignedIn me={auth.me} path={path} onSignedOut={() => setAuth({ state: "signed-out", step: "password" })} />
    </Suspense>
  );
}
