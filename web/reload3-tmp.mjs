import { webkit } from "playwright";
const state = "/private/tmp/claude-501/-Users-mohammed-Projects-linx/d73cf61b-a4b9-435e-b398-08eba9c87322/scratchpad/wk-state.json";
for (const variant of ["no web-phone", "no locks API", "no manifest link"]) {
  const b = await webkit.launch();
  const ctx = await b.newContext({ storageState: state });
  const p = await ctx.newPage();
  if (variant === "no web-phone") await p.route("**/api/v1/me/web-phone", (r) => r.fulfill({ status: 409, contentType: "application/problem+json", body: JSON.stringify({ code: "no_extension", detail: "x", status: 409, title: "x", type: "about:blank" }) }));
  if (variant === "no locks API") await p.addInitScript(() => { Object.defineProperty(navigator, "locks", { value: undefined }); });
  if (variant === "no manifest link") await p.route("**/manifest.webmanifest", (r) => r.fulfill({ status: 200, contentType: "application/manifest+json", body: "{}" }));
  await p.goto("https://home.mym.ae/", { waitUntil: "load" });
  await p.waitForTimeout(4000);
  const t = Date.now();
  let r;
  try { await p.reload({ waitUntil: "load", timeout: 15000 }); r = `ok ${Date.now() - t} ms`; } catch { r = "HANG"; }
  console.log(`${variant}: reload ${r}`);
  await b.close();
}
