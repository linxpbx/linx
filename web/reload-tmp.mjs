import { webkit } from "playwright";
const b = await webkit.launch();
const ctx = await b.newContext({ storageState: "/private/tmp/claude-501/-Users-mohammed-Projects-linx/d73cf61b-a4b9-435e-b398-08eba9c87322/scratchpad/wk-state.json" });
const p = await ctx.newPage();
const pending = new Map();
p.on("request", (r) => { pending.set(r, Date.now()); });
p.on("requestfinished", (r) => pending.delete(r));
p.on("requestfailed", (r) => pending.delete(r));
p.on("requestfailed", (r) => console.log("  FAILED", new URL(r.url()).pathname, r.failure()?.errorText));
p.on("websocket", (ws) => { console.log("  ws open", new URL(ws.url()).pathname); ws.on("close", () => console.log("  ws closed", new URL(ws.url()).pathname)); });
let t = Date.now();
await p.goto("https://home.mym.ae/", { waitUntil: "load" });
await p.waitForTimeout(4000);
console.log("protocols:", JSON.stringify(await p.evaluate(() => [...new Set(performance.getEntries().map((e) => e.nextHopProtocol).filter(Boolean))])));
console.log("loaded, status:", await p.getByTestId("account-menu").innerText().catch(() => "?"), Date.now() - t, "ms");
for (let i = 0; i < 1; i++) {
  t = Date.now();
  console.log("RELOAD", i + 1);
  try { await p.reload({ waitUntil: "load", timeout: 25000 }); console.log("reload done", Date.now() - t, "ms"); }
  catch (e) { console.log("reload FAILED after", Date.now() - t, "ms:", e.message.split("\n")[0]);
    for (const [r, at] of pending) console.log("   still waiting:", r.method(), new URL(r.url()).pathname, Date.now() - at, "ms"); }
  await p.waitForTimeout(4000);
}
await b.close();
