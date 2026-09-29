// "Paste what the company sent you" (docs/SIMPLER.md §1.3): reads the
// usual labels in a phone company's settings email or page and fills the
// line's form. Runs in the browser only; nothing is sent anywhere to read
// it, and the admin sees what was understood before anything is saved.

export interface PastedSettings {
  host?: string;
  port?: number;
  username?: string;
  password?: string;
  transport?: "tls" | "tcp" | "udp";
  numbers: string[];
}

// Labels, most specific first: the first match for a field wins over later
// ones (a "registrar" beats a "proxy", an "auth ID" beats a "username").
const HOST = ["registrar", "sip registrar", "sip server", "server address", "sip domain", "sip host", "server", "domain",
  "outbound proxy", "proxy", "host", "gateway", "realm"];
const USER = ["auth id", "authorization id", "auth username", "authentication id", "authorization user", "sip username",
  "sip user", "user name", "username", "user id", "login", "user", "account"];
const PASS = ["sip password", "password", "secret", "pass"];
const PORT = ["sip port", "port"];
const TRANSPORT = ["transport", "protocol"];
const NUMBER = ["did", "dids", "phone number", "phone numbers", "your number", "number", "numbers", "telephone", "tel"];

function rank(label: string, list: string[]): number {
  const l = label.toLowerCase().replace(/[^a-z ]/g, " ").replace(/\s+/g, " ").trim();
  const i = list.findIndex((w) => l === w || l.endsWith(` ${w}`) || l.startsWith(`${w} `));
  return i;
}

function cleanHost(v: string): { host?: string; port?: number } {
  let s = v.trim().replace(/^sips?:/i, "").replace(/^[a-z]+:\/\//i, "").replace(/[/;].*$/, "");
  s = s.replace(/^.*@/, "");
  const m = /^([A-Za-z0-9.-]+)(?::(\d{1,5}))?$/.exec(s);
  if (!m || !m[1]!.includes(".")) return {};
  return { host: m[1]!.replace(/\.$/, "").toLowerCase(), port: m[2] ? Number(m[2]) : undefined };
}

function numbersIn(v: string): string[] {
  return (v.match(/\+?\d[\d ()-]{5,}\d/g) ?? []).map((n) => n.replace(/[^\d+]/g, "")).filter((n) => n.replace("+", "").length >= 6);
}

/** Reads settings out of pasted text: "Label: value" (or "=", or a tab) per line, or the value on the next line. */
export function parseProviderText(text: string): PastedSettings {
  const out: PastedSettings = { numbers: [] };
  const best: Record<string, number> = {};
  const set = <K extends keyof PastedSettings>(field: K, value: PastedSettings[K], r: number) => {
    if (r < 0 || value === undefined || value === "" || (best[field] !== undefined && best[field]! <= r)) return;
    best[field] = r;
    out[field] = value;
  };
  const lines = text.split(/\r?\n/).map((l) => l.trim());
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    const m = /^([^:=\t]{2,40}?)\s*(?::|=|\t|\s[-–]\s)\s*(.*)$/.exec(line);
    let pair: [string, string] | null = m ? [m[1]!, m[2]!] : null;
    // A label alone on its line, its value on the next.
    if (!pair && /^[A-Za-z][A-Za-z /()-]{1,38}$/.test(line) && lines[i + 1]) pair = [line, lines[i + 1]!];
    if (!pair) continue;
    const label = pair[0];
    const value = pair[1].trim().replace(/^["'`]|["'`]$/g, "");
    if (!value) continue;
    const h = rank(label, HOST);
    if (h >= 0) {
      const c = cleanHost(value);
      set("host", c.host, h);
      if (c.port) set("port", c.port, 100);
    }
    set("username", /^\S{1,128}$/.test(value) ? value : undefined, rank(label, USER));
    set("password", /^\S{1,128}$/.test(value) ? value : undefined, rank(label, PASS));
    const p = rank(label, PORT);
    if (p >= 0 && /^\d{1,5}$/.test(value)) set("port", Number(value), p);
    const t = rank(label, TRANSPORT);
    if (t >= 0) {
      const v = value.toLowerCase();
      set("transport", v.includes("tls") ? "tls" : v.includes("tcp") ? "tcp" : v.includes("udp") ? "udp" : undefined, t);
    }
    if (rank(label, NUMBER) >= 0) {
      for (const n of numbersIn(value)) if (!out.numbers.includes(n)) out.numbers.push(n);
    }
  }
  // A host that is really the username's domain ("user@sip.example.com").
  if (!out.host && out.username?.includes("@")) {
    const c = cleanHost(out.username.split("@")[1]!);
    if (c.host) { out.host = c.host; out.username = out.username.split("@")[0]; }
  }
  return out;
}
