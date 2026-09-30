// System → Settings → Help answers (docs/HELP.md §4): written answers in
// Help, off until an admin picks a provider. Only the question and the
// matching guide sections go to it. The key is sealed and never shown
// again; changes need "confirm it's you".
import { useCallback, useEffect, useState } from "react";
import { api, problemCode, problemMessage, type Me } from "@/api/client";
import type { components } from "@/api/schema";
import { needsConfirm, useConfirmIdentity } from "@/components/ConfirmIdentity";
import { SystemCard } from "@/components/SystemPage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { forgetGuides } from "@/lib/help";
import { hasScope } from "@/lib/roles";

type Setting = components["schemas"]["HelpAnswers"];
type Provider = Setting["provider"];
type Test = components["schemas"]["HelpAnswersTest"];

const PROVIDERS: { id: Provider; label: string; hint: string; url?: string; model: string; key: string }[] = [
  { id: "anthropic", label: "Anthropic (Claude)", hint: "Fast and cheap with Claude Haiku. Needs an API key from console.anthropic.com.",
    model: "claude-haiku-4-5", key: "API key" },
  { id: "ollama", label: "Ollama", hint: "A model on your own computer or network: nothing leaves your network. Its address must be https (put it behind your proxy).",
    url: "https://ollama.example.com", model: "llama3.2", key: "API key (only if your proxy asks for one)" },
  { id: "openai", label: "Another service (OpenAI-compatible)", hint: "Any service that speaks the OpenAI chat API, at its https address.",
    url: "https://api.example.com/v1", model: "the model's name", key: "API key" },
];

export function HelpAnswersCard({ me }: { me: Me }) {
  const [saved, setSaved] = useState<Setting | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [provider, setProvider] = useState<Provider>("anthropic");
  const [url, setUrl] = useState("");
  const [model, setModel] = useState("");
  const [key, setKey] = useState("");
  const [personLimit, setPersonLimit] = useState("");
  const [serverLimit, setServerLimit] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [blockedHost, setBlockedHost] = useState("");
  const [note, setNote] = useState("");
  const [test, setTest] = useState<Test | null>(null);
  const confirm = useConfirmIdentity(me);
  const canWrite = hasScope(me, "settings:write");
  const canAllow = hasScope(me, "outbound_allowlist:write");

  const show = useCallback((s: Setting) => {
    setSaved(s);
    setEnabled(s.enabled);
    setProvider(s.provider);
    setUrl(s.base_url);
    setModel(s.model);
    setKey("");
    setPersonLimit(String(s.person_daily_limit));
    setServerLimit(String(s.server_daily_limit));
  }, []);
  useEffect(() => {
    void api.GET("/api/v1/help-answers").then(({ data }) => { if (data) show(data); });
  }, [show]);

  if (!saved) return null;
  const p = PROVIDERS.find((x) => x.id === provider)!;
  const moved = provider !== saved.provider || (provider !== "anthropic" && url.trim() !== saved.base_url);
  const body: components["schemas"]["HelpAnswersPatch"] = {};
  if (enabled !== saved.enabled) body.enabled = enabled;
  if (provider !== saved.provider) body.provider = provider;
  if (provider !== "anthropic" && url.trim() !== saved.base_url) body.base_url = url.trim();
  if (model.trim() !== saved.model) body.model = model.trim();
  if (key.trim() || (moved && saved.api_key_set)) body.api_key = key.trim();
  if (Number(personLimit) !== saved.person_daily_limit) body.person_daily_limit = Number(personLimit);
  if (Number(serverLimit) !== saved.server_daily_limit) body.server_daily_limit = Number(serverLimit);
  const changed = Object.keys(body).length > 0;

  const say = (w: string) => { setNote(w); window.setTimeout(() => setNote(""), 2500); };
  const doSave = async () => {
    setBusy(true);
    setError("");
    setBlockedHost("");
    const { data, error: err } = await api.PATCH("/api/v1/help-answers", { params: { header: { "If-Match": saved.etag } }, body });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (!data) {
      setError(problemMessage(err));
      if (problemCode(err) === "base_url_blocked") {
        try { setBlockedHost(new URL(url.trim()).hostname); } catch { /* not an address */ }
      }
      return { confirm: false };
    }
    show(data);
    forgetGuides();
    say("Saved: Help answers");
    return { confirm: false };
  };
  const save = () => confirm.run(doSave);
  // Linx never sends to the home network unless an admin allows that
  // address (ADR-028), a confirm-it's-you action of its own.
  const allowAndSave = () => confirm.run(async () => {
    setBusy(true);
    const { error: err } = await api.POST("/api/v1/outbound-allowlist", { body: { value: blockedHost, description: "Help answers" } });
    setBusy(false);
    if (needsConfirm(err)) return { confirm: true };
    if (err && problemCode(err) !== "allowlist_duplicate") { setError(problemMessage(err)); return { confirm: false }; }
    return doSave();
  });
  const runTest = async () => {
    setBusy(true);
    setTest(null);
    setError("");
    const { data, error: err } = await api.POST("/api/v1/help-answers/test", { body: {} });
    setBusy(false);
    if (!data) { setError(problemMessage(err)); return; }
    setTest(data);
    const again = await api.GET("/api/v1/help-answers");
    if (again.data) setSaved(again.data);
  };

  return (
    <SystemCard title="Help answers" action={
      <Switch checked={enabled} disabled={!canWrite || busy} aria-label="Written answers in Help" onCheckedChange={setEnabled} />
    }>
      <p className="text-sm text-muted-foreground">
        Help can write answers to people's questions from Linx's guides. Only the question and the matching parts of the guides
        are sent, to the provider you pick; nothing about this server, its people or its calls.
      </p>
      <p className="mt-2 text-sm" role="status">
        Right now: {saved.enabled ? `on, written by ${PROVIDERS.find((x) => x.id === saved.provider)?.label}` : "off: Help shows search results only"}.
        {" "}{saved.used_today.toLocaleString("en")} of {saved.server_daily_limit.toLocaleString("en")} questions used today.
      </p>
      <fieldset disabled={!canWrite} className="mt-4 flex flex-col gap-4">
        <RadioGroup value={provider} onValueChange={(v) => {
          const next = v as Provider;
          setProvider(next);
          setModel(next === saved.provider ? saved.model : next === "anthropic" ? "claude-haiku-4-5" : "");
          setUrl(next === saved.provider ? saved.base_url : "");
        }} aria-label="Provider">
          {PROVIDERS.map((x) => (
            <label key={x.id} htmlFor={`answers-${x.id}`} className="flex cursor-pointer items-start gap-3 rounded-md border p-3 text-sm has-[[data-state=checked]]:border-primary">
              <RadioGroupItem id={`answers-${x.id}`} value={x.id} className="mt-0.5" />
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="flex flex-wrap items-center gap-2 font-medium">
                  {x.label}{x.id === "anthropic" && <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>}
                </span>
                <span className="text-muted-foreground">{x.hint}</span>
              </span>
            </label>
          ))}
        </RadioGroup>
        <div className="grid gap-4 sm:grid-cols-2">
          {p.url && (
            <div className="flex flex-col gap-2 sm:col-span-2"><Label htmlFor="answers-url">Address</Label>
              <Input id="answers-url" className="font-mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={p.url} autoComplete="off" /></div>
          )}
          <div className="flex flex-col gap-2"><Label htmlFor="answers-model">Model</Label>
            <Input id="answers-model" className="font-mono" value={model} onChange={(e) => setModel(e.target.value)} placeholder={p.model} autoComplete="off" /></div>
          <div className="flex flex-col gap-2"><Label htmlFor="answers-key">{p.key}</Label>
            <Input id="answers-key" type="password" value={key} onChange={(e) => setKey(e.target.value)} autoComplete="new-password"
              placeholder={saved.api_key_set && !moved ? "Saved: leave empty to keep it" : ""} /></div>
          <div className="flex flex-col gap-2"><Label htmlFor="answers-person">Questions a day, each person</Label>
            <Input id="answers-person" type="number" min={1} max={10000} value={personLimit} onChange={(e) => setPersonLimit(e.target.value)} /></div>
          <div className="flex flex-col gap-2"><Label htmlFor="answers-server">Questions a day, whole server</Label>
            <Input id="answers-server" type="number" min={1} max={100000} value={serverLimit} onChange={(e) => setServerLimit(e.target.value)} /></div>
        </div>
        {moved && saved.api_key_set && !key.trim() && (
          <p className="text-sm text-status-away">The saved key only goes where it was given for: paste it again{provider === "ollama" ? " if this one needs it" : ""}.</p>
        )}
      </fieldset>
      {canWrite && (
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button size="sm" disabled={!changed || busy} aria-busy={busy} onClick={() => void save()}>Save</Button>
          <Button size="sm" variant="outline" disabled={changed || busy} onClick={() => void runTest()}>Test</Button>
          {changed && <span className="text-sm text-muted-foreground">Save first, then Test.</span>}
          <span className="text-sm text-status-available" role="status">{note}</span>
        </div>
      )}
      {error && (
        <div className="mt-3 flex flex-col gap-2">
          <p role="alert" className="text-sm font-medium text-destructive">{error}</p>
          {blockedHost && canAllow && (
            <Button size="sm" variant="outline" className="self-start" disabled={busy} onClick={() => void allowAndSave()}>Allow {blockedHost} and save</Button>
          )}
        </div>
      )}
      {test && (
        <div className="mt-4 rounded-md border bg-background p-3 text-sm" aria-live="polite">
          <p className="font-medium [overflow-wrap:anywhere]">“{test.question}”</p>
          {test.ok
            ? <p className="mt-2 whitespace-pre-wrap [overflow-wrap:anywhere]">{test.answer}</p>
            : <p className="mt-2 text-destructive [overflow-wrap:anywhere]">{test.error}</p>}
          {test.ok && test.guides.length > 0 && <p className="mt-2 text-muted-foreground">From: {test.guides.map((g) => g.title).join(", ")}</p>}
        </div>
      )}
      {confirm.dialog}
    </SystemCard>
  );
}
