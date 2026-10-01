// System → Server settings (docs/INSTALL.md §7, docs/ui/INSTALL_SCREENS.md
// §5.2): this server's own settings, for a system admin. They can be
// changed only while `sudo linx setup` has the page open (four hours): the
// server's setup does the change with its own plans and reports each step,
// and Linx restarts what changed. A new domain or front door is checked
// first (the preview): the DNS records to add, the front door's block to
// paste, and what else it means. The same panel is the repair page on port
// 6464 (screens/Repair.tsx).
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Check, CircleX, LoaderCircle, TriangleAlert } from "lucide-react";
import { problemCode, problemMessage, type Me } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { CheckIt } from "@/components/CheckIt";
import { DnsRecords } from "@/components/DnsRecords";
import { BY_HAND, DnsKeyFields } from "@/components/DnsKeyForm";
import { FrontDoorCard } from "@/components/FrontDoorCard";
import { PublicPortFields, PublicPortWarning } from "@/components/PublicPortChoice";
import { CopyBlock, Disclosure, PortainerNote, RecordBox } from "@/components/InstallFrame";
import { SystemCard as Card, SystemHeader } from "@/components/SystemPage";
import { domainProblem, portProblem, proxyAddressProblem, publicPortProblem } from "@/lib/install";
import {
  ADVANCED_DOORS, doorChoice, DOORS, PROXY_DOORS, sessionClient,
  type FrontDoorKind, type ServerChange, type ServerPreview, type ServerSettings, type ServerSettingsClient,
} from "@/lib/serverSettings";
import { companyById, dnsKeyBody, dnsKeyFilled, startingCompany, type DnsKeyValue } from "@/lib/dnsCompanies";
import { cn } from "@/lib/utils";

const SIZE_NAMES: Record<string, string> = { lite: "Lite", standard: "Standard", performance: "Performance" };
const SERVER_PATH = "/admin/system/server";

export function SystemServerScreen({ me }: { me: Me }) {
  return (
    <div className="mx-auto max-w-5xl">
      <SystemHeader current={SERVER_PATH} systemAdmin={me.role === "system_admin"} />
      <div className="mt-6">
        <ServerSettingsPanel me={me} client={sessionClient} afterMove={SERVER_PATH} />
      </div>
    </div>
  );
}

/**
 * The settings, polled from the server while the page is open. afterMove:
 * the path to open at the new address after a new domain.
 */
export function ServerSettingsPanel({ me, client, afterMove = "" }: { me: Me | null; client: ServerSettingsClient; afterMove?: string }) {
  const [open, setOpen] = useState<boolean | null>(null);
  const [s, setS] = useState<ServerSettings | null>(null);
  const [error, setError] = useState("");
  const [unreachable, setUnreachable] = useState(false);
  // Where Linx answers after a change that moved it.
  const [movedTo, setMovedTo] = useState("");

  const load = useCallback(async () => {
    try {
      const { data, error: err } = await client.load();
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
  }, [client]);
  const running = s?.apply.state === "running";
  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), running || unreachable ? 3000 : 10000);
    return () => clearInterval(t);
  }, [load, running, unreachable]);
  const here = s && movedTo === (s.address || `https://${s.domain}`);

  return (
    <div className="flex flex-col gap-4">
      {error && <p role="alert" className="text-sm font-medium">{error}</p>}
      {unreachable && (
        <p role="status" className="flex items-start gap-2 text-sm">
          <LoaderCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0 animate-spin text-primary" />
          <span>
            Linx is restarting with the new settings…
            {movedTo && <> When it's done it answers at <a className="text-link underline-offset-4 hover:underline break-all" href={movedTo + afterMove}>{movedTo}</a>:
              open it there and sign in again.</>}
          </span>
        </p>
      )}
      {movedTo && here && s?.apply.state === "ok" && !unreachable && (
        <p role="status" className="flex items-start gap-2 rounded-md border px-4 py-3 text-sm">
          <Check aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-available" />
          <span>Linx now answers at <a className="text-link underline-offset-4 hover:underline break-all" href={movedTo + afterMove}>{movedTo}</a>.</span>
        </p>
      )}
      {open === false && <Closed />}
      {open && s && <Settings me={me} s={s} client={client} onChanged={() => void load()} onMoved={setMovedTo} />}
    </div>
  );
}

function Closed() {
  return (
    <Card title="Server settings">
      <p className="text-sm text-muted-foreground">
        This server's domain, front door, size, Portainer and DNS key can be changed here only while setup on the server has this page
        open. On the server, run this and choose the Server settings page. It stays open for four hours:
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

function ChangeLink({ open, label, disabled, onClick }: { open: boolean; label: string; disabled: boolean; onClick: () => void }) {
  if (open) return null;
  return <Button variant="link" className="ms-2 h-auto p-0" disabled={disabled} onClick={onClick}>{label}</Button>;
}

function FieldError({ message }: { message?: string }) {
  if (!message) return null;
  return <p role="alert" className="text-sm font-medium text-destructive">{message}</p>;
}

function Settings({ me, s, client, onChanged, onMoved }:
  { me: Me | null; s: ServerSettings; client: ServerSettingsClient; onChanged: () => void; onMoved: (to: string) => void }) {
  const confirm = useConfirmIdentity(me);
  const [profile, setProfile] = useState(s.profile);
  const [portainer, setPortainer] = useState(s.portainer);
  // The DNS company's key (docs/ui/SCREENS_PHASE1F.md §3.2), and Stop /
  // start again (null: unchanged).
  const [detected, setDetected] = useState("");
  const [dnsKey, setDnsKey] = useState<DnsKeyValue>({ provider: startingCompany(s.domain, s.provider), key: {} });
  const [addToken, setAddToken] = useState(false);
  const [byHand, setByHand] = useState<boolean | null>(null);
  const [editDomain, setEditDomain] = useState(false);
  const [domain, setDomain] = useState("");
  const [editDoor, setEditDoor] = useState(false);
  const [door, setDoor] = useState<string>(doorChoice(s.front_door));
  const [advanced, setAdvanced] = useState(ADVANCED_DOORS.includes(s.front_door));
  const [proxy, setProxy] = useState(s.proxy_address ?? "");
  const [udp, setUdp] = useState(String(s.turn_udp_port || 443));
  // Another public port (docs/ui/SCREENS_PHASE1F.md §4): the owner's
  // warning first, then the ports.
  const [publicPort, setPublicPort] = useState(String(s.public_port || 8443));
  const [portAccepted, setPortAccepted] = useState(s.front_door === "public-port");
  const [preview, setPreview] = useState<ServerPreview | null>(null);
  // Which row's Check showed the preview (it's shown there).
  const [checkedAt, setCheckedAt] = useState<CheckAt | null>(null);
  // A new token checked on its own: the DNS company let Linx see the domain.
  const [tokenOK, setTokenOK] = useState(false);
  const [doorDone, setDoorDone] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [checking, setChecking] = useState(false);
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState("");
  const running = s.apply.state === "running";
  const company = companyById(s.provider)?.name ?? s.provider;
  const keyGiven = addToken && dnsKeyFilled(dnsKey);

  const newDomain = editDomain ? domain.trim().toLowerCase() : "";
  const proxyDoor = PROXY_DOORS.includes(door);
  const portDoor = door === "public-port";
  const doorChanged = editDoor && (door !== doorChoice(s.front_door) || (proxyDoor && (proxy.trim() !== (s.proxy_address ?? "") ||
    Number(udp) !== (s.turn_udp_port || 443))) || (portDoor && (Number(publicPort) !== s.public_port || Number(udp) !== (s.turn_udp_port || 443))));
  const address = s.address || `https://${s.domain}`;
  const moves = (newDomain !== "" && newDomain !== s.domain) || doorChanged;
  const changed = profile !== s.profile || portainer !== s.portainer || keyGiven || byHand !== null || moves;

  // Back to what the server says once a change is done.
  useEffect(() => {
    if (s.apply.state === "ok") {
      setProfile(s.profile);
      setPortainer(s.portainer);
      setDnsKey({ provider: startingCompany(s.domain, s.provider), key: {} });
      setAddToken(false);
      setByHand(null);
      setEditDomain(false);
      setDomain("");
      setEditDoor(false);
      setDoor(doorChoice(s.front_door));
      setProxy(s.proxy_address ?? "");
      setUdp(String(s.turn_udp_port || 443));
      setPublicPort(String(s.public_port || 8443));
      setPortAccepted(s.front_door === "public-port");
      setPreview(null);
      setDoorDone(false);
    }
  }, [s.apply.state, s.profile, s.portainer, s.front_door, s.proxy_address, s.turn_udp_port, s.public_port, s.domain, s.provider]);
  // Anything edited after a preview needs a new one.
  useEffect(() => { setPreview(null); setCheckedAt(null); setTokenOK(false); setDoorDone(false); }, [profile, portainer, dnsKey, byHand, domain, door, proxy, udp, publicPort, editDomain, editDoor]);

  const body = (): ServerChange => ({
    profile: profile as ServerChange["profile"], portainer,
    ...(keyGiven ? { dns_key: dnsKeyBody(dnsKey) as ServerChange["dns_key"] } : {}),
    ...(byHand !== null ? { dns_by_hand: byHand } : {}),
    ...(newDomain && newDomain !== s.domain ? { domain: newDomain } : {}),
    ...(doorChanged ? {
      front_door: door as FrontDoorKind,
      ...(proxyDoor ? { proxy_address: proxy.trim(), turn_udp_port: Number(udp) === 443 ? 0 : Number(udp) } : {}),
      ...(portDoor ? { public_port: Number(publicPort) || 0, turn_udp_port: Number(udp) === 443 ? 0 : Number(udp) } : {}),
    } : {}),
    ...(doorDone ? { door_done: true } : {}),
  });

  // The page's own checks, before asking the server.
  const localProblems = (): Record<string, string> => {
    const p: Record<string, string> = {};
    if (editDomain && newDomain && newDomain !== s.domain && domainProblem(newDomain)) p.domain = domainProblem(newDomain);
    if (doorChanged && proxyDoor) {
      if (proxyAddressProblem(proxy)) p.proxy_address = proxyAddressProblem(proxy);
      if (portProblem(Number(udp))) p.turn_udp_port = portProblem(Number(udp));
    }
    if (doorChanged && portDoor) {
      if (publicPortProblem(Number(publicPort))) p.public_port = publicPortProblem(Number(publicPort));
      if (portProblem(Number(udp))) p.turn_udp_port = portProblem(Number(udp));
    }
    return p;
  };

  // at: the row whose Check was pressed, or "apply" (the button at the
  // bottom, which checks first too).
  const check = async (at: CheckAt | "apply") => {
    setError("");
    const local = localProblems();
    setFieldErrors(local);
    if (Object.keys(local).length > 0) return;
    setChecking(true);
    try {
      const { data, error: err } = await client.preview(body());
      if (!data) {
        setError(problemMessage(err));
        return;
      }
      if (data.errors.length > 0) {
        setFieldErrors(Object.fromEntries(data.errors.map((e) => [e.field, e.message])));
        return;
      }
      // The key's own Check answers for the key, even with a move pending:
      // the move's preview comes from its own row's Check.
      if (at === "token") setTokenOK(true);
      else if (moves) {
        setPreview(data);
        setCheckedAt(at === "apply" ? (newDomain && newDomain !== s.domain ? "domain" : "door") : at);
      } else setAsking(true);
    } catch {
      setError("Linx didn't answer. Try again in a moment.");
    } finally {
      setChecking(false);
    }
  };

  const apply = async () => {
    setError("");
    const { error: err, status } = await client.change(body());
    if (needsConfirm(err)) return { confirm: true as const };
    if (status >= 400) {
      if (problemCode(err) === "token_refused") setFieldErrors({ token: problemMessage(err) });
      else setError(problemMessage(err));
    } else {
      if (preview && preview.address !== address) onMoved(preview.address);
      setPreview(null);
      onChanged();
    }
    return { confirm: false as const };
  };

  const needsTick = !!preview?.setup;
  const review = preview && (
    <Review preview={preview} doorDone={doorDone} onDoorDone={setDoorDone} running={running}
      onCancel={() => { setPreview(null); setCheckedAt(null); }} onApply={() => setAsking(true)} ready={!needsTick || doorDone} />
  );
  return (
    <>
      <Card title="Server settings">
        <p className="text-sm text-muted-foreground">
          Open for changes until {new Date(s.expires_at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}.
        </p>
        <dl className="mt-2">
          <Row label="Where">{s.where === "home" ? "At home or at the office" : "Rented server"}</Row>
          <Row label="In front">
            {DOORS[s.front_door] ?? s.front_door}
            {s.proxy_address && PROXY_DOORS.includes(s.front_door) && <span className="text-muted-foreground"> at {s.proxy_address}</span>}
            {s.front_door === "public-port" && <span className="text-muted-foreground"> on port {s.public_port}: {address}</span>}
            <ChangeLink open={editDoor} label="Change" disabled={running} onClick={() => setEditDoor(true)} />
            {editDoor && (
              <div className="mt-3 flex max-w-md flex-col gap-3">
                <RadioGroup value={door} aria-label="What's in front of this server" className="gap-2" onValueChange={(d) => {
                  setDoor(d);
                  if (d !== "public-port") setPortAccepted(s.front_door === "public-port");
                }}>
                  {s.front_doors.filter((d) => !ADVANCED_DOORS.includes(d) || advanced).map((d) => (
                    <label key={d} htmlFor={`server-door-${d}`} className="flex items-center gap-2">
                      <RadioGroupItem id={`server-door-${d}`} value={d} />
                      <span>{DOORS[d] ?? d}</span>
                    </label>
                  ))}
                </RadioGroup>
                {s.front_doors.some((d) => ADVANCED_DOORS.includes(d)) && !advanced && (
                  <Button variant="link" className="h-auto w-fit p-0" onClick={() => setAdvanced(true)}>Advanced</Button>
                )}
                {door === "http-proxy" && (
                  <p className="flex items-start gap-2 text-sm">
                    <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
                    <span className="min-w-0">
                      Not recommended: your proxy sees everything that passes through it, and calls need their own port for audio.
                      Caddy (with its layer-4 add-on), nginx and HAProxy can pass Linx through instead (“Another program passes Linx through”).
                    </span>
                  </p>
                )}
                {portDoor && !portAccepted && (
                  <PublicPortWarning onDoors={() => setDoor(s.where === "rented" ? "linx-443" : "proxy")} onUse={() => {
                    setPortAccepted(true);
                    // Certificates then need the DNS company's key (§4.2).
                    if (!s.token_saved) {
                      setAddToken(true);
                      setByHand(null);
                      setDnsKey({ provider: startingCompany(s.domain, undefined, detected), key: {} });
                    }
                  }} />
                )}
                {portDoor && portAccepted && (
                  <PublicPortFields id="server" port={publicPort} onPort={setPublicPort} udp={Number(udp) || 0} onUdp={(v) => setUdp(String(v))}
                    domain={newDomain || s.domain} lanAddress={s.where === "home" ? s.lan_address : undefined}
                    errors={{ public_port: fieldErrors.public_port, turn_udp_port: fieldErrors.turn_udp_port }} />
                )}
                {proxyDoor && (
                  <>
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor="server-proxy">Its address on your network</Label>
                      <Input id="server-proxy" inputMode="decimal" placeholder="192.168.1.30" value={proxy} onChange={(e) => setProxy(e.target.value)}
                        aria-invalid={fieldErrors.proxy_address ? true : undefined} />
                      <FieldError message={fieldErrors.proxy_address} />
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor="server-udp">UDP port your router sends to Linx for calls</Label>
                      <Input id="server-udp" inputMode="numeric" className="w-32" value={udp} onChange={(e) => setUdp(e.target.value)}
                        aria-invalid={fieldErrors.turn_udp_port ? true : undefined} />
                      <FieldError message={fieldErrors.turn_udp_port} />
                    </div>
                  </>
                )}
                <FieldError message={fieldErrors.front_door} />
                <div className="flex flex-wrap items-center gap-3">
                  <CheckButton busy={checking} disabled={!doorChanged || running || !!preview} onClick={() => void check("door")} />
                  <Button variant="link" className="h-auto w-fit p-0" onClick={() => { setEditDoor(false); setDoor(doorChoice(s.front_door)); }}>Keep it as it is</Button>
                </div>
              </div>
            )}
            {preview && checkedAt === "door" && review}
            {me && !editDoor && !running && <CheckIt className="mt-3" />}
          </Row>
          <Row label="Domain">
            {s.domain}
            <ChangeLink open={editDomain} label="Change" disabled={running} onClick={() => setEditDomain(true)} />
            {editDomain && (
              <div className="mt-3 flex max-w-md flex-col gap-1.5">
                <Label htmlFor="server-domain">New domain</Label>
                <div className="flex gap-2">
                  <Input id="server-domain" autoComplete="off" spellCheck={false} placeholder="example.com" value={domain} className="min-w-0 flex-1"
                    onChange={(e) => setDomain(e.target.value)} aria-invalid={fieldErrors.domain ? true : undefined} />
                  <CheckButton busy={checking} disabled={!newDomain || newDomain === s.domain || running || !!preview} onClick={() => void check("domain")} />
                </div>
                <FieldError message={fieldErrors.domain} />
                <p className="text-muted-foreground">Linx moves to it: passkeys and desk phones set up for {s.domain} need attention.</p>
                <Button variant="link" className="h-auto w-fit p-0" onClick={() => { setEditDomain(false); setDomain(""); }}>Keep {s.domain}</Button>
              </div>
            )}
            {preview && checkedAt === "domain" && review}
          </Row>
          <Row label="DNS">
            {me && !editDomain && !running && (
              <DnsRecords className="mb-3" onLoaded={(r) => setDetected(r.company)}
                onAutomatic={!s.token_saved || s.dns_by_hand ? () => {
                  setAddToken(!s.token_saved);
                  setByHand(s.dns_by_hand ? false : null);
                  setDnsKey({ provider: startingCompany(s.domain, s.token_saved ? s.provider : undefined, detected), key: {} });
                } : undefined} />
            )}
            {s.token_saved
              ? s.dns_by_hand
                ? `${company} key added. You keep the records yourself.`
                : `${company}, kept right automatically ✓`
              : "No key: the certificate renews through port 443, and DNS records are yours to keep."}
            {s.token_saved && s.dns_by_hand && byHand === null &&
              <ChangeLink open={false} label="Let Linx keep them right" disabled={running} onClick={() => setByHand(false)} />}
            <ChangeLink open={addToken} label={s.token_saved ? "Replace key" : "Set it up automatically"} disabled={running}
              onClick={() => {
                setAddToken(true);
                if (!s.token_saved) setByHand(null);
                setDnsKey({ provider: startingCompany(s.domain, s.token_saved ? s.provider : undefined, detected), key: {} });
              }} />
            {s.token_saved && !s.dns_by_hand && byHand === null &&
              <ChangeLink open={false} label="Stop" disabled={running} onClick={() => setByHand(true)} />}
            {byHand !== null && !addToken && (
              <p role="status" className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
                {byHand
                  ? "Linx stops changing the records; they stay as they are now. The key still renews the certificate. Press Apply."
                  : `Linx keeps the records right again at ${company}. Press Apply.`}
                <Button variant="link" className="h-auto p-0" onClick={() => setByHand(null)}>Cancel</Button>
              </p>
            )}
            {addToken && (
              <div className="mt-3 flex max-w-md flex-col gap-3">
                <h3 className="font-display text-base font-semibold">Keep the records right automatically</h3>
                <DnsKeyFields idPrefix="server-dns" domain={newDomain || s.domain} address={s.public_address} detected={detected}
                  value={dnsKey} onChange={setDnsKey} disabled={running} />
                {dnsKey.provider !== BY_HAND && (
                  <>
                    <p className="text-muted-foreground">
                      Linx changes only {newDomain || s.domain}'s own records, never one it didn't make. The key stays on this server, readable
                      only by its certificate service.
                    </p>
                    <div className="flex flex-wrap items-center gap-3">
                      <Button variant="outline" disabled={!dnsKeyFilled(dnsKey) || running || checking} onClick={() => void check("token")}>
                        {checking && <LoaderCircle aria-hidden="true" className="animate-spin" />}Check the key
                      </Button>
                    </div>
                  </>
                )}
                <FieldError message={fieldErrors.token} />
                {tokenOK && (
                  <p role="status" className="flex items-center gap-2">
                    <Check aria-hidden="true" className="size-4 shrink-0 text-status-available" />
                    This key can change {newDomain || s.domain}'s records. Press Apply to save it.
                  </p>
                )}
                <Button variant="link" className="h-auto w-fit p-0" onClick={() => { setAddToken(false); setByHand(null); }}>
                  {s.token_saved ? "Keep the key there is" : "Not now"}
                </Button>
              </div>
            )}
            {!addToken && <FieldError message={fieldErrors.token} />}
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
          {!s.portainer_allowed && (
            <Row label="Portainer">
              <PortainerNote />
            </Row>
          )}
        </dl>
        {error && <p role="alert" className="mt-3 text-sm font-medium">{error}</p>}
        {!preview && (
          <div className="mt-4 flex justify-end">
            <Button disabled={!changed || running || checking} onClick={() => void check("apply")}>
              {checking && <LoaderCircle aria-hidden="true" className="animate-spin" />}
              Apply
            </Button>
          </div>
        )}
      </Card>
      {s.steps.length > 0 && <Progress s={s} />}
      <Dialog open={asking} onOpenChange={setAsking}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{preview && preview.address !== address ? `Move Linx to ${preview.address}?` : "Apply these changes?"}</DialogTitle>
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

/** What a new domain or front door needs first, and what it means. */
function Review({ preview, doorDone, onDoorDone, ready, running, onCancel, onApply }: {
  preview: ServerPreview; doorDone: boolean; onDoorDone: (v: boolean) => void; ready: boolean; running: boolean;
  onCancel: () => void; onApply: () => void;
}) {
  return (
    <section aria-labelledby="server-review" className="mt-4 rounded-md border p-4">
      <h3 id="server-review" className="font-display text-base font-semibold">Before you apply</h3>
      <div className="mt-3 flex flex-col gap-5 text-sm">
        {preview.warnings.length > 0 && (
          <ul className="flex flex-col gap-2">
            {preview.warnings.map((w) => (
              <li key={w} className="flex min-w-0 items-start gap-2">
                <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-status-away" />
                <span className="min-w-0 break-words">{w}</span>
              </li>
            ))}
          </ul>
        )}
        {preview.add_records.length > 0 && (
          <section className="flex flex-col gap-2">
            <h3 className="font-medium">DNS records to add</h3>
            {preview.add_records.map((r) => <RecordBox key={r.name} record={r} />)}
          </section>
        )}
        {preview.setup?.card && (
          <FrontDoorCard card={preview.setup.card} router={preview.setup.steps}>
            <div className="flex items-start gap-3">
              <Checkbox id="server-door-done" checked={doorDone} onCheckedChange={(c) => onDoorDone(c === true)} className="mt-0.5" />
              <Label htmlFor="server-door-done" className="font-normal leading-snug">I've done these steps</Label>
            </div>
          </FrontDoorCard>
        )}
        {preview.setup && !preview.setup.card && (
          <section className="flex flex-col gap-2">
            <h3 className="font-medium">What's in front of this server</h3>
            <ul className="list-disc space-y-1 ps-5">
              {preview.setup.steps.map((line) => <li key={line}>{line}</li>)}
            </ul>
            {preview.setup.files.map((f) => (
              <Disclosure key={f.title} label={`Show ${f.title}`}>
                {f.path && <p className="mb-2 text-muted-foreground">Goes in {f.path}.</p>}
                <CopyBlock text={f.text} label={f.title} />
              </Disclosure>
            ))}
            <div className="mt-1 flex items-start gap-3">
              <Checkbox id="server-door-done" checked={doorDone} onCheckedChange={(c) => onDoorDone(c === true)} className="mt-0.5" />
              <Label htmlFor="server-door-done" className="font-normal leading-snug">I've done these steps</Label>
            </div>
          </section>
        )}
        <section className="flex flex-col gap-2">
          <h3 className="font-medium">Setup on the server will</h3>
          <ol className="list-decimal space-y-1 ps-5">
            {preview.steps.map((st) => <li key={st}>{st}</li>)}
          </ol>
        </section>
        <div className="flex flex-wrap justify-end gap-3">
          <Button variant="outline" onClick={onCancel}>Back</Button>
          <Button disabled={!ready || running} onClick={onApply}>Apply</Button>
        </div>
      </div>
    </section>
  );
}

type CheckAt = "domain" | "door" | "token";

/** The Check next to a field that setup on the server checks. */
function CheckButton({ busy, disabled, onClick }: { busy: boolean; disabled: boolean; onClick: () => void }) {
  return (
    <Button type="button" variant="outline" disabled={disabled || busy} onClick={onClick}>
      {busy && <LoaderCircle aria-hidden="true" className="animate-spin" />}
      Check
    </Button>
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
