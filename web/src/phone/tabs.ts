// One phone line per browser, not per tab (found in the install demo): all
// tabs share the sign-in, and each tab starting "the" line gave it a new
// password, knocking the other tab off, which then did the same back. The
// tab holding the Web Lock runs the line; the others show "open in another
// tab" and take over when it closes, or at once with "Use it here".
import type { PhoneLine } from "./line";

const LOCK = "linx-phone-line";

export interface LineHolder {
  /** Moves the line to this tab (the other tab steps back). */
  takeOver(): void;
  /** Signs out and gives the line up (the page closing, signing out). */
  release(): void;
}

export function holdLine(line: PhoneLine, locks: LockManager | null = navigator.locks ?? null): LineHolder {
  if (!locks) {
    // A browser without Web Locks: as before, one line per tab.
    void line.start();
    return { takeOver() {}, release: () => line.stop() };
  }
  let released = false;
  let gen = 0; // each request's own number: an older one that ends does nothing
  let pending: AbortController | null = null;
  let free: (() => void) | null = null;

  const run = async (mine: number) => {
    if (released || mine !== gen) return;
    await line.start();
    await new Promise<void>((resolve) => { free = resolve; });
    line.stop();
  };

  const request = (steal: boolean) => {
    const mine = ++gen;
    pending?.abort();
    pending = null;
    free?.();
    free = null;
    if (steal) {
      locks.request(LOCK, { steal: true }, () => run(mine)).catch(() => lost(mine));
      return;
    }
    // Free right now: take it without showing "elsewhere" first.
    void locks.request(LOCK, { ifAvailable: true }, async (lock) => {
      if (lock) return run(mine);
      if (released || mine !== gen) return;
      line.openElsewhere();
      const ac = new AbortController();
      pending = ac;
      locks.request(LOCK, { signal: ac.signal }, () => run(mine)).catch(() => lost(mine));
    }).catch(() => lost(mine));
  };

  // Another tab pressed "Use it here" (our lock was stolen), or our queued
  // request was replaced: stand back and wait for the line again.
  const lost = (mine: number) => {
    if (released || mine !== gen) return;
    free?.();
    free = null;
    request(false);
  };

  // Give the lock back as the page goes (reload, close, another page):
  // Safari doesn't finish a reload while the old page still holds a Web
  // Lock, so every reload hung on iPhone and iPad (owner, Phase 1E demo).
  // A page Safari keeps and shows again (back/forward cache) asks again.
  const onPageHide = () => {
    gen++; // any request still in flight stands down
    pending?.abort();
    pending = null;
    free?.();
    free = null;
  };
  const onPageShow = (e: PageTransitionEvent) => { if (e.persisted && !released) request(false); };
  window.addEventListener("pagehide", onPageHide);
  window.addEventListener("pageshow", onPageShow);

  request(false);
  line.takeOverHere = () => { if (!released) request(true); };
  return {
    takeOver: () => { if (!released) request(true); },
    release: () => {
      released = true;
      window.removeEventListener("pagehide", onPageHide);
      window.removeEventListener("pageshow", onPageShow);
      pending?.abort();
      free?.();
      line.stop();
    },
  };
}
