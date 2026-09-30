// System → Settings (docs/ui/ADMIN_SCREENS_PHASE1E.md §10.5): numbers,
// place, Simple mode, where admins may sign in from, company sign-in,
// Help's written answers (docs/HELP.md §4), and running the setup wizard
// again.
import { useCallback, useEffect, useState } from "react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Guarded, SystemCard, SystemHeader } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { navigate } from "@/hooks/useRoute";
import { goToCompany } from "@/lib/company";
import { hasScope } from "@/lib/roles";
import { HelpAnswersCard } from "@/screens/SystemHelpAnswers";

type Settings = components["schemas"]["Settings"];
type Provider = components["schemas"]["SsoProvider"];
type ProviderKind = components["schemas"]["SsoProviderKind"];
type User = components["schemas"]["User"];

function Row({ label, children, action }: { label: string; children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b py-3 last:border-b-0">
      <span className="w-40 text-sm font-medium">{label}</span>
      <span className="min-w-0 flex-1 text-sm">{children}</span>
      {action}
    </div>
  );
}

// --- Add a company sign-in provider (§10.5) -----------------------------------------

const PROVIDERS: { kind: ProviderKind; label: string; hint: string; steps: string[]; issuer: "filled" | "ask"; issuerHint?: string }[] = [
  { kind: "google", label: "Google", hint: "Gmail and Google Workspace accounts.", issuer: "filled", steps: [
    "Open console.cloud.google.com → APIs & Services → Credentials.",
    "Create credentials → OAuth client ID → Web application.",
    "Under Authorized redirect URIs, add the address below.",
    "Copy the Client ID and Client secret it shows.",
  ] },
  { kind: "microsoft", label: "Microsoft", hint: "Microsoft 365 work accounts.", issuer: "ask",
    issuerHint: "https://login.microsoftonline.com/<Directory (tenant) ID>/v2.0", steps: [
      "Open entra.microsoft.com → App registrations → New registration.",
      "Redirect URI: Web, the address below.",
      "Copy the Application (client) ID and the Directory (tenant) ID.",
      "Certificates & secrets → New client secret, and copy its Value.",
    ] },
  { kind: "authentik", label: "Authentik", hint: "Your own Authentik server.", issuer: "ask", issuerHint: "https://auth.example.com/application/o/linx/", steps: [
    "Applications → Providers → Create → OAuth2/OpenID Provider, confidential.",
    "Redirect URIs: the address below.",
    "Copy the Client ID, Client Secret, and the OpenID Configuration Issuer.",
  ] },
  { kind: "keycloak", label: "Keycloak", hint: "Your own Keycloak server.", issuer: "ask", issuerHint: "https://sso.example.com/realms/<realm>", steps: [
    "Clients → Create client, OpenID Connect, Client authentication on.",
    "Valid redirect URIs: the address below.",
    "Copy the Client ID, and from Credentials the Client secret.",
  ] },
  { kind: "oidc", label: "Another OpenID Connect provider", hint: "Anything that speaks OpenID Connect.", issuer: "ask", steps: [
    "Create a confidential (web) client at your provider.",
    "Allow the address below as its redirect address.",
    "Copy its client ID, secret and issuer address.",
  ] },
];

function AddProvider({ me, open, onOpenChange, redirect, onDone }: {
  me: Me; open: boolean; onOpenChange: (o: boolean) => void; redirect: string; onDone: () => void;
}) {
  const [step, setStep] = useState(1);
  const [kind, setKind] = useState<ProviderKind>("google");
  const [name, setName] = useState("");
  const [issuer, setIssuer] = useState("");
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [shown, setShown] = useState(true);
  const [created, setCreated] = useState<Provider | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const p = PROVIDERS.find((x) => x.kind === kind)!;

  useEffect(() => {
    if (open) { setStep(1); setKind("google"); setName(""); setIssuer(""); setClientId(""); setSecret(""); setShown(true); setCreated(null); setError(""); }
  }, [open]);

  const create = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/sso-providers", {
      body: { kind, client_id: clientId.trim(), client_secret: secret.trim(), shown, enabled: true,
        ...(name.trim() ? { name: name.trim() } : {}), ...(p.issuer === "ask" ? { issuer: issuer.trim() } : {}) },
    });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setCreated(data);
    setStep(5);
    return { confirm: false };
  });

  const close = () => { onOpenChange(false); if (created) onDone(); };
  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Add company sign-in</SheetTitle>
          <SheetDescription>1 Which · 2 Make an app · 3 Its keys · 4 Sign-in page · 5 Try it</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4 text-sm">
          {step === 1 && (
            <RadioGroup value={kind} onValueChange={(v) => setKind(v as ProviderKind)}>
              {PROVIDERS.map((x) => (
                <label key={x.kind} htmlFor={`sso-${x.kind}`} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                  <RadioGroupItem id={`sso-${x.kind}`} value={x.kind} className="mt-0.5" />
                  <span className="flex flex-col gap-0.5">
                    <span className="flex flex-wrap items-center gap-2 font-medium">
                      {x.label}{x.kind === "google" && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended for Gmail and Workspace</span>}
                    </span>
                    <span className="text-muted-foreground">{x.hint}</span>
                  </span>
                </label>
              ))}
            </RadioGroup>
          )}
          {step === 2 && (
            <>
              <ol className="list-decimal space-y-1 ps-5">{p.steps.map((s, i) => <li key={i}>{s}</li>)}</ol>
              <div className="flex flex-col gap-2 rounded-md border bg-card p-3">
                <span className="font-medium">Redirect address</span>
                <span className="break-all font-mono text-xs">{redirect}</span>
                <Button size="sm" variant="outline" className="self-start" onClick={() => void navigator.clipboard?.writeText(redirect)}>Copy</Button>
              </div>
            </>
          )}
          {step === 3 && (
            <>
              {kind === "oidc" && (
                <div className="flex flex-col gap-2"><Label htmlFor="sso-name">Name on the button</Label>
                  <Input id="sso-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Company account" /></div>
              )}
              {p.issuer === "ask" && (
                <div className="flex flex-col gap-2"><Label htmlFor="sso-issuer">Issuer address</Label>
                  <Input id="sso-issuer" className="font-mono" value={issuer} onChange={(e) => setIssuer(e.target.value)} placeholder={p.issuerHint} /></div>
              )}
              <div className="flex flex-col gap-2"><Label htmlFor="sso-client">Client ID</Label>
                <Input id="sso-client" className="font-mono" value={clientId} onChange={(e) => setClientId(e.target.value)} autoComplete="off" /></div>
              <div className="flex flex-col gap-2"><Label htmlFor="sso-secret">Client secret</Label>
                <Input id="sso-secret" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="new-password" /></div>
              <p className="text-muted-foreground">Linx checks the provider's address when you save. The secret is never shown again.</p>
            </>
          )}
          {step === 4 && (
            <label className="flex items-center justify-between gap-4">
              <span className="flex flex-col gap-0.5">
                <span className="font-medium">Show "Sign in with {name.trim() || p.label}" on the sign-in page</span>
                <span className="text-muted-foreground">Off: it works only for linking and confirming.</span>
              </span>
              <Switch checked={shown} onCheckedChange={setShown} aria-label="Show on the sign-in page" />
            </label>
          )}
          {step === 5 && created && (
            <div className="flex flex-col gap-3">
              <p className="font-medium">{created.name} is added.</p>
              <p className="text-muted-foreground">Try it: link your own account now. You go to {created.name} and come back to My account.</p>
              <Button className="self-start" onClick={() => void goToCompany("/api/v1/me/sso-links", created.id)}>Link my account with {created.name}</Button>
            </div>
          )}
          {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
        </div>
        <div className="mt-auto flex items-center justify-between gap-2 border-t p-4">
          {step < 5 ? (
            <>
              <Button variant="outline" disabled={step === 1 || busy} onClick={() => setStep(step - 1)}>Back</Button>
              <Button disabled={busy || (step === 3 && (!clientId.trim() || !secret.trim() || (p.issuer === "ask" && !issuer.trim()) || (kind === "oidc" && !name.trim())))}
                aria-busy={busy} onClick={() => (step === 4 ? void create() : setStep(step + 1))}>
                {step === 4 ? "Add" : "Next"}
              </Button>
            </>
          ) : <Button className="ms-auto" onClick={close}>Done</Button>}
        </div>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

// --- The page ---------------------------------------------------------------------

export function SystemSettingsScreen({ me, onSimpleModeChange }: { me: Me; onSimpleModeChange: (v: boolean) => void }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [networks, setNetworks] = useState("");
  const [restricted, setRestricted] = useState(false);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const confirm = useConfirmIdentity(me);
  const canWrite = hasScope(me, "settings:write");
  const canSso = hasScope(me, "sso:write");
  const canSeeSso = hasScope(me, "sso:read");

  const load = useCallback(async () => {
    const [s, p, u] = await Promise.all([
      api.GET("/api/v1/settings"),
      canSeeSso ? api.GET("/api/v1/sso-providers") : Promise.resolve({ data: undefined }),
      api.GET("/api/v1/users", { params: { query: { limit: 200 } } }),
    ]);
    if (s.data) {
      setSettings(s.data);
      setRestricted(s.data.admin_network_restricted);
      setNetworks(s.data.admin_networks.join("\n"));
    }
    if (p.data) setProviders(p.data.items);
    if (u.data) setUsers(u.data.items);
  }, [canSeeSso]);
  useEffect(() => { void load(); }, [load]);

  const say = (w: string) => { setSaved(w); window.setTimeout(() => setSaved(""), 2500); };
  const patch = (body: components["schemas"]["SettingsPatch"], what: string) => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/settings", { body });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setSettings(data);
    if (body.simple_mode !== undefined) onSimpleModeChange(data.simple_mode);
    say(`Saved: ${what}`);
    return { confirm: false };
  });
  const toggleProvider = async (p: Provider, field: "enabled" | "shown") => {
    const { data, error: err } = await api.PATCH("/api/v1/sso-providers/{id}", {
      params: { path: { id: p.id }, header: { "If-Match": p.etag } }, body: { [field]: !p[field] },
    });
    if (needsConfirm(err)) { confirm.ask(); return; }
    if (!data) { setError(problemMessage(err)); return; }
    setProviders((list) => list.map((x) => (x.id === p.id ? data : x)));
  };

  if (!settings) return <div className="p-6" aria-busy="true" />;
  const people = settings.extension_ranges.find((r) => r.kind === "people");
  const unlinked = users.filter((u) => !u.disabled && u.role !== "system_admin" && (u.company_sign_in ?? []).length === 0);
  const reason = "Only an admin can change settings";
  const networksList = networks.split(/[\s,]+/).map((n) => n.trim()).filter(Boolean);
  const networksChanged = restricted !== settings.admin_network_restricted || networksList.join(",") !== settings.admin_networks.join(",");
  const redirect = providers[0]?.redirect_uri ?? `${window.location.origin}/api/v1/sso/callback`;

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <SystemHeader current="/admin/system/settings" systemAdmin={me.role === "system_admin"} />
      <p className="mt-2 min-h-5 text-sm text-status-available" role="status">{saved}</p>
      <div className="flex flex-col gap-6">
        <SystemCard title="Phone system">
          <Row label="Numbers" action={canWrite && <Button size="sm" variant="outline" onClick={() => navigate("/setup")}>Change</Button>}>
            {settings.extension_digits} digits{people ? `, people from ${people.from} to ${people.to}` : ""}
          </Row>
          <Row label="Place">
            <RadioGroup value={settings.site_kind || "business"} onValueChange={(v) => void patch({ site_kind: v as "home" | "business" }, "place")}
              className="flex flex-wrap gap-4" disabled={!canWrite || busy} aria-label="Place">
              <label htmlFor="place-business" className="flex items-center gap-2"><RadioGroupItem id="place-business" value="business" /> Business</label>
              <label htmlFor="place-home" className="flex items-center gap-2"><RadioGroupItem id="place-home" value="home" /> Home</label>
            </RadioGroup>
          </Row>
          <Row label="Country" action={<Button size="sm" variant="outline" onClick={() => navigate("/admin/outgoing")}>Outgoing calls</Button>}>
            {(() => { try { return new Intl.DisplayNames(["en"], { type: "region" }).of(settings.country); } catch { return settings.country; } })()}
          </Row>
          <Row label="Simple mode" action={<Switch checked={settings.simple_mode} disabled={!canWrite || busy} aria-label="Simple mode"
            onCheckedChange={(v) => void patch({ simple_mode: v }, "Simple mode")} />}>
            Hides expert pages and settings.
          </Row>
          <Row label="Setup wizard" action={canWrite && <Button size="sm" variant="outline" onClick={() => navigate("/setup")}>Run it again</Button>}>
            Numbers, people, phone line and calls, step by step.
          </Row>
        </SystemCard>

        <SystemCard title="Admins can sign in from">
          <RadioGroup value={restricted ? "network" : "anywhere"} onValueChange={(v) => setRestricted(v === "network")} disabled={!canWrite}>
            <label htmlFor="admin-anywhere" className="flex cursor-pointer items-start gap-3 text-sm">
              <RadioGroupItem id="admin-anywhere" value="anywhere" className="mt-0.5" />
              <span><span className="font-medium">Anywhere</span> <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>
                <span className="block text-muted-foreground">With their second sign-in step, as everyone else.</span></span>
            </label>
            <label htmlFor="admin-network" className="flex cursor-pointer items-start gap-3 text-sm">
              <RadioGroupItem id="admin-network" value="network" className="mt-0.5" />
              <span><span className="font-medium">Only my home or office network</span>
                <span className="block text-muted-foreground">Elsewhere, admins see only the ordinary pages. Your home network from setup always counts; add others below only if you need them.</span></span>
            </label>
          </RadioGroup>
          {restricted && (
            <div className="mt-3 flex flex-col gap-2">
              <Label htmlFor="admin-networks">Other networks too (optional, one per line, e.g. 203.0.113.0/24)</Label>
              <textarea id="admin-networks" rows={3} value={networks} onChange={(e) => setNetworks(e.target.value)} disabled={!canWrite}
                className="w-full max-w-md rounded-md border bg-background px-3 py-2 font-mono text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50" />
              <p className="text-sm text-status-away">If you aren't on one of these networks now, you lose the admin pages as soon as you save, until you're back.</p>
            </div>
          )}
          <p className="mt-3 text-sm" role="status">
            Right now: admins can sign in {settings.admin_network_restricted ? "only from your home or office network" : "from anywhere"}.
          </p>
          {canWrite && networksChanged && (
            <div className="mt-3 flex flex-col gap-2 rounded-md border border-primary/40 bg-primary/5 p-3 text-sm">
              <p className="font-medium">Not saved yet.</p>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" disabled={busy}
                  onClick={() => void patch({ admin_network_restricted: restricted, admin_networks: networksList }, "where admins sign in from")}>
                  {restricted ? "Change anyway" : "Save"}
                </Button>
                <Button size="sm" variant="outline" disabled={busy}
                  onClick={() => { setRestricted(settings.admin_network_restricted); setNetworks(settings.admin_networks.join("\n")); setError(""); }}>
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </SystemCard>

        {canSeeSso && (
          <SystemCard title="Company sign-in" action={
            <Guarded allowed={canSso} reason={reason}><Button size="sm" disabled={!canSso} onClick={() => setAdding(true)}>+ Add a provider</Button></Guarded>
          }>
            {providers.length === 0 && <p className="text-sm text-muted-foreground">People sign in with Google or Microsoft (or your own server) instead of a password.</p>}
            <ul className="flex flex-col divide-y">
              {providers.map((p) => (
                <li key={p.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3 text-sm">
                  <span className="min-w-40 grow basis-40">
                    <span className="font-medium">{p.name}</span>
                    <span className="block text-muted-foreground">
                      {!p.enabled ? "Turned off" : p.shown ? "On, shown on the sign-in page" : "On, not shown on the sign-in page"}
                    </span>
                  </span>
                  {canSso && (
                    <span className="flex flex-wrap gap-2">
                      <Button size="sm" variant="outline" onClick={() => void toggleProvider(p, "shown")} disabled={!p.enabled}>{p.shown ? "Hide button" : "Show button"}</Button>
                      <Button size="sm" variant="outline" onClick={() => void toggleProvider(p, "enabled")}>{p.enabled ? "Turn off" : "Turn on"}</Button>
                    </span>
                  )}
                </li>
              ))}
            </ul>
            {providers.length > 0 && (
              <div className="mt-3 flex items-start justify-between gap-4 border-t pt-3">
                <span className="text-sm">
                  <span className="font-medium">People must use company sign-in</span>
                  <span className="block text-muted-foreground">System admins can always use a password or passkey.</span>
                  {!settings.company_sign_in_required && unlinked.length > 0 && (
                    <span className="block text-status-away">
                      {unlinked.length} {unlinked.length === 1 ? "person hasn't" : "people haven't"} linked a company account yet and couldn't sign in:{" "}
                      {unlinked.slice(0, 3).map((u) => u.name).join(", ")}{unlinked.length > 3 ? "…" : ""}
                    </span>
                  )}
                </span>
                <Switch checked={!!settings.company_sign_in_required} disabled={!canSso || busy} aria-label="People must use company sign-in"
                  onCheckedChange={(v) => void patch({ company_sign_in_required: v }, "company sign-in")} />
              </div>
            )}
          </SystemCard>
        )}
        {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
        <HelpAnswersCard me={me} />
      </div>
      <AddProvider me={me} open={adding} onOpenChange={setAdding} redirect={redirect} onDone={() => void load()} />
      {confirm.dialog}
    </div>
  );
}
