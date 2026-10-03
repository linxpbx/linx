// Setting up an iPhone or iPad (docs/PHASE2.md §4, ADR-073): the setup code
// with its QR picture, the emailed link, the 8 characters to type, the
// countdown, and the list of phones already set up.
//
// Nothing secret is stored: the code is shown once, works once, and expires
// in 10 minutes. No SIP password is ever in it.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";

type Device = components["schemas"]["Device"];
type NewEnrollment = components["schemas"]["NewEnrollment"];
type InviteEmail = components["schemas"]["InviteEmail"];

/** How often the dialog asks whether the phone has finished (10 minutes at most). */
const CHECK_EVERY_MS = 3000;

export type PhoneKind = "iPhone" | "iPad";

/** "in 9 minutes", "in 45 seconds", "Expired". */
export function countdownWords(until: string, now: number): string {
  const left = Math.round((new Date(until).getTime() - now) / 1000);
  if (left <= 0) return "Expired";
  if (left < 90) return `${left} second${left === 1 ? "" : "s"} left`;
  return `${Math.round(left / 60)} minutes left`;
}

/** "2 minutes ago", "yesterday", "3 March". */
export function lastSeenWords(at: string | undefined, now: number): string {
  if (!at) return "never";
  const secs = Math.round((now - new Date(at).getTime()) / 1000);
  if (secs < 90) return "just now";
  if (secs < 3600) return `${Math.round(secs / 60)} minutes ago`;
  if (secs < 36 * 3600) return `${Math.round(secs / 3600)} hours ago`;
  return new Date(at).toLocaleDateString(undefined, { day: "numeric", month: "long" });
}

function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

/** The code as a picture, drawn in the browser (the library loads with it). */
function QrPicture({ url }: { url: string }) {
  const [src, setSrc] = useState("");
  useEffect(() => {
    let live = true;
    void (async () => {
      const qr = await (await import("qrcode")).default.toDataURL(url, { margin: 1, width: 192 });
      if (live) setSrc(qr);
    })();
    return () => { live = false; };
  }, [url]);
  if (!src) return <div className="size-48 rounded-md border bg-card" aria-hidden="true" />;
  return <img src={src} alt="The setup code as a picture to scan" width={192} height={192} className="rounded-md border bg-white" />;
}

/**
 * AddPhoneDialog makes one setup code and waits for the phone.
 *
 * `onSetUp` runs when the phone finishes, so the list behind refreshes.
 * `emailOn` is null while Linx is still being asked whether email is set up.
 */
export function AddPhoneDialog({ open, onOpenChange, personName, userId, emailOn, isMe, onSetUp }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  personName: string;
  /** Whose phone it is; left out means my own. */
  userId?: string;
  emailOn: boolean | null;
  isMe: boolean;
  onSetUp: () => void;
}) {
  const firstName = personName.split(" ")[0] || personName;
  const [kind, setKind] = useState<PhoneKind>("iPhone");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [ticket, setTicket] = useState<NewEnrollment | null>(null);
  const [email, setEmail] = useState<InviteEmail | null>(null);
  const [done, setDone] = useState(false);
  const now = useNow(open && !!ticket && !done);
  const defaultName = isMe ? `My ${kind}` : `${firstName}'s ${kind}`;

  const reset = useCallback(() => {
    setKind("iPhone"); setName(""); setTicket(null); setEmail(null); setDone(false); setError(""); setBusy(false);
  }, []);

  const create = async (delivery: "qr" | "email" | "by_hand") => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/enrollments", {
      body: {
        name: (name.trim() || defaultName).slice(0, 100),
        ...(userId ? { user_id: userId } : {}),
        delivery,
        ...(delivery === "email" ? { send_email: true } : {}),
      },
    });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setTicket(data);
    setEmail(data.email ?? null);
  };

  // While the code is on screen, ask every few seconds whether the phone has
  // finished: the ticket disappears from the waiting list the moment it has.
  const ticketId = ticket?.enrollment.id;
  const expiresAt = ticket?.enrollment.expires_at;
  const expired = !!expiresAt && new Date(expiresAt).getTime() <= now;
  const onSetUpRef = useRef(onSetUp);
  onSetUpRef.current = onSetUp;
  useEffect(() => {
    if (!open || !ticketId || done || expired) return;
    let live = true;
    const check = async () => {
      const { data } = await api.GET("/api/v1/enrollments");
      if (!live || !data) return;
      if (!data.items.some((t) => t.id === ticketId)) {
        setDone(true);
        onSetUpRef.current();
      }
    };
    const timer = setInterval(() => void check(), CHECK_EVERY_MS);
    return () => { live = false; clearInterval(timer); };
  }, [open, ticketId, done, expired]);

  const cancel = async () => {
    if (ticketId && !done) await api.DELETE("/api/v1/enrollments/{id}", { params: { path: { id: ticketId } } });
    onOpenChange(false);
    reset();
  };

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) void cancel(); else onOpenChange(o); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{ticket ? (done ? "Phone set up" : `Set up ${ticket.enrollment.name}`) : "Add a phone"}</DialogTitle>
          <DialogDescription>
            {!ticket && (isMe
              ? "An iPhone or iPad of your own, so you can take calls on it."
              : `An iPhone or iPad for ${personName}, on extension ${""}`)}
            {ticket && !done && "Open Linx on the phone and scan this, or type the code. It works once."}
            {done && "It can make and take calls now."}
          </DialogDescription>
        </DialogHeader>

        {!ticket && (
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label>Which one</Label>
              <RadioGroup value={kind} onValueChange={(v) => setKind(v as PhoneKind)} className="flex gap-4">
                {(["iPhone", "iPad"] as const).map((k) => (
                  <Label key={k} htmlFor={`phone-kind-${k}`} className="flex items-center gap-2 font-normal">
                    <RadioGroupItem id={`phone-kind-${k}`} value={k} />
                    {k}
                  </Label>
                ))}
              </RadioGroup>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="phone-name">Name</Label>
              <Input id="phone-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={defaultName} autoFocus />
              <p className="text-sm text-muted-foreground">So you can tell it apart from their other phones.</p>
            </div>
            {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
            <div className="flex flex-col gap-2">
              <Button disabled={busy} onClick={() => void create("qr")}>Show a QR code</Button>
              <Button variant="outline" disabled={busy || emailOn === false} onClick={() => void create("email")}>
                {isMe ? "Email a link to me" : `Email a link to ${firstName}`}
              </Button>
              {emailOn === false && (
                <p className="text-sm text-muted-foreground">
                  Email isn't set up yet, so Linx can't send the link. An admin can turn it on in System → Settings.
                </p>
              )}
              <Button variant="outline" disabled={busy} onClick={() => void create("by_hand")}>Set it up by hand</Button>
            </div>
          </div>
        )}

        {ticket && !done && (
          <div className="flex flex-col items-center gap-4">
            {ticket.enrollment.delivery === "qr" && ticket.setup_url && <QrPicture url={ticket.setup_url} />}
            {email && <EmailResult email={email} />}
            <div className="flex flex-col items-center gap-1">
              <p className="text-sm text-muted-foreground">Or type this code into the app:</p>
              <p className="font-mono text-2xl tracking-[0.3em]" aria-label={`Setup code ${ticket.code.split("").join(" ")}`}>{ticket.code}</p>
            </div>
            <p className={`text-sm ${expired ? "text-destructive" : "text-muted-foreground"}`} aria-live="polite">
              {expired ? "This code has expired. Close this and make another one." : countdownWords(ticket.enrollment.expires_at, now)}
            </p>
            <p className="text-sm text-muted-foreground" aria-live="polite">
              {expired ? "" : "Waiting for the phone…"}
            </p>
            <p className="text-center text-sm text-muted-foreground">
              The code works once. The phone makes its own security key; no password is in the code.
            </p>
          </div>
        )}

        {done && (
          <p className="text-sm">
            <span className="font-medium">{ticket?.enrollment.name}</span> is set up for {personName}.
          </p>
        )}

        <DialogFooter>
          {!done && ticket && <Button variant="outline" onClick={() => void cancel()}>Cancel</Button>}
          {done && <Button onClick={() => { onOpenChange(false); reset(); }}>Done</Button>}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function EmailResult({ email }: { email: InviteEmail }) {
  if (email.error) return <p className="text-sm text-status-away">Not emailed: {email.error}</p>;
  if (email.queued) return <p className="text-sm text-muted-foreground">Emailed to {email.to}.</p>;
  return null;
}

/** One phone in a list: its name, when it was last in touch, and Expired. */
export function PhoneRow({ device, now, actions }: { device: Device; now: number; actions?: ReactNode }) {
  const expired = device.phone?.expired;
  return (
    <div className="flex flex-wrap items-center gap-2.5 rounded-md border p-2.5 text-sm">
      <span className="flex-1 min-w-40">
        {device.name}
        <span className="text-muted-foreground">
          {" · "}
          {expired ? "Expired" : device.online ? "Online" : `Last used ${lastSeenWords(device.phone?.last_seen_at, now)}`}
        </span>
      </span>
      {expired && (
        <span className="rounded-sm bg-status-away/15 px-1.5 py-0.5 text-xs text-status-away">Set it up again</span>
      )}
      {actions}
    </div>
  );
}
