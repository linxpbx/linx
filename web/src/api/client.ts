// The Linx API on this page's own origin (ADR-037), typed from
// api/openapi.yaml (`make api` regenerates schema.d.ts).
import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";

export type Me = components["schemas"]["Principal"];
export type WebPhone = components["schemas"]["WebPhone"];
export type TurnCredentials = components["schemas"]["TurnCredentials"];
export type TeamMember = components["schemas"]["TeamMember"];
export type TeamList = components["schemas"]["TeamList"];
export type Presence = components["schemas"]["Presence"];
export type Problem = components["schemas"]["Problem"];

export const CSRF_COOKIE = "__Host-linx_csrf";
export const CSRF_HEADER = "X-CSRF-Token";

export function csrfToken(): string {
  for (const part of document.cookie.split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === CSRF_COOKIE) return decodeURIComponent(v.join("="));
  }
  return "";
}

// Every change needs the session's CSRF token in a header (docs/WEB.md §4).
const csrf: Middleware = {
  onRequest({ request }) {
    if (!["GET", "HEAD", "OPTIONS"].includes(request.method)) {
      const token = csrfToken();
      if (token) request.headers.set(CSRF_HEADER, token);
    }
    return request;
  },
};

export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "same-origin",
  fetch: (req) => globalThis.fetch(req),
});
api.use(csrf);

/** The plain-language message from a problem+json error, or a fallback. */
export function problemMessage(error: unknown, fallback = "Something went wrong. Please try again."): string {
  if (error && typeof error === "object" && "detail" in error && typeof error.detail === "string" && error.detail) {
    return error.detail;
  }
  return fallback;
}

export function problemCode(error: unknown): string {
  if (error && typeof error === "object" && "code" in error && typeof error.code === "string") return error.code;
  return "";
}
