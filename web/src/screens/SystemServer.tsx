// System → Server settings (docs/INSTALL.md §7, docs/ui/INSTALL_SCREENS.md
// §5.2): this server's own settings, for a system admin. They can be
// changed only while `sudo linx setup` has the page open (four hours): the
// server's setup does the change with its own plans and reports each step,
// and Linx restarts what changed. The domain and what's in front of the
// server are shown here; changing them is still the terminal's.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Check, CircleX, LoaderCircle, TriangleAlert } from "lucide-react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { SystemCard as Card, SystemHeader } from "@/components/SystemPage";
import { cn } from "@/lib/utils";

type ServerSettings = components["schemas"]["ServerSettingsView"];

const SIZE_NAMES: Record<string, string> = { lite: "Lite", standard: "Standard", performance: "Performance" };
const DOORS: Record<string, string> = {
  "linx-443": "Linx takes port 443 itself", pangolin: "Pangolin", nginx: "nginx or HAProxy", "http-proxy": "Caddy or Nginx Proxy Manager",
  "home-only": "Nothing: home network only", none: "Nothing yet",
};

export function SystemServerScreen({ me }: { me: Me }) {
  const [open, setOpen] = useState<boolean | null>(null);
  const [s, setS] = useState<ServerSettings | null>(null);
  const [error, setError] = useState("");
  const [unreachable, setUnreachable] = useState(false);

  const load = useCallback(async () => {
    try {
      const { data, error: err } = await api.GET("/api/v1/server-settings");
      if (!data) {
        setError(problemMessage(err));
        return;
      }
      setUnreachable(false);
      setOpen(data.open);
      setS(data.settings ?? null);
    } catch {
      // Linx restarting after a change: keep looking.
      setUnreachable(true);
    }
  }, []);
  const running = s?.apply.state === "running";
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), running || unreachable ? 3000 : 10000);
    return () => clearInterval(t);
  }, [load, running, unreachable]);

  return (
    <div className="mx-auto max-w-5xl">
      <SystemHeader current="/admin/system/server" systemAdmin={me.role === "system_admin"} />
      <div className="mt-6 flex flex-col gap-4">
        {error && <p role="alert" className="text-sm font-medium">{error}</p>}
        {unreachable && (
          <p role="status" className="flex items-center gap-2 text-sm">
            <LoaderCircle aria-hidden="true" className="size-4 animate-spin text-primary" />Linx is restarting with the new settings…
          </p>
        )}
        {open === false && <Closed />}
        {open && s && <Settings me={me} s={s} onChanged={() => void load()} />}
      </div>
    </div>
  );
}

function Closed() {
  return (
    <Card title="Server settings">
      <p className="text-sm text-muted-foreground">
        This server's size, Portainer and DNS token can be changed here only while setup on the server has this page open. On the server,
        run this and choose the Server settings page. It stays open for four hours:
      </p>
      <code className="mt-3 block w-fit rounded-md bg-muted px-3 py-2 font-mono text-sm">sudo linx setup</code>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 border-b py-3 last:border-b-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-sm break-words">{children}</dd>
    </div>
  );
}

function Settings({ me, s, onChanged }: { me: Me; s: ServerSettings; onChanged: () => void }) {
  const confirm = useConfirmIdentity(me);
  const [profile, setProfile] = useState(s.profile);
  const [portainer, setPortainer] = useState(s.portainer);
  const [token, setToken] = useState("");
  const [addToken, setAddToken] = useState(false);
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState("");
  const running = s.apply.state === "running";
  const changed = profile !== s.profile || portainer !== s.portainer || (addToken && token.trim() !== "");
  const company = s.provider === "duckdns" ? "DuckDNS" : "Cloudflare";

  // Back to what the server says once a change is done.
  useEffect(() => {
    if (s.apply.state === "ok") {
      setProfile(s.profile);
      setPortainer(s.portainer);
      setToken("");
      setAddToken(false);
    }
  }, [s.apply.state, s.profile, s.portainer]);

  const apply = async () => {
    setError("");
    const { error: err, response } = await api.POST("/api/v1/server-settings", {
      body: { profile: profile as "lite" | "standard" | "performance", portainer, ...(addToken && token.trim() ? { token: token.trim() } : {}) },
    });
    if (needsConfirm(err)) return { confirm: true as const };
    if (!response.ok) setError(problemMessage(err));
    else onChanged();
    return { confirm: false as const };
  };

  return (
    <>
      <Card title="Server settings">
        <p className="text-sm text-muted-foreground">
          Open for changes until {new Date(s.expires_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}.
        </p>
        <dl className="mt-2">
          <Row label="Where">{s.where === "home" ? "At home or at the office" : "Rented server"}</Row>
          <Row label="In front">{DOORS[s.front_door] ?? s.front_door}</Row>
          <Row label="Domain">{s.domain}</Row>
          <Row label="DNS company">
            {s.token_saved ? `${company} token added ✓` : "No token: the certificate renews through port 443, and DNS records are yours to keep."}
            {!addToken ? (
              <Button variant="link" className="ms-2 h-auto p-0" disabled={running} onClick={() => setAddToken(true)}>
                {s.token_saved ? "Replace token" : "Add a token"}
              </Button>
            ) : (
              <div className="mt-2 flex max-w-md flex-col gap-2">
                <Label htmlFor="server-token">New {company} token</Label>
                <Input id="server-token" type="password" autoComplete="off" spellCheck={false} value={token}
                  onChange={(e) => setToken(e.target.value)} />
                <p className="text-muted-foreground">Linx checks it can see {s.domain} before using it.</p>
              </div>
            )}
          </Row>
          <Row label="Size">
            <RadioGroup value={profile} onValueChange={setProfile} aria-label="Size of this server" className="gap-2" disabled={running}>
              {s.profiles.map((p) => (
                <label key={p.name} htmlFor={`server-size-${p.name}`} className="flex items-start gap-2">
                  <RadioGroupItem id={`server-size-${p.name}`} value={p.name} className="mt-0.5" />
                  <span>
                    <span className="font-medium">{SIZE_NAMES[p.name] ?? p.name}</span>
                    {p.name === s.profile_pick && <span className="ms-2 rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>}
                    <span className="block text-muted-foreground">{p.description.charAt(0).toUpperCase() + p.description.slice(1)}.</span>
                  </span>
                </label>
              ))}
            </RadioGroup>
          </Row>
          {s.portainer_allowed && (
            <Row label="Portainer">
              <div className="flex items-start gap-3">
                <Switch id="server-portainer" aria-label="Portainer" checked={portainer} onCheckedChange={setPortainer} disabled={running} />
                <span className="text-muted-foreground">A web page to look at this server's Docker containers, on your home network only.</span>
              </div>
            </Row>
          )}
        </dl>
        <p className="mt-4 text-sm text-muted-foreground">
          To change the domain or what's in front of this server, run <code className="font-mono text-foreground">sudo linx setup</code> on
          the server and choose the terminal.
        </p>
        {error && <p role="alert" className="mt-3 text-sm font-medium">{error}</p>}
        <div className="mt-4 flex justify-end">
          <Button disabled={!changed || running} onClick={() => setAsking(true)}>Apply</Button>
        </div>
      </Card>
      {s.steps.length > 0 && <Progress s={s} />}
      <Dialog open={asking} onOpenChange={setAsking}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Apply these changes?</DialogTitle>
            <DialogDescription>
              Linx restarts the services whose settings changed, for about a minute. Calls in progress may drop.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAsking(false)}>Cancel</Button>
            <Button onClick={() => { setAsking(false); void confirm.run(apply); }}>Apply</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {confirm.dialog}
    </>
  );
}

function Progress({ s }: { s: ServerSettings }) {
  return (
    <Card title={s.apply.state === "failed" ? "The change stopped" : s.apply.state === "ok" ? "Changed" : "Changing the settings"}>
      <ol className="flex flex-col gap-2" aria-label="Change steps">
        {s.steps.map((st) => (
          <li key={st.title} className="flex min-w-0 gap-2 text-sm">
            <span className="mt-0.5 flex size-4 shrink-0 items-center justify-center">
              {st.state === "ok" ? <Check aria-hidden="true" className="size-4 text-status-available" />
                : st.state === "running" ? <LoaderCircle aria-hidden="true" className="size-4 animate-spin text-primary" />
                  : st.state === "failed" ? <CircleX aria-hidden="true" className="size-4 text-destructive" />
                    : <span aria-hidden="true" className="size-2.5 rounded-full border" />}
            </span>
            <span className={cn("min-w-0", !st.state && "text-muted-foreground")}>
              {st.title}
              {st.state === "failed" && st.detail && <span className="block break-words text-muted-foreground">{st.detail}</span>}
            </span>
          </li>
        ))}
      </ol>
      {s.apply.state === "failed" && (
        <p className="mt-3 text-sm">Nothing that finished is undone. Press Apply again to try the rest.</p>
      )}
      {s.keep.length > 0 && (
        <div className="mt-4 rounded-md border border-status-away p-4 text-sm">
          <p className="flex items-center gap-2 font-medium">
            <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-status-away" />Write this down now
          </p>
          {s.keep.map((k) => (
            <div key={k.title} className="mt-2 min-w-0">
              <p className="font-medium">{k.title}</p>
              <code className="mt-1 block w-fit max-w-full rounded-md bg-muted px-2 py-1 font-mono break-all">{k.value}</code>
              {k.note && <p className="mt-1 text-muted-foreground">{k.note}</p>}
            </div>
          ))}
          <p className="mt-3 text-muted-foreground">It's shown only until this page closes.</p>
        </div>
      )}
    </Card>
  );
}
