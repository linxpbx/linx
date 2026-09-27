// A backup destination's failure in plain words (docs/BACKUP.md; the admin
// UI's "no jargon" rule). The server reports restic's and ssh's own text,
// which is kept for "Details"; this picks out the common causes.

const RULES: [RegExp, (host: string) => string][] = [
  [/connection refused/i, (h) => `Couldn't connect to ${h}: nothing answered there.`],
  [/no route to host|network is unreachable|i\/o timeout|timed out/i, (h) => `Couldn't reach ${h}. Is it switched on and on the network?`],
  [/could not resolve|no such host|name or service not known/i, (h) => `Couldn't find ${h}: check its name.`],
  [/host key verification failed|remote host identification has changed/i, (h) => `${h} doesn't look like the server it was when this place was added. Check it before trusting it again.`],
  [/permission denied \(publickey/i, (h) => `${h} refused Linx's login. Add Linx's public key to its authorized_keys.`],
  [/wrong password or no key found/i, () => "The backup's password doesn't open the backups there."],
  [/invalidaccesskeyid|signaturedoesnotmatch|access denied/i, () => "The storage refused Linx's access key."],
  [/nosuchbucket/i, () => "That bucket doesn't exist."],
  [/no space left on device|quota exceeded|disk quota/i, () => "The backup place is full."],
  [/permission denied/i, () => "Linx isn't allowed to write there."],
  [/dumping the database/i, () => "Couldn't read the database to back it up. Is Linx running?"],
];

/** The host a restic repository string names ("sftp:user@host:/path", "s3:https://host/bucket"). */
function hostOf(text: string): string {
  const sftp = /sftp:[^@\s]+@([^:\s]+):/.exec(text);
  if (sftp?.[1]) return sftp[1];
  const ssh = /connect to host (\S+)/.exec(text);
  if (ssh?.[1]) return ssh[1];
  const s3 = /s3:https?:\/\/([^/\s]+)/.exec(text);
  if (s3?.[1]) return s3[1];
  return "the backup place";
}

/** A short plain-words reason, or null when the text isn't one we know (show it as it is). */
export function plainReason(error: string): string | null {
  for (const [re, say] of RULES) {
    if (re.test(error)) return say(hostOf(error));
  }
  return null;
}
