// The activity log in plain sentences (docs/ui/ADMIN_SCREENS_PHASE1E.md
// §10.3). An audit entry's action is "<thing>.<what happened>"; unknown
// ones fall back to the words themselves, so a new action still reads.

const THINGS: Record<string, string> = {
  user: "person", extension: "extension", device: "phone", trunk: "phone line", trunk_did: "phone number", did: "phone number",
  wireguard_profile: "connection", call_permission_level: "calling rules", outbound_routing: "outgoing call order",
  settings: "settings", alert_channel: "place for alerts", api_key: "API key", oauth_client: "app login",
  webhook: "webhook", webhook_delivery: "webhook delivery", outbound_allowlist: "home-network address", sso_provider: "company sign-in",
  backup: "backup", system: "service", sip: "browser phone line",
  voicemail: "voicemail", voicemail_box: "voicemail box", voicemail_greeting: "voicemail greeting",
};
const VERBS: Record<string, string> = {
  create: "Added", update: "Changed", delete: "Removed", revoke: "Removed", disable: "Turned off", unlock: "Unlocked",
  reset_password: "Made a new password for", rotate_secret: "Made a new secret for", replay: "Sent again", test: "Tested",
  service_restart: "Restarted",
};
// Whole actions with their own sentence.
const SENTENCES: Record<string, string> = {
  "user.sign_in": "Signed in", "user.sign_in_code": "Entered a sign-in code", "user.mfa_enabled": "Set up an authenticator app",
  "user.mfa_reset": "Reset an authenticator app", "user.passkey_added": "Added a passkey", "user.passkey_removed": "Removed a passkey",
  "user.passkey_renamed": "Renamed a passkey", "user.password_added": "Added a password", "user.password_changed": "Changed a password",
  "user.password_set": "Set a password", "user.password_only_accepted": "Chose to sign in with a password only",
  "backup.password_shown": "Showed a backup file's password", "sip.relay_closed": "Closed a browser phone line",
  "system.service_restart": "Restarted a service",
  "voicemail_greeting.record": "Recorded a voicemail greeting", "voicemail.keep_days": "Changed how long voicemail is kept",
  "voicemail.play": "Listened to the voicemail of",
};

export interface AuditEntryLike {
  action: string; result: string; target?: string; detail?: Record<string, unknown>;
}

function nameIn(detail?: Record<string, unknown>): string {
  for (const k of ["name", "email", "number", "display_name", "service"]) {
    const v = detail?.[k];
    if (typeof v === "string" && v) return v;
  }
  return "";
}

export function activitySentence(e: AuditEntryLike): string {
  const [thing = "", ...rest] = e.action.split(".");
  const what = rest.join(".");
  const name = nameIn(e.detail);
  let s = SENTENCES[e.action];
  if (!s) {
    const verb = VERBS[what] ?? (what ? what.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase()) : "Changed");
    s = `${verb} ${THINGS[thing] ?? thing.replace(/_/g, " ")}`;
  }
  if (name) s += ` ${/^[+\d]/.test(name) ? name : `"${name}"`}`;
  if (e.result === "denied") s = `Refused: ${s.charAt(0).toLowerCase()}${s.slice(1)}`;
  if (e.result === "failed") s = `Failed: ${s.charAt(0).toLowerCase()}${s.slice(1)}`;
  const reason = e.detail?.reason;
  if (typeof reason === "string" && reason && e.result !== "ok") s += ` (${reason.replace(/_/g, " ")})`;
  return s;
}

/** "What" filter choices: an action prefix each. */
export const ACTIVITY_KINDS: { value: string; label: string }[] = [
  { value: "", label: "Any change" },
  { value: "user.sign_in", label: "Sign-ins" },
  { value: "user.", label: "People" },
  { value: "extension.", label: "Extensions" },
  { value: "device.", label: "Phones" },
  { value: "trunk", label: "Phone lines and numbers" },
  { value: "settings.", label: "Settings" },
  { value: "api_key.", label: "API keys" },
  { value: "webhook", label: "Webhooks" },
  { value: "alert_channel.", label: "Alerts" },
  { value: "backup.", label: "Backups" },
];
