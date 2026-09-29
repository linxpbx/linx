import { describe, expect, it } from "vitest";
import { holdLine } from "./tabs";
import type { PhoneLine } from "./line";

// A stand-in for the browser's LockManager with the parts holdLine uses:
// one lock name, a queue, ifAvailable, steal (the holder's request rejects)
// and abort signals for queued requests.
function fakeLocks() {
  type Req = { cb: (l: Lock | null) => Promise<unknown>; resolve: (v: unknown) => void; reject: (e: unknown) => void };
  let holder: Req | null = null;
  const queue: Req[] = [];
  const grant = (r: Req) => {
    holder = r;
    void r.cb({ name: "x", mode: "exclusive" } as Lock).then(
      (v) => { if (holder === r) { holder = null; r.resolve(v); next(); } },
      (e) => { if (holder === r) { holder = null; r.reject(e); next(); } },
    );
  };
  const next = () => { const r = queue.shift(); if (r) grant(r); };
  const request = (_name: string, opts: LockOptions, cb: (l: Lock | null) => Promise<unknown>) =>
    new Promise((resolve, reject) => {
      const r: Req = { cb, resolve, reject };
      if (opts.steal) {
        const old = holder;
        holder = null;
        old?.reject(new DOMException("stolen", "AbortError"));
        grant(r);
      } else if (!holder) {
        grant(r);
      } else if (opts.ifAvailable) {
        void cb(null).then(resolve, reject);
      } else {
        queue.push(r);
        opts.signal?.addEventListener("abort", () => {
          const i = queue.indexOf(r);
          if (i >= 0) { queue.splice(i, 1); reject(new DOMException("aborted", "AbortError")); }
        });
      }
    });
  return { request } as unknown as LockManager;
}

function fakeLine() {
  const log: string[] = [];
  const line = {
    status: "starting",
    takeOverHere: () => {},
    start: async () => { log.push("start"); line.status = "ready"; },
    stop: () => { log.push("stop"); },
    openElsewhere: () => { log.push("elsewhere"); line.status = "elsewhere"; },
  };
  return { line, log };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe("holdLine", () => {
  it("one tab holds the line; another waits, takes over, and gets it back when the first closes", async () => {
    const locks = fakeLocks();
    const a = fakeLine();
    const b = fakeLine();
    const ha = holdLine(a.line as unknown as PhoneLine, locks);
    await tick();
    expect(a.line.status).toBe("ready");

    const hb = holdLine(b.line as unknown as PhoneLine, locks);
    await tick();
    expect(b.line.status).toBe("elsewhere");
    expect(b.log).not.toContain("start");

    // "Use it here" in the second tab: the first steps back.
    b.line.takeOverHere();
    await tick(); await tick();
    expect(b.line.status).toBe("ready");
    expect(a.line.status).toBe("elsewhere");

    // The second tab closes: the first gets the line back by itself.
    hb.release();
    await tick(); await tick();
    expect(a.line.status).toBe("ready");
    ha.release();
  });

  it("without Web Locks, each tab starts its own line as before", async () => {
    const a = fakeLine();
    holdLine(a.line as unknown as PhoneLine, null);
    await tick();
    expect(a.log).toEqual(["start"]);
  });
});
