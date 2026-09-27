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

// Every PATCH in the API is a JSON Merge Patch (RFC 7396) and the server
// accepts it only as application/merge-patch+json (api/openapi.yaml);
// openapi-fetch sends every body as application/json, so label it here once
// rather than at each call. Renaming a passkey is the one PATCH that takes
// plain JSON.
const PLAIN_JSON_PATCH = /\/api\/v1\/me\/passkeys\//;

export const mergePatch: Middleware = {
  onRequest({ request }) {
    if (request.method === "PATCH" && !PLAIN_JSON_PATCH.test(new URL(request.url).pathname)
      && request.headers.get("Content-Type")?.startsWith("application/json")) {
      request.headers.set("Content-Type", "application/merge-patch+json");
    }
    return request;
  },
};

export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "same-origin",
  fetch: (req) => globalThis.fetch(req),
});
api.use(csrf, mergePatch);

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
