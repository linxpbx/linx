// Passkeys with the browser's own API (ADR-051: no client library). Every
// ceremony is two requests: POST <path>/options gives the options (and a
// short-lived challenge cookie), the device answers, and POST <path> sends
// the answer back as PublicKeyCredential.toJSON() gives it.
import { CSRF_HEADER, csrfToken } from "@/api/client";
import { onRepairPage } from "@/lib/repair";

type Json = Record<string, unknown>;

interface PublicKeyCredentialJSONStatics {
  parseCreationOptionsFromJSON(options: Json): PublicKeyCredentialCreationOptions;
  parseRequestOptionsFromJSON(options: Json): PublicKeyCredentialRequestOptions;
  isConditionalMediationAvailable?(): Promise<boolean>;
}

function statics(): PublicKeyCredentialJSONStatics | undefined {
  const pkc = (globalThis as { PublicKeyCredential?: unknown }).PublicKeyCredential as
    Partial<PublicKeyCredentialJSONStatics> | undefined;
  return pkc && typeof pkc.parseCreationOptionsFromJSON === "function" ? (pkc as PublicKeyCredentialJSONStatics) : undefined;
}

/** Whether this browser can use passkeys the way Linx asks for them. */
export function passkeysSupported(): boolean {
  // A passkey belongs to the domain: none can work on the repair page.
  return window.isSecureContext && statics() !== undefined && !onRepairPage();
}

/** Whether the email box can offer passkeys in the browser's autofill. */
export async function autofillSupported(): Promise<boolean> {
  try {
    return passkeysSupported() && (await statics()?.isConditionalMediationAvailable?.()) === true;
  } catch {
    return false;
  }
}

/** The person closed the browser's prompt, or it was replaced by another. */
export function cancelled(err: unknown): boolean {
  return err instanceof DOMException && (err.name === "NotAllowedError" || err.name === "AbortError");
}

/** A name for a new passkey from this device, e.g. "Mac — Safari". */
export function deviceName(): string {
  const ua = navigator.userAgent;
  const device = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android"
    : /Mac OS X|Macintosh/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : /CrOS/.test(ua) ? "Chromebook"
      : /Linux/.test(ua) ? "Linux" : "This device";
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome"
    : /Safari\//.test(ua) ? "Safari" : "";
  return browser ? `${device} — ${browser}` : device;
}

export class PasskeyError extends Error {
  constructor(readonly code: string, message: string, readonly status = 0) {
    super(message);
  }
}

async function post(path: string, body?: unknown): Promise<Json> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const csrf = csrfToken();
  if (csrf) headers[CSRF_HEADER] = csrf;
  const res = await fetch(path, { method: "POST", credentials: "same-origin", headers, body: JSON.stringify(body ?? {}) });
  const data = (await res.json().catch(() => ({}))) as Json;
  if (!res.ok) {
    throw new PasskeyError(typeof data.code === "string" ? data.code : "",
      typeof data.detail === "string" && data.detail ? data.detail : "Something went wrong. Please try again.", res.status);
  }
  return data;
}

function toJSON(cred: Credential | null): Json {
  if (!cred) throw new DOMException("No passkey chosen", "NotAllowedError");
  return (cred as PublicKeyCredential).toJSON() as unknown as Json;
}

/** Asks the device for a new passkey (the prompt); returns its answer. */
export async function createPasskey(path: string): Promise<Json> {
  const options = await post(`${path}/options`);
  const publicKey = statics()!.parseCreationOptionsFromJSON(options);
  return toJSON(await navigator.credentials.create({ publicKey }));
}

/** Sends a new passkey's answer with its name. */
export function savePasskey(path: string, credential: Json, name: string): Promise<Json> {
  return post(path, { name, credential });
}

/**
 * Asks the device to use one of its passkeys and sends the answer. With
 * `conditional`, the browser offers them in the email box's autofill
 * instead of a prompt, until `signal` aborts.
 */
export async function answerWithPasskey(path: string, opts: { conditional?: boolean; signal?: AbortSignal } = {}): Promise<Json> {
  let options: Json;
  try {
    options = await post(`${path}/options`);
  } catch (err) {
    // Nothing was asked of the person yet: autofill just doesn't start.
    if (opts.conditional) throw new DOMException("Passkey autofill unavailable", "AbortError");
    throw err;
  }
  const publicKey = statics()!.parseRequestOptionsFromJSON(options);
  const cred = await navigator.credentials.get({
    publicKey, signal: opts.signal, ...(opts.conditional ? { mediation: "conditional" as CredentialMediationRequirement } : {}),
  });
  return post(path, { credential: toJSON(cred) });
}
