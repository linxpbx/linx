// The one "where does the call go" picker (docs/ui/SCREENS_PHASE1F.md §0,
// ADR-068): the same drop-down everywhere a call is sent somewhere. A
// choice that would make a loop is greyed with the reason; what isn't
// built yet (voicemail boxes, menus and queues) is greyed so the list's
// shape never changes.
import { useMemo, useState } from "react";
import { ChevronDown, Search } from "lucide-react";
import type { components } from "@/api/schema";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

export type Destination = components["schemas"]["Destination"];
export type RingGroup = components["schemas"]["RingGroup"];
export type ExtensionChoice = { id: string; number: string; display_name: string };

export const NOT_AVAILABLE: Destination = { kind: "message", message: "not-available" };
export const CLOSED: Destination = { kind: "message", message: "closed" };

/** A destination in a few words, as lists show it. */
export function destinationLabel(d: Destination | undefined, extensions: ExtensionChoice[], groups: RingGroup[]): string {
  if (!d) return "—";
  if (d.kind === "extension") {
    const e = extensions.find((x) => x.id === d.extension_id);
    if (e) return `${e.display_name} (${e.number})`;
  }
  if (d.kind === "ring_group") {
    const g = groups.find((x) => x.id === d.ring_group_id);
    if (g) return g.name;
  }
  if (d.kind === "message") return d.message === "closed" ? "Play \"We're closed\" and hang up" : "Nobody (callers hear \"not available\")";
  return d.label ?? "—";
}

/** Why sending calls from group `self` to `to` would loop, or "" if it wouldn't. */
export function loopReason(self: string | undefined, to: RingGroup, groups: RingGroup[]): string {
  if (!self) return "";
  if (to.id === self) return "This is the group itself";
  let at: RingGroup | undefined = to;
  for (let i = 0; at && i <= groups.length; i++) {
    if (at.if_no_answer.kind !== "ring_group") return "";
    if (at.if_no_answer.ring_group_id === self) return `${at.name} already sends its unanswered calls here`;
    const next: string | undefined = at.if_no_answer.ring_group_id;
    at = groups.find((g) => g.id === next);
  }
  return "";
}

function same(a: Destination | undefined, b: Destination): boolean {
  return !!a && a.kind === b.kind && a.extension_id === b.extension_id && a.ring_group_id === b.ring_group_id && a.message === b.message;
}

function Section({ title }: { title: string }) {
  return <p className="px-2 pb-1 pt-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</p>;
}

export function DestinationPicker({ id, value, onChange, extensions, groups, self, onNewGroup, ringOnly, placeholder }: {
  id?: string; value: Destination | undefined; onChange: (d: Destination) => void;
  /** Only people and ring groups (who a number rings). */
  ringOnly?: boolean;
  /** Shown when nothing is chosen. */
  placeholder?: string;
  extensions: ExtensionChoice[]; groups: RingGroup[];
  /** The ring group being edited: choices leading back to it are greyed. */
  self?: string;
  /** "+ New ring group…": opens ring-group quick add on top. */
  onNewGroup?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const people = useMemo(() => extensions
    .filter((e) => !q || e.number.includes(q) || e.display_name.toLowerCase().includes(q))
    .sort((a, b) => a.number.localeCompare(b.number)), [extensions, q]);
  const shownGroups = groups.filter((g) => !q || g.name.toLowerCase().includes(q) || (g.number ?? "").includes(q));

  const pick = (d: Destination) => { onChange(d); setOpen(false); setQuery(""); };
  const option = (key: string, d: Destination, label: string, hint?: string, disabledReason?: string) => (
    <button key={key} type="button" role="option" aria-selected={same(value, d)} aria-disabled={!!disabledReason || undefined}
      disabled={!!disabledReason} onClick={() => pick(d)}
      className={cn(
        "flex w-full flex-col items-start rounded-sm px-2 py-1.5 text-start text-sm outline-none",
        "hover:bg-accent focus-visible:bg-accent disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent",
        same(value, d) && "bg-accent font-medium",
      )}>
      <span>{label}</span>
      {(disabledReason || hint) && <span className="text-xs text-muted-foreground">{disabledReason || hint}</span>}
    </button>
  );
  const later = (label: string) => (
    <p className="px-2 py-1.5 text-sm text-muted-foreground/70" aria-disabled="true">{label}</p>
  );

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button id={id} type="button" variant="outline" role="combobox" aria-expanded={open} aria-haspopup="listbox"
          className="w-full justify-between font-normal">
          <span className="truncate">{value ? destinationLabel(value, extensions, groups) : (placeholder ?? "—")}</span>
          <ChevronDown aria-hidden="true" className="size-4 opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] min-w-72 p-1">
        <div className="relative p-1">
          <Search aria-hidden="true" className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search" className="ps-8" aria-label="Search destinations" autoFocus />
        </div>
        <div role="listbox" aria-label="Where calls go" className="max-h-80 overflow-y-auto">
          <Section title="People" />
          {people.length === 0 && <p className="px-2 py-1.5 text-sm text-muted-foreground">No one matches.</p>}
          {people.map((e) => option(`e-${e.id}`, { kind: "extension", extension_id: e.id }, `${e.number} ${e.display_name}`))}
          <Section title="Ring groups" />
          {shownGroups.map((g) => option(`g-${g.id}`, { kind: "ring_group", ring_group_id: g.id },
            `${g.number ? `${g.number} ` : ""}${g.name}`,
            `${g.members.length} ${g.members.length === 1 ? "person" : "people"}, ${g.strategy === "all" ? "all at once" : "one after another"}`,
            loopReason(self, g, groups)))}
          {onNewGroup && (
            <button type="button" onClick={() => { setOpen(false); onNewGroup(); }}
              className="w-full rounded-sm px-2 py-1.5 text-start text-sm text-link hover:bg-accent">
              + New ring group…
            </button>
          )}
          {!ringOnly && <>
            <Section title="Voicemail" />
            {later("Voicemail boxes: coming soon")}
            <Section title="Other" />
            {option("m-closed", CLOSED, "Play \"We're closed\" and hang up")}
            {option("m-na", NOT_AVAILABLE, "Nobody (callers hear \"not available\")")}
          </>}
          {later("Menus and queues: coming later")}
        </div>
      </PopoverContent>
    </Popover>
  );
}
