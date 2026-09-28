// The web install's page frame (docs/ui/INSTALL_SCREENS.md §0): one card
// in the middle, the whole install's progress line above it, the plain
// page's strip, and the pieces every install step uses.
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Check, ChevronDown, CircleAlert, CircleX, Clock, Copy, LoaderCircle, ShieldAlert, TriangleAlert } from "lucide-react";
import { Wordmark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { RadioGroupItem } from "@/components/ui/radio-group";
import { cn } from "@/lib/utils";
import { mmss } from "@/lib/install";

/**
 * The whole install's steps: the plain page covers the first four. Sign-in
 * comes after Install (docs/INSTALL.md §14 item 2): the first admin needs
 * the full Linx, which Install starts.
 */
export const PROGRESS = ["Server", "Domain", "You", "Certificate", "DNS", "Install", "Sign-in"] as const;

export function LinkUnusable() {
  return (
    <Frame>
      <h1 className="font-display text-xl font-semibold">This link can't be used</h1>
      <p className="mt-2 text-sm text-muted-foreground">Setup links work once, for four hours. For a new one, run this on the server:</p>
      <code className="mt-4 block rounded-md bg-muted px-3 py-2 font-mono text-sm">sudo linx setup</code>
      <p className="mt-4 text-sm text-muted-foreground">Answers you already gave are kept for the new link.</p>
    </Frame>
  );
}

/** Seconds left, counted down from what the server said when the page loaded. */
export function useSecondsLeft(initial: number): number {
  const [left, setLeft] = useState(initial);
  useEffect(() => {
    const end = performance.now() + initial * 1000;
    const t = setInterval(() => setLeft(Math.max(0, Math.round((end - performance.now()) / 1000))), 1000);
    return () => clearInterval(t);
  }, [initial]);
  return left;
}

/**
 * How long this link has left, and how to get a new one (under the card).
 * The last five minutes are a warning. On the secure page there's no link
 * to renew: it says what happens when the time is up instead.
 */
export function Countdown({ left, secure = false }: { left: number; secure?: boolean }) {
  const soon = left <= 300;
  return (
    <div role="timer" aria-live={soon ? "polite" : "off"}
      className={cn("mt-4 flex items-start gap-2 rounded-md px-4 py-3 text-sm", soon ? "border border-status-away bg-card" : "text-muted-foreground")}>
      {soon
        ? <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
        : <Clock aria-hidden="true" className="mt-0.5 size-4 shrink-0" />}
      <span>
        {soon ? <strong className="font-medium text-foreground">This {secure ? "setup page" : "link"} closes in {mmss(left)}.</strong>
          : <>This {secure ? "setup page" : "link"} closes in {mmss(left)}.</>}{" "}
        {secure
          ? <>If it does, run <code className="font-mono text-foreground">sudo linx setup</code> on the server for a new link. Your answers so far are kept.</>
          : <>Need more time? Run <code className="font-mono text-foreground">sudo linx setup --new-link</code> on the server for a new link. Your answers so far are kept.</>}
      </span>
    </div>
  );
}

/**
 * The card. at: where the progress line is (none on the welcome and
 * "can't be used" pages); back: the finished steps that can still be
 * clicked. strip: the first page's "temporary certificate" line (port
 * 6464, docs/INSTALL.md §14 item 1).
 */
export function Frame({ at, back, strip = true, footer, children }: {
  at?: number; back?: (i: number) => void; strip?: boolean; footer?: ReactNode; children: ReactNode;
}) {
  return (
    <main className="flex min-h-dvh items-start justify-center bg-background px-4 py-10 sm:items-center">
      <div className="w-full max-w-xl min-w-0">
        <div className="mb-8 flex justify-center">
          <Wordmark className="text-5xl" />
        </div>
        {at !== undefined && <ProgressLine at={at} back={back} />}
        <section className="rounded-lg border bg-card shadow-xs">
          {strip && (
            <p className="flex items-start gap-2 border-b px-6 py-3 text-sm text-muted-foreground">
              <ShieldAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              <span>This page uses a temporary certificate, so your browser can't tell it's really your server. The fingerprint setup printed on the server can.</span>
            </p>
          )}
          <div className="p-6">{children}</div>
        </section>
        {footer}
      </div>
    </main>
  );
}

/** "● Server ─ ● Domain ─ ◉ You ─ ○ …"; "Step 3 of 7 · You" on a phone. */
function ProgressLine({ at, back }: { at: number; back?: (i: number) => void }) {
  return (
    <nav aria-label="Install progress" className="mb-4">
      <p className="text-center text-sm text-muted-foreground sm:hidden">Step {at + 1} of {PROGRESS.length} · {PROGRESS[at]}</p>
      <ol className="hidden flex-wrap items-center justify-center gap-x-2 gap-y-1 text-sm sm:flex">
        {PROGRESS.map((name, i) => {
          const done = i < at;
          const dot = (
            <span className={cn("flex items-center gap-1.5", i === at ? "font-medium text-foreground" : done ? "text-foreground" : "text-muted-foreground")}>
              <span aria-hidden="true" className={cn("inline-block size-2.5 rounded-full border",
                done ? "border-primary bg-primary" : i === at ? "border-primary ring-2 ring-primary/30" : "border-border")} />
              {name}
              {i === at && <span className="sr-only"> (this step)</span>}
            </span>
          );
          return (
            <li key={name} className="flex items-center gap-2">
              {i > 0 && <span aria-hidden="true" className="h-px w-3 bg-border" />}
              {done && back && i < 3 ? <button type="button" className="hover:underline" onClick={() => back(i)}>{dot}</button> : dot}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

export function Title({ children, lead }: { children: ReactNode; lead?: ReactNode }) {
  return (
    <div className="mb-6">
      <h1 className="font-display text-xl font-semibold">{children}</h1>
      {lead && <p className="mt-1 text-sm text-muted-foreground">{lead}</p>}
    </div>
  );
}

export function FieldMessage({ message }: { message: string }) {
  if (!message) return null;
  return (
    <p role="alert" className="flex items-start gap-2 text-sm font-medium">
      <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      {message}
    </p>
  );
}

export function Nav({ onBack, next = "Next", busy = false, disabled = false }: { onBack?: () => void; next?: string; busy?: boolean; disabled?: boolean }) {
  return (
    <div className="mt-8 flex flex-wrap items-center justify-end gap-3">
      {onBack && <Button type="button" variant="outline" onClick={onBack} disabled={busy}>Back</Button>}
      <Button type="submit" disabled={busy || disabled}>
        {busy && <LoaderCircle aria-hidden="true" className="animate-spin" />}
        {next}
      </Button>
    </div>
  );
}

export function submit(fn: () => void) {
  return (e: FormEvent) => { e.preventDefault(); fn(); };
}

export type Mark = "ok" | "waiting" | "todo" | "running" | "failed" | "later";

export function Row({ n, state, title, children }: { n: number; state: Mark; title: string; children?: ReactNode }) {
  const icon = {
    ok: <Check aria-hidden="true" className="size-4 text-status-available" />,
    running: <LoaderCircle aria-hidden="true" className="size-4 animate-spin text-link" />,
    waiting: <LoaderCircle aria-hidden="true" className="size-4 animate-spin text-muted-foreground" />,
    failed: <CircleX aria-hidden="true" className="size-4 text-destructive" />,
    todo: <span aria-hidden="true" className="size-2.5 rounded-full border-2 border-primary" />,
    later: <span aria-hidden="true" className="size-2.5 rounded-full border" />,
  }[state];
  const words = { ok: "done", running: "working on it", waiting: "waiting", failed: "didn't work", todo: "your turn", later: "not yet" }[state];
  return (
    <li className="flex min-w-0 gap-3">
      <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center">{icon}</span>
      <div className="min-w-0 flex-1">
        <p className={cn("text-sm font-medium", state === "later" && "text-muted-foreground")}>
          <span className="me-1 text-muted-foreground">{n}</span> {title}
          <span className="sr-only"> ({words})</span>
        </p>
        {children && <div className="mt-2">{children}</div>}
      </div>
    </li>
  );
}

export function Disclosure({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="min-w-0">
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}
        className="flex items-center gap-1 text-start text-sm text-link hover:underline">
        <ChevronDown aria-hidden="true" className={cn("size-4 shrink-0 transition-transform", !open && "-rotate-90")} />
        {label}
      </button>
      {open && <div className="mt-2 ps-5">{children}</div>}
    </div>
  );
}

export function CopyButton({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button type="button" variant="ghost" size="icon" aria-label={done ? `${label} copied` : `Copy ${label}`}
      onClick={() => { void navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1500); }); }}>
      {done ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
    </Button>
  );
}

export function Detail({ text }: { text?: string }) {
  if (!text) return null;
  return <span className="break-words">It got: <span className="font-mono text-xs">{text}</span></span>;
}

export function Choice({ id, value, title, hint, badge, disabled, children }: {
  id: string; value: string; title: ReactNode; hint?: ReactNode; badge?: string; disabled?: boolean; children?: ReactNode;
}) {
  return (
    <div className={cn("rounded-md border p-4 has-[[data-state=checked]]:border-primary", disabled && "opacity-60")}>
      <label htmlFor={id} className={cn("flex items-start gap-3", disabled ? "cursor-not-allowed" : "cursor-pointer")}>
        <RadioGroupItem id={id} value={value} className="mt-1" disabled={disabled} />
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="text-sm font-medium">
            {title}
            {badge && <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">{badge}</span>}
          </span>
          {hint && <span className="text-sm text-muted-foreground">{hint}</span>}
        </span>
      </label>
      {children && <div className="mt-3 ps-7">{children}</div>}
    </div>
  );
}
