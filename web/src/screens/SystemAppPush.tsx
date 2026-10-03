// System → Settings → Calls to the app (ADR-074, docs/PHASE2.md §5): the
// Apple key that lets this server wake a sleeping iPhone or iPad when a call
// comes for it. The key is sealed and never shown again. A system admin
// changes it, after "confirm it's you"; other admins see it read-only.
import { useCallback, useEffect, useState } from "react";
import { api, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { Guarded, SystemCard } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { hasScope } from "@/lib/roles";

type AppPush = components["schemas"]["AppPush"];

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 py-1.5 text-sm">
      <span className="w-28 shrink-0 font-medium">{label}</span>
      <span className="min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

/** Seconds, in the words the rest of Linx uses. */
function waitWords(ms: number) {
  if (ms === 0) return "Calls don't wait";
  return `${(ms / 1000).toFixed(ms % 1000 === 0 ? 0 : 1)} seconds`;
}

function SetUpAppPush({ me, open, onOpenChange, saved, onSaved }: {
  me: Me; open: boolean; onOpenChange: (open: boolean) => void; saved: AppPush; onSaved: (p: AppPush) => void;
}) {
  const [team, setTeam] = useState(saved.team_id);
  const [keyId, setKeyId] = useState(saved.key_id);
  const [bundle, setBundle] = useState(saved.bundle_id || "com.linxpbx.app");
  const [environment, setEnvironment] = useState<AppPush["environment"]>(saved.environment);
  const [wait, setWait] = useState(String(saved.wait_ms));
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);

  useEffect(() => {
    if (!open) return;
    setTeam(saved.team_id);
    setKeyId(saved.key_id);
    setBundle(saved.bundle_id || "com.linxpbx.app");
    setEnvironment(saved.environment);
    setWait(String(saved.wait_ms));
    setKey("");
    setError("");
  }, [open, saved]);

  const save = () => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/app-push", {
      params: { header: { "If-Match": saved.etag } },
      body: {
        enabled: true, team_id: team.trim().toUpperCase(), key_id: keyId.trim().toUpperCase(),
        bundle_id: bundle.trim(), environment, wait_ms: Number(wait) || 0,
        ...(key.trim() ? { key: key.trim() } : {}),
      },
    });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    onSaved(data);
    onOpenChange(false);
    return { confirm: false };
  });

  const ready = team.trim().length === 10 && keyId.trim().length === 10 && bundle.trim() !== "" &&
    (key.trim() !== "" || saved.key_set);
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Calls to the app</SheetTitle>
          <SheetDescription>The key Apple gives you, so a sleeping phone rings.</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4 text-sm">
          <p className="text-muted-foreground">
            In your Apple developer account: Keys → add a key with <span className="font-medium">Apple Push Notifications service</span>,
            download the .p8 file (Apple lets you download it once), and copy the team id and the key id.
          </p>
          <div className="flex flex-col gap-2">
            <Label htmlFor="push-team">Team id</Label>
            <Input id="push-team" autoComplete="off" className="font-mono" maxLength={10} value={team}
              onChange={(e) => setTeam(e.target.value)} placeholder="ABCDE12345" />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="push-key-id">Key id</Label>
            <Input id="push-key-id" autoComplete="off" className="font-mono" maxLength={10} value={keyId}
              onChange={(e) => setKeyId(e.target.value)} placeholder="ABC1234DEF" />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="push-key">{saved.key_set ? "The .p8 key (leave empty to keep the one saved)" : "The .p8 key"}</Label>
            <textarea id="push-key" rows={5} value={key} spellCheck={false}
              onChange={(e) => setKey(e.target.value)}
              placeholder={"-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----"}
              className="w-full rounded-md border bg-background px-3 py-2 font-mono text-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50" />
            <span className="text-muted-foreground">Open the .p8 file in a text editor and paste all of it. It is sealed here and never shown again.</span>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="push-bundle">App id</Label>
            <Input id="push-bundle" autoComplete="off" className="font-mono" value={bundle}
              onChange={(e) => setBundle(e.target.value)} />
          </div>
          <div className="flex flex-col gap-2">
            <span className="font-medium">Which Apple service</span>
            <RadioGroup value={environment} onValueChange={(v) => setEnvironment(v as AppPush["environment"])} aria-label="Which Apple service">
              <label htmlFor="push-production" className="flex items-center gap-2">
                <RadioGroupItem id="push-production" value="production" /> The App Store and TestFlight
              </label>
              <label htmlFor="push-sandbox" className="flex items-center gap-2">
                <RadioGroupItem id="push-sandbox" value="sandbox" /> A build from Xcode (sandbox)
              </label>
            </RadioGroup>
            <span className="text-muted-foreground">Each phone says which one its app was built for, so both can work at once.</span>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="push-wait">How long a call waits for a sleeping phone</Label>
            <Input id="push-wait" inputMode="numeric" className="w-32" value={wait} onChange={(e) => setWait(e.target.value)} />
            <span className="text-muted-foreground">In milliseconds, up to 15000. The caller hears ringing meanwhile; 0 means a sleeping phone doesn't get the call.</span>
          </div>
          {error && <p role="alert" className="text-sm font-medium text-destructive break-words">{error}</p>}
          <Button disabled={!ready || busy} onClick={save}>Save</Button>
        </div>
        {confirm.dialog}
      </SheetContent>
    </Sheet>
  );
}

export function AppPushCard({ me }: { me: Me }) {
  const [push, setPush] = useState<AppPush | null>(null);
  const [setup, setSetup] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirm = useConfirmIdentity(me);
  const canChange = me.role === "system_admin" && hasScope(me, "settings:write");
  const reason = "Only a system admin can change how Linx rings the app: it holds an Apple key";

  const load = useCallback(async () => {
    const { data } = await api.GET("/api/v1/app-push");
    if (data) setPush(data);
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (!push) return null;
  const turn = (on: boolean) => confirm.run(async () => {
    setBusy(true);
    setError("");
    const { data, error: err } = await api.PATCH("/api/v1/app-push", {
      params: { header: { "If-Match": push.etag } }, body: { enabled: on },
    });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) { setError(problemMessage(err)); return { confirm: false }; }
    setPush(data);
    return { confirm: false };
  });
  const setUp = push.key_set && push.team_id !== "";
  const s = push.status;

  return (
    <SystemCard title="Calls to the app" action={!setUp && (
      <Guarded allowed={canChange} reason={reason}>
        <Button size="sm" disabled={!canChange} onClick={() => setSetup(true)}>Set it up</Button>
      </Guarded>
    )}>
      <p className="text-sm text-muted-foreground">
        So a call rings someone's iPhone or iPad when the app isn't open. Linx asks Apple to wake the app and holds
        the call ringing while it arrives. Apple is told the call's number and nothing else.
      </p>
      {!setUp && <p className="mt-2 text-sm" role="status">Not set up: the app only rings while it is open.</p>}
      {setUp && (
        <div className="mt-3 flex flex-col">
          <Row label="Status">{push.enabled ? "On" : "Off: the app only rings while it is open"}</Row>
          <Row label="Apple key">{push.key_id} · team {push.team_id}</Row>
          <Row label="For">{push.environment === "production" ? "The App Store and TestFlight" : "A build from Xcode (sandbox)"}</Row>
          <Row label="Calls wait">{waitWords(push.wait_ms)}</Row>
          <Row label="Sent">
            {s.sent} pushes{s.failed > 0 ? `, ${s.failed} didn't go out` : ""}{s.dead_tokens > 0 ? `, ${s.dead_tokens} phones asked to send a new token` : ""}
          </Row>
          <Row label="Woken">
            {s.woken === 0 ? "No phone has been woken yet" : (
              `${s.woken} phones, ${s.average_wake_seconds?.toFixed(1) ?? "?"}s on average to ring (slowest ${s.slowest_wake_seconds.toFixed(1)}s)`
            )}
          </Row>
          {error && <p role="alert" className="mt-2 text-sm font-medium text-destructive break-words">{error}</p>}
          <div className="mt-3 flex flex-wrap gap-2">
            <Guarded allowed={canChange} reason={reason}>
              <Button size="sm" variant="outline" disabled={!canChange || busy} onClick={() => setSetup(true)}>Change</Button>
            </Guarded>
            <Guarded allowed={canChange} reason={reason}>
              <Button size="sm" variant="outline" disabled={!canChange || busy} onClick={() => void turn(!push.enabled)}>
                {push.enabled ? "Turn off" : "Turn on"}
              </Button>
            </Guarded>
          </div>
        </div>
      )}
      <SetUpAppPush me={me} open={setup} onOpenChange={(o) => { setSetup(o); if (!o) void load(); }} saved={push} onSaved={setPush} />
      {confirm.dialog}
    </SystemCard>
  );
}
