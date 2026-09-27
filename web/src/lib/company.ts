// Company sign-in (ADR-052, docs/ADMIN.md §6): the browser goes to the
// provider (Google, Microsoft, ...) and comes back to Linx, which answers
// with a redirect carrying only a short result or error code. Signing in and
// linking use the whole window; "confirm it's you" uses a small window so the
// dialog (and the action waiting on it) stays where it is.
import { api, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";

export type CompanyButton = components["schemas"]["CompanyButton"];

type StartPath = "/api/v1/session/company" | "/api/v1/me/sso-links" | "/api/v1/session/confirm/company";

/** Starts a flow and returns the provider's page, or throws the reason. */
async function start(path: StartPath, providerId: string): Promise<string> {
  const { data, error } = await api.POST(path, { body: { provider_id: providerId } });
  if (!data) throw new Error(problemMessage(error, "Company sign-in isn't working right now. Try again."));
  return data.url;
}

/** Sends this window to the provider (sign in, or link from My account). */
export async function goToCompany(path: "/api/v1/session/company" | "/api/v1/me/sso-links", providerId: string) {
  window.location.assign(await start(path, providerId));
}

/** The plain sentence for a company_error code (ADMIN_SCREENS_PHASE1E.md §12.1). */
export function companyErrorMessage(code: string): string {
  switch (code) {
    case "no_account":
      return "No Linx account uses this company account. Ask your admin to add you.";
    case "email_unverified":
      return "Your company account hasn't confirmed this email address.";
    case "account_disabled":
      return "Your Linx account is disabled. Ask your admin.";
    case "not_linked":
      return "Your Linx account is linked to a different company account.";
    case "linked_elsewhere":
      return "That company account is already linked to another person in Linx.";
    case "email_mismatch":
      return "That company account's email doesn't match your Linx email.";
    case "company_cancelled":
      return "Company sign-in was cancelled.";
    case "company_expired":
      return "That took too long. Try again.";
    default:
      return "Your company account's sign-in didn't work. Try again, or ask your admin.";
  }
}

/** Reads and removes ?company_error= (and ?company=) from the address. */
export function takeCompanyResult(): { error: string; result: string } {
  const q = new URLSearchParams(window.location.search);
  const error = q.get("company_error") ?? "";
  const result = q.get("company") ?? "";
  if (error || result) window.history.replaceState(null, "", window.location.pathname);
  return { error, result };
}

const CHANNEL = "linx-company";

export type CompanyDone = { result: "confirmed" | "code_required" } | { error: string };

/** The small window's page (/company-done): tells the dialog, then closes. */
export function reportCompanyDone() {
  const q = new URLSearchParams(window.location.search);
  const error = q.get("company_error");
  const msg: CompanyDone = error ? { error } : { result: q.get("result") === "code_required" ? "code_required" : "confirmed" };
  try {
    const ch = new BroadcastChannel(CHANNEL);
    ch.postMessage(msg);
    ch.close();
  } catch {
    // Very old browsers: the dialog can't hear back; the person confirms another way.
  }
  window.close();
}

/**
 * "Confirm it's you" with a company account: opens the provider in a small
 * window and waits for its answer (or signal, when the person gives up).
 * The window must open straight from the click (browsers block pop-ups
 * otherwise), so it's opened before asking the server where to send it.
 * Whether the window was closed can't be watched: providers' pages cut the
 * link to the page that opened them (Cross-Origin-Opener-Policy), so a
 * browser reports it closed while it's still open.
 */
export async function confirmWithCompany(providerId: string, signal: AbortSignal): Promise<CompanyDone> {
  const popup = window.open("about:blank", "linx-company", "popup,width=520,height=680");
  if (!popup) throw new Error("Your browser blocked the sign-in window. Allow pop-ups for this page and try again.");
  let url: string;
  try {
    url = await start("/api/v1/session/confirm/company", providerId);
  } catch (err) {
    popup.close();
    throw err;
  }
  popup.location.href = url;
  return new Promise((resolve) => {
    const ch = new BroadcastChannel(CHANNEL);
    const done = (msg: CompanyDone) => {
      ch.close();
      resolve(msg);
    };
    ch.onmessage = (e: MessageEvent<CompanyDone>) => done(e.data);
    signal.addEventListener("abort", () => done({ error: "company_cancelled" }), { once: true });
  });
}
