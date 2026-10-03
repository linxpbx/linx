// The page an emailed setup link opens on the phone (docs/PHASE2.md §4).
// The code is in the link's #fragment, which browsers never send to a
// server, so it stays between the email and this page.
//
// Until the app is on the phone, the page shows the code to scan or type.
import { useEffect, useMemo, useState } from "react";
import { Wordmark } from "@/components/brand";
import { ThemeMenu } from "@/components/ThemeMenu";
import { countdownWords } from "@/components/AddPhone";

/** When the token runs out, read from the token itself (no checking: the
 *  server does that when the phone uses it). */
function expiryOf(token: string): string {
  try {
    const payload = token.split(".")[1] ?? "";
    const body = JSON.parse(atob(payload.replace(/-/g, "+").replace(/_/g, "/"))) as { exp?: number };
    if (body.exp) return new Date(body.exp * 1000).toISOString();
  } catch {
    // A link that isn't a Linx setup link: the page says so below.
  }
  return "";
}

export default function SetUpPhoneScreen() {
  const token = useMemo(() => decodeURIComponent(window.location.hash.replace(/^#/, "")), []);
  const expires = useMemo(() => expiryOf(token), [token]);
  const [qr, setQr] = useState("");
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    if (!token) return;
    let live = true;
    void (async () => {
      const url = window.location.href;
      const picture = await (await import("qrcode")).default.toDataURL(url, { margin: 1, width: 208 });
      if (live) setQr(picture);
    })();
    return () => { live = false; };
  }, [token]);

  const expired = !!expires && new Date(expires).getTime() <= now;
  const bad = !token || !expires;

  return (
    <main className="flex min-h-dvh flex-col items-center bg-background px-4 py-10">
      <div className="flex w-full max-w-md items-center justify-between">
        <Wordmark className="h-7" />
        <ThemeMenu />
      </div>
      <div className="mt-8 w-full max-w-md rounded-lg border bg-card p-6 text-center">
        <h1 className="font-display text-2xl font-semibold tracking-tight">Set up this phone</h1>
        {bad && (
          <p className="mt-3 text-sm text-muted-foreground">
            This link isn't a Linx setup link, or it was cut short on the way. Ask for a new one.
          </p>
        )}
        {!bad && expired && (
          <p className="mt-3 text-sm text-destructive">
            This code has expired. Ask your admin for a new one — they're good for 10 minutes.
          </p>
        )}
        {!bad && !expired && (
          <>
            <p className="mt-2 text-sm text-muted-foreground">
              Open Linx on this iPhone or iPad and scan this code, or tap it in the app.
            </p>
            <div className="mt-5 flex justify-center">
              {qr
                ? <img src={qr} alt="The setup code as a picture to scan" width={208} height={208} className="rounded-md border bg-white" />
                : <div className="size-52 rounded-md border" aria-hidden="true" />}
            </div>
            <p className="mt-4 text-sm text-muted-foreground" aria-live="polite">{countdownWords(expires, now)}</p>
            <p className="mt-4 text-sm text-muted-foreground">
              It works once. The phone makes its own security key as it finishes, and no password is in this code.
            </p>
          </>
        )}
      </div>
    </main>
  );
}
