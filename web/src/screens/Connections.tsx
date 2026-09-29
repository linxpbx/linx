// Connections (WireGuard), an expert page (docs/ui/ADMIN_SCREENS_PHASE1E.md
// §6.5): private connections a phone company's line can go through.
import { useCallback, useEffect, useState } from "react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { DataTable } from "@/components/DataTable";
import { Dot } from "@/components/presence";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { ColumnDef } from "@tanstack/react-table";
import { tunnelDot, tunnelWords } from "@/lib/lines";
import { isReadOnlyAdmin } from "@/lib/roles";

type Profile = components["schemas"]["WireguardProfile"];
type Trunk = components["schemas"]["Trunk"];

function handshakeWords(iso?: string): string {
  if (!iso) return "never";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return new Date(iso).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
}

function AddConnection({ open, onOpenChange, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [config, setConfig] = useState("");
  const [created, setCreated] = useState<Profile | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { if (open) { setName(""); setConfig(""); setCreated(null); setError(""); } }, [open]);

  const readFile = async (f: File | undefined) => {
    if (!f) return;
    if (f.size > 16_384) { setError("That file is too big to be a WireGuard configuration."); return; }
    setConfig(await f.text());
    if (!name) setName(f.name.replace(/\.conf$/i, ""));
  };
  const submit = async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/wireguard-profiles", { body: { name: name.trim(), config } });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setCreated(data);
  };
  const close = () => { onOpenChange(false); if (created) onDone(); };

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close(); else onOpenChange(o); }}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Add a connection</DialogTitle>
          <DialogDescription>The WireGuard configuration file your phone company gave you.</DialogDescription>
        </DialogHeader>
        {!created ? (
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="wg-name">Name</Label>
              <Input id="wg-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Provider VPN" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="wg-file">Configuration file</Label>
              <Input id="wg-file" type="file" accept=".conf,text/plain" onChange={(e) => void readFile(e.target.files?.[0])} />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="wg-config">Or paste it</Label>
              <textarea id="wg-config" rows={8} value={config} onChange={(e) => setConfig(e.target.value)} spellCheck={false}
                placeholder={"[Interface]\nPrivateKey = …\nAddress = 10.6.0.2/32\n\n[Peer]\nPublicKey = …\nEndpoint = vpn.example.com:51820"}
                className="w-full min-w-0 rounded-md border bg-background px-3 py-2 font-mono text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50" />
              <p className="text-sm text-muted-foreground">Its private key stays sealed on this server and is never shown again.</p>
            </div>
            {error && <p role="alert" className="text-sm font-medium text-destructive">{error}</p>}
            <DialogFooter><Button disabled={busy || !name.trim() || !config.trim()} aria-busy={busy} onClick={() => void submit()}>Add</Button></DialogFooter>
          </div>
        ) : (
          <div className="flex flex-col gap-3 text-sm">
            <p className="font-medium">"{created.name}" is added.</p>
            <p>Linx's public key, if your company asks for it:</p>
            <p className="break-all rounded-md bg-card p-2 font-mono text-xs">{created.public_key}</p>
            {(created.notes ?? []).map((n, i) => <p key={i} className="text-muted-foreground">{n}</p>)}
            <p className="text-muted-foreground">Choose it as a line's connection with <span className="font-mono">sudo linx trunk add --wireguard</span>.</p>
            <DialogFooter><Button onClick={close}>Done</Button></DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function ConnectionsScreen({ me }: { me: Me }) {
  const [profiles, setProfiles] = useState<Profile[] | null>(null);
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<Profile | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [error, setError] = useState("");
  const readOnly = isReadOnlyAdmin(me);

  const load = useCallback(async () => {
    const [p, t] = await Promise.all([
      api.GET("/api/v1/wireguard-profiles", { params: { query: { limit: 200 } } }),
      api.GET("/api/v1/trunks", { params: { query: { limit: 200 } } }),
    ]);
    if (p.data) setProfiles(p.data.items);
    if (t.data) setTrunks(t.data.items);
  }, []);
  useEffect(() => {
    void load();
    const t = window.setInterval(() => void load(), 15_000);
    return () => window.clearInterval(t);
  }, [load]);

  const linesOf = (id: string) => trunks.filter((t) => t.wireguard_profile_id === id);
  const remove = async () => {
    if (!selected) return;
    const { response, error: err } = await api.DELETE("/api/v1/wireguard-profiles/{id}", { params: { path: { id: selected.id } } });
    setConfirmRemove(false);
    if (!response.ok) { setError(problemMessage(err)); return; }
    setSelected(null);
    void load();
  };

  const columns: ColumnDef<Profile>[] = [
    { accessorKey: "name", header: "Name", cell: ({ row }) => <span className="font-medium">{row.original.name}</span> },
    { id: "status", header: "Status", accessorFn: (p) => p.status, cell: ({ row }) => (
      <span className="flex items-center gap-2"><Dot tone={tunnelDot[row.original.status] ?? "neutral"} />{tunnelWords[row.original.status] ?? row.original.status}</span>
    ) },
    { id: "handshake", header: "Last heard from", meta: { wide: true }, accessorFn: (p) => p.last_handshake_at ?? "",
      cell: ({ row }) => handshakeWords(row.original.last_handshake_at) },
    { id: "lines", header: "Lines", accessorFn: (p) => linesOf(p.id).map((t) => t.name).join(", "),
      cell: ({ row }) => linesOf(row.original.id).map((t) => t.name).join(", ") || <span className="text-muted-foreground">—</span> },
  ];

  if (profiles === null) return <div className="p-6" aria-busy="true" />;
  const used = selected ? linesOf(selected.id) : [];

  return (
    <div className="w-full max-w-5xl px-4 py-6 md:px-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-display text-3xl font-semibold tracking-tight">Connections</h1>
          <p className="mt-1 text-sm text-muted-foreground">Private (WireGuard) connections to phone companies that offer one.</p>
        </div>
        {!readOnly && <Button onClick={() => setAdding(true)}>+ Add</Button>}
      </div>
      <div className="mt-4">
        {profiles.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed p-10 text-center">
            <p className="max-w-prose text-sm text-muted-foreground">
              Some phone companies connect over a private WireGuard connection instead of the internet. Most don't: you only need this if yours gave you a WireGuard file.
            </p>
            {!readOnly && <Button variant="outline" onClick={() => setAdding(true)}>Add a connection</Button>}
          </div>
        ) : (
          <DataTable columns={columns} data={profiles} onRowClick={setSelected} />
        )}
      </div>
      {error && <p role="alert" className="mt-4 text-sm font-medium text-destructive">{error}</p>}

      <AddConnection open={adding} onOpenChange={setAdding} onDone={() => void load()} />
      {selected && (
        <Sheet open onOpenChange={(o) => { if (!o) setSelected(null); }}>
          <SheetContent className="w-full sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{selected.name}</SheetTitle>
              <SheetDescription>{tunnelWords[selected.status] ?? selected.status} · last heard from {handshakeWords(selected.last_handshake_at)}</SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-6 overflow-y-auto px-4 pb-4 text-sm">
              {selected.status_detail && <p className="text-muted-foreground">{selected.status_detail}</p>}
              <dl className="grid grid-cols-[8rem_minmax(0,1fr)] gap-y-1">
                <dt className="text-muted-foreground">Its address</dt><dd className="break-all font-mono">{selected.peer_endpoint_host}:{selected.peer_endpoint_port}</dd>
                <dt className="text-muted-foreground">Linx's address in it</dt><dd className="break-all font-mono">{selected.address}</dd>
                <dt className="text-muted-foreground">Linx's public key</dt><dd className="break-all font-mono text-xs">{selected.public_key}</dd>
                <dt className="text-muted-foreground">Lines using it</dt><dd>{used.map((t) => t.name).join(", ") || "None"}</dd>
              </dl>
              {!readOnly && (
                <section className="rounded-md border border-destructive/30 p-3">
                  <h3 className="font-semibold text-destructive">Danger zone</h3>
                  <Button size="sm" variant="outline" className="mt-2" disabled={used.length > 0} onClick={() => setConfirmRemove(true)}>Remove</Button>
                  {used.length > 0 && <p className="mt-2 text-muted-foreground">Move {used.map((t) => `"${t.name}"`).join(", ")} off it first.</p>}
                </section>
              )}
            </div>
            <Dialog open={confirmRemove} onOpenChange={setConfirmRemove}>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Remove "{selected.name}"?</DialogTitle>
                  <DialogDescription>Its keys are deleted. To use it again, add the file again.</DialogDescription>
                </DialogHeader>
                <DialogFooter>
                  <Button variant="outline" onClick={() => setConfirmRemove(false)}>Cancel</Button>
                  <Button variant="destructive" onClick={() => void remove()}>Remove</Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}
