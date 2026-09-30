import { webkit } from "playwright";
const state = "/private/tmp/claude-501/-Users-mohammed-Projects-linx/d73cf61b-a4b9-435e-b398-08eba9c87322/scratchpad/wk-state.json";
for (const fake of ["none", "/sip", "/api/v1/team/live", "both"]) {
  const b = await webkit.launch();
  const ctx = await b.newContext({ storageState: state });
  const p = await ctx.newPage();
  for (const path of fake === "both" ? ["/sip", "/api/v1/team/live"] : fake === "none" ? [] : [fake]) {
    await p.routeWebSocket((u) => u.pathname === path, () => {});
  }
  await p.goto("https://home.mym.ae/", { waitUntil: "load" });
  await p.waitForTimeout(4000);
  const t = Date.now();
  let r;
  try { await p.reload({ waitUntil: "load", timeout: 15000 }); r = `ok ${Date.now() - t} ms`; } catch { r = "HANG"; }
  console.log(`stand-in for ${fake}: reload ${r}`);
  await b.close();
}
