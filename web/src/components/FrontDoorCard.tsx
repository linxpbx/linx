// The front-door card (docs/ui/SCREENS_PHASE1F.md §1.2, docs/SIMPLER.md §2.2):
// the three things every front door that passes Linx through does, and
// "How to do this in…" for each product. The same card on the install
// page's certificate step and in System → Server settings; the facts and
// the guides come from setup on the server (installer.DoorCard), so what's
// shown is what setup wrote to /etc/linx/front-door.
import { useState, type ReactNode } from "react";
import { Info } from "lucide-react";

import { CopyBlock, CopyButton } from "@/components/InstallFrame";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { DoorCard } from "@/lib/install";

export function FrontDoorCard({ card, router, children }: {
  card: DoorCard;
  /** What's left for the router, one step per line. */
  router?: string[];
  /** "I've done these steps", as each page has it. */
  children?: ReactNode;
}) {
  const [guide, setGuide] = useState(card.pick || card.guides[0]?.id || "");
  const [web, turn] = card.routes;
  return (
    <section aria-labelledby="door-card" className="flex min-w-0 flex-col gap-4 rounded-md border p-4 text-sm">
      <h3 id="door-card" className="font-display text-base font-semibold">Your front door needs to do three things</h3>
      <ol className="flex flex-col gap-4">
        <Fact n={1} title="Pass these names through without unlocking them">
          <ul className="flex flex-col gap-1">
            {card.routes.map((r) => (
              <li key={r.name} className="flex min-w-0 items-center gap-1">
                <code className="min-w-0 font-mono break-all">{r.name}</code>
                <CopyButton text={r.name} label={r.name} />
              </li>
            ))}
          </ul>
          <p className="text-muted-foreground">Linx's own certificate has to reach the browser.</p>
        </Fact>
        <Fact n={2} title="Send them to this server">
          <ul className="flex flex-col gap-1">
            {card.routes.map((r) => (
              <li key={r.name} className="flex min-w-0 flex-wrap items-center gap-x-2">
                <code className="min-w-0 font-mono break-all">{r.name}</code>
                <span aria-hidden="true">→</span>
                <span className="sr-only">goes to</span>
                <span className="flex items-center gap-1">
                  <code className="font-mono">{r.address}</code>
                  <CopyButton text={r.address} label={`address for ${r.name}`} />
                </span>
              </li>
            ))}
          </ul>
        </Fact>
        <Fact n={3} title="Tell Linx who's visiting">
          <ul className="flex flex-col gap-1">
            {web && <li><code className="font-mono break-all">{web.name}</code>: turn on “PROXY protocol, version 2”</li>}
            {turn && <li><code className="font-mono break-all">{turn.name}</code>: leave it off</li>}
          </ul>
          <p className="text-muted-foreground">Linx only accepts it from {card.proxy}, your front door.</p>
        </Fact>
      </ol>

      <div className="flex min-w-0 flex-col gap-2">
        <h4 className="font-medium" id="door-guides">How to do this in</h4>
        <Tabs value={guide} onValueChange={setGuide} className="min-w-0">
          <TabsList aria-labelledby="door-guides" className="hidden sm:inline-flex">
            {card.guides.map((g) => <TabsTrigger key={g.id} value={g.id}>{g.title}</TabsTrigger>)}
          </TabsList>
          <Select value={guide} onValueChange={setGuide}>
            <SelectTrigger aria-label="How to do this in" className="w-full sm:hidden"><SelectValue /></SelectTrigger>
            <SelectContent>
              {card.guides.map((g) => <SelectItem key={g.id} value={g.id}>{g.title}</SelectItem>)}
            </SelectContent>
          </Select>
          {card.guides.map((g) => (
            <TabsContent key={g.id} value={g.id} className="flex min-w-0 flex-col gap-3 rounded-md border p-3">
              {g.note && (
                <p className="flex items-start gap-2">
                  <Info aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 break-words">{g.note}</span>
                </p>
              )}
              <ol className="list-decimal space-y-1 ps-5">
                {g.steps.map((s) => <li key={s} className="break-words">{s}</li>)}
              </ol>
              {g.files?.map((f) => (
                <div key={f.title} className="flex min-w-0 flex-col gap-1">
                  {f.path && <p className="text-muted-foreground">The block: goes in {f.path}.</p>}
                  <CopyBlock text={f.text} label={f.title} />
                </div>
              ))}
            </TabsContent>
          ))}
        </Tabs>
      </div>

      {router && router.length > 0 && (
        <div className="flex flex-col gap-1">
          <h4 className="font-medium">Then, whichever it is</h4>
          <ul className="list-disc space-y-1 ps-5">
            {router.map((s) => <li key={s} className="break-words">{s}</li>)}
          </ul>
        </div>
      )}
      {children}
    </section>
  );
}

function Fact({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="flex min-w-0 gap-3">
      <span aria-hidden="true" className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted font-medium">{n}</span>
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-medium">{title}</p>
        {children}
      </div>
    </li>
  );
}
