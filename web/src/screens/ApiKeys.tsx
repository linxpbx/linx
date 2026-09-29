// API keys and app logins (OAuth clients), an expert page
// (docs/ui/ADMIN_SCREENS_PHASE1E.md §10.4): list, create with an access
// preset and an expiry (shown once), revoke.
import { useCallback, useEffect, useState } from "react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { DataTable } from "@/components/DataTable";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { hasScope } from "@/lib/roles";
import { cn } from "@/lib/utils";

type ApiKey = components["schemas"]["ApiKey"];
type Client = components["schemas"]["OAuthClient"];
type Cred = ApiKey | Client;
type Tab = "keys" | "clients";

// The server's sensitive scopes (internal/auth/scopes.go): never in "all",
// named one by one, and creating a credential with one asks "confirm it's you".
const SENSITIVE = ["api_keys:write", "backups:write", "calls:control", "devices:write", "oauth_clients:write", "outbound_allowlist:write",
  "recordings:read", "routing:write", "sso:write", "transcripts:read", "trunks:write", "users:write"];

type Preset = "read" | "people" | "all" | "custom";
const PRESETS: { value: Preset; title: string; hint: string }[] = [
  { value: "read", title: "Read only", hint: "See everything, change nothing." },
  { value: "people", title: "Manage people and extensions", hint: "Add and change people, extensions and their phones." },
  { value: "all", title: "Everything", hint: "Every everyday permission you have; not the sensitive ones (phone lines, keys, backups…)." },
  { value: "custom", title: "Choose permissions", hint: "Pick exactly which, including sensitive ones." },
];
const EXPIRY = [{ days: 90, label: "90 days", recommended: true }, { days: 365, label: "1 year" }, { days: 729, label: "2 years" }];

function scopesFor(preset: Preset, mine: string[], custom: string[]): string[] {
  if (preset === "all") return ["all"];
  if (preset === "read") return mine.filter((s) => s.endsWith(":read") && !SENSITIVE.includes(s));
  if (preset === "people") return ["users:read", "users:write", "extensions:read", "extensions:write", "devices:read", "devices:write"].filter((s) => mine.includes(s));
  return custom;
}

function whenWords(iso?: string) {
  if (!iso) return "never";
  return new Date(iso).toLocaleDateString([], { day: "numeric", month: "short", year: "numeric" });
}
const scopeSummary = (s: string[]) => (s.includes("all") ? "Everything" : s.every((x) => x.endsWith(":read")) ? `Read only (${s.length})` : `${s.length} permissions`);

function CreateCredential({ me, tab, open, onOpenChange, onDone }: {
  me: Me; tab: Tab; open: boolean; onOpenChange: (o: boolean) => void; onDone: () => void;
}) {
  const [name, setName] = useState("");
  const [preset, setPreset] = useState<Preset>("read");
  const [custom, setCustom] = useState<string[]>([]);
  const [days, setDays] = useState(90);
  const [secret, setSecret] = useState<{ label: string; value: string; id?: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const mine = me.scopes;

  useEffect(() => { if (open) { setName(""); setPreset("read"); setCustom([]); setDays(90); setSecret(null); setError(""); } }, [open]);

  const scopes = scopesFor(preset, mine, custom);
  const create = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const body = { name: name.trim(), scopes, expires_at: new Date(Date.now() + days * 86_400_000).toISOString() };
    const res = tab === "keys"
      ? await api.POST("/api/v1/api-keys", { body })
      : await api.POST("/api/v1/oauth-clients", { body });
    setBusy(false);
    if (needsConfirm(res.error)) return { confirm: true };
    if (!res.data) { setError(problemMessage(res.error)); return { confirm: false }; }
    if ("key" in res.data) setSecret({ label: "The key", value: res.data.key });
    else setSecret({ label: "The client secret", value: res.data.client_secret, id: res.data.oauth_client.client_id });
    return { confirm: false };
  });
  const close = () => { onOpenChange(false); if (secret) onDone(); };
  const sensitive = scopes.filter((s) => SENSITIVE.includes(s));

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader><DialogTitle>{tab === "keys" ? "Create an API key" : "Create an app login"}</DialogTitle></DialogHeader>
        {!secret ? (
          <div className="flex flex-col gap-4 text-sm">
            <div className="flex flex-col gap-2">
              <Label htmlFor="cred-name">What it's for</Label>
              <Input id="cred-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="CRM sync" autoFocus />
            </div>
            <fieldset className="flex flex-col gap-2">
              <legend className="mb-2 font-medium">Access</legend>
              <RadioGroup value={preset} onValueChange={(v) => setPreset(v as Preset)}>
                {PRESETS.map((p) => (
                  <label key={p.value} htmlFor={`preset-${p.value}`} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 has-[[data-state=checked]]:border-primary">
                    <RadioGroupItem id={`preset-${p.value}`} value={p.value} className="mt-0.5" />
                    <span><span className="font-medium">{p.title}</span><span className="block text-muted-foreground">{p.hint}</span></span>
                  </label>
                ))}
              </RadioGroup>
            </fieldset>
            {preset === "custom" && (
              <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2" role="group" aria-label="Permissions">
                {mine.map((s) => (
                  <label key={s} className="flex items-center gap-2 font-mono text-xs">
                    <Checkbox checked={custom.includes(s)} onCheckedChange={(v) => setCustom(v ? [...custom, s] : custom.filter((x) => x !== s))} />
                    {s}{SENSITIVE.includes(s) && <span className="font-sans text-status-away">sensitive</span>}
                  </label>
                ))}
              </div>
            )}
            {sensitive.length > 0 && (
              <p className="text-status-away">Includes sensitive permissions ({sensitive.join(", ")}): you'll be asked to confirm it's you.</p>
            )}
            <fieldset>
              <legend className="mb-2 font-medium">Stops working after</legend>
              <RadioGroup value={String(days)} onValueChange={(v) => setDays(Number(v))} className="flex flex-wrap gap-4">
                {EXPIRY.map((x) => (
                  <label key={x.days} htmlFor={`exp-${x.days}`} className="flex items-center gap-2">
                    <RadioGroupItem id={`exp-${x.days}`} value={String(x.days)} />{x.label}
                    {x.recommended && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>}
                  </label>
                ))}
              </RadioGroup>
            </fieldset>
            {error && <p role="alert" className="font-medium text-destructive">{error}</p>}
            <DialogFooter>
              <Button disabled={busy || !name.trim() || scopes.length === 0} aria-busy={busy} onClick={() => void create()}>Create</Button>
            </DialogFooter>
          </div>
        ) : (
          <div className="flex flex-col gap-3 text-sm">
            {secret.id && <p>Client ID: <span className="break-all font-mono">{secret.id}</span></p>}
            <p className="font-medium">{secret.label}, shown once:</p>
            <p className="break-all rounded-md border bg-card p-3 font-mono text-xs">{secret.value}</p>
            <Button size="sm" variant="outline" className="self-start" onClick={() => void navigator.clipboard?.writeText(secret.value)}>Copy</Button>
            <p className="text-muted-foreground">Store it somewhere safe. Lost it? Revoke this one and create another.</p>
            <DialogFooter><Button onClick={close}>I've saved it</Button></DialogFooter>
          </div>
        )}
        {confirm.dialog}
      </DialogContent>
    </Dialog>
  );
}

export function ApiKeysScreen({ me }: { me: Me }) {
  const [tab, setTab] = useState<Tab>("keys");
  const [keys, setKeys] = useState<ApiKey[] | null>(null);
  const [clients, setClients] = useState<Client[]>([]);
  const [users, setUsers] = useState<components["schemas"]["User"][]>([]);
  const [creating, setCreating] = useState(false);
  const [selected, setSelected] = useState<Cred | null>(null);
  const [revoking, setRevoking] = useState(false);
  const [error, setError] = useState("");
  const canWrite = hasScope(me, tab === "keys" ? "api_keys:write" : "oauth_clients:write");

  const load = useCallback(async () => {
    const [k, c, u] = await Promise.all([
      api.GET("/api/v1/api-keys", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/oauth-clients", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/users", { params: { query: { limit: 200 } } }),
    ]);
    setKeys(k.data?.items ?? []);
    setClients(c.data?.items ?? []);
    if (u.data) setUsers(u.data.items);
  }, []);
  useEffect(() => { void load(); }, [load]);

  const byWhom = (actor: string) => {
    const [t, id] = actor.split(":");
    if (t === "user") return users.find((u) => u.id === id)?.name ?? "A person";
    if (t === "system") return "On the server";
    return actor;
  };
  const revoke = async () => {
    if (!selected) return;
    const res = "client_id" in selected
      ? await api.DELETE("/api/v1/oauth-clients/{id}", { params: { path: { id: selected.id } } })
      : await api.DELETE("/api/v1/api-keys/{id}", { params: { path: { id: selected.id } } });
    setRevoking(false);
    if (!res.response.ok) { setError(problemMessage(res.error)); return; }
    setSelected(null);
    void load();
  };

  const columns: ColumnDef<Cred>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => (
      <span className={cn("font-medium", row.original.revoked_at && "text-muted-foreground line-through")}>{row.original.name}</span>
    ) },
    { id: "access", header: "Access", accessorFn: (c) => scopeSummary(c.scopes) },
    { id: "by", header: "Created by", meta: { wide: true }, accessorFn: (c) => byWhom(c.created_by) },
    { id: "used", header: "Last used", meta: { wide: true }, accessorFn: (c) => c.last_used_at ?? "", cell: ({ row }) => whenWords(row.original.last_used_at) },
    { id: "expires", header: "Stops working", accessorFn: (c) => c.expires_at, cell: ({ row }) => (row.original.revoked_at ? "Revoked" : whenWords(row.original.expires_at)) },
  ];

  if (keys === null) return <div className="p-6" aria-busy="true" />;
  const rows: Cred[] = tab === "keys" ? keys : clients;

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <h1 className="font-display text-3xl font-semibold tracking-tight">API keys</h1>
        {canWrite && <Button onClick={() => setCreating(true)}>+ Create</Button>}
      </div>
      <nav aria-label="API keys" className="mt-4 flex gap-1 border-b">
        {([["keys", "API keys"], ["clients", "App logins (OAuth)"]] as const).map(([v, label]) => (
          <button key={v} type="button" aria-current={tab === v ? "page" : undefined} onClick={() => setTab(v)}
            className={cn("-mb-px border-b-2 px-3 py-2 text-sm", tab === v ? "border-primary font-medium" : "border-transparent")}>{label}</button>
        ))}
      </nav>
      <p className="mt-3 text-sm text-muted-foreground">
        {tab === "keys" ? "For your own software to use the Linx API. Each key can do only what it's given." : "For apps that sign in with a client ID and secret (OAuth client credentials)."}
      </p>
      <div className="mt-4">
        {rows.length === 0
          ? <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">None yet.</p>
          : <DataTable columns={columns} data={rows} onRowClick={setSelected} />}
      </div>
      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}

      <CreateCredential me={me} tab={tab} open={creating} onOpenChange={setCreating} onDone={() => void load()} />
      {selected && (
        <Sheet open onOpenChange={(o) => { if (!o) setSelected(null); }}>
          <SheetContent className="w-full sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{selected.name}</SheetTitle>
              <SheetDescription>{selected.revoked_at ? "Revoked" : `Stops working ${whenWords(selected.expires_at)}`}</SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4 text-sm">
              <dl className="grid grid-cols-[7rem_minmax(0,1fr)] gap-y-1">
                <dt className="text-muted-foreground">{"client_id" in selected ? "Client ID" : "Starts with"}</dt>
                <dd className="break-all font-mono">{"client_id" in selected ? selected.client_id : selected.prefix}</dd>
                <dt className="text-muted-foreground">Created by</dt><dd>{byWhom(selected.created_by)}, {whenWords(selected.created_at)}</dd>
                <dt className="text-muted-foreground">Last used</dt><dd>{whenWords(selected.last_used_at)}{selected.last_used_ip ? ` from ${selected.last_used_ip}` : ""}</dd>
                <dt className="text-muted-foreground">Only from</dt><dd className="font-mono">{selected.allowed_ips.join(", ") || "Anywhere"}</dd>
              </dl>
              <div>
                <p className="font-medium">Permissions</p>
                <p className="mt-1 break-words font-mono text-xs">{selected.scopes.join(" ")}</p>
              </div>
              {canWrite && !selected.revoked_at && <Button variant="outline" className="self-start" onClick={() => setRevoking(true)}>Revoke</Button>}
            </div>
            <Dialog open={revoking} onOpenChange={setRevoking}>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Revoke "{selected.name}"?</DialogTitle>
                  <DialogDescription>Anything using it stops working at once. This can't be undone.</DialogDescription>
                </DialogHeader>
                <DialogFooter>
                  <Button variant="outline" onClick={() => setRevoking(false)}>Cancel</Button>
                  <Button variant="destructive" onClick={() => void revoke()}>Revoke</Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}
