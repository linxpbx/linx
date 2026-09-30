// The Light / Dark / Match device switch (owner, 2026-09-30) on every
// page: in a page's header, or in the corner of the pages with one card
// in the middle. A small menu of its own, not the dropdown component: that
// brings a positioning library the sign-in page doesn't load otherwise
// (about 24 KB compressed, docs/RESOURCES.md).
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { Monitor, Moon, Sun } from "lucide-react";
import { setThemeChoice, useThemeChoice, type ThemeChoice } from "@/lib/theme";
import { cn } from "@/lib/utils";

const CHOICES: { value: ThemeChoice; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
  { value: "device", label: "Match this device", icon: Monitor },
];

export function ThemeMenu({ className }: { className?: string }) {
  const choice = useThemeChoice();
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const items = useRef<(HTMLButtonElement | null)[]>([]);
  const menuId = useId();
  const Icon = CHOICES.find((c) => c.value === choice)?.icon ?? Monitor;

  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };

  useEffect(() => {
    if (!open) return;
    items.current[Math.max(0, CHOICES.findIndex((c) => c.value === choice))]?.focus();
    const outside = (e: PointerEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
    // Focus only when it opens, not when the choice changes.
  }, [open]);

  const onKey = (e: KeyboardEvent) => {
    const at = items.current.indexOf(document.activeElement as HTMLButtonElement);
    const move = (i: number) => { e.preventDefault(); items.current[(i + CHOICES.length) % CHOICES.length]?.focus(); };
    if (e.key === "Escape") { e.preventDefault(); close(true); }
    else if (e.key === "Tab") setOpen(false);
    else if (e.key === "ArrowDown") move(at + 1);
    else if (e.key === "ArrowUp") move(at - 1);
    else if (e.key === "Home") move(0);
    else if (e.key === "End") move(CHOICES.length - 1);
  };

  return (
    <div ref={box} className={cn("relative shrink-0", className)}>
      <button ref={trigger} type="button" aria-label="Appearance" title="Appearance"
        aria-haspopup="menu" aria-expanded={open} aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => { if (e.key === "ArrowDown" && !open) { e.preventDefault(); setOpen(true); } }}
        className="flex size-10 items-center justify-center rounded-full text-muted-foreground outline-none hover:bg-card hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50">
        <Icon aria-hidden="true" className="size-5" />
      </button>
      {open && (
        <div id={menuId} role="menu" aria-label="Appearance" onKeyDown={onKey}
          className="absolute end-0 top-full z-50 mt-1 w-52 rounded-md border bg-popover p-1 text-popover-foreground shadow-md">
          <p aria-hidden="true" className="px-2 py-1.5 text-sm font-medium">Appearance</p>
          {CHOICES.map((c, i) => (
            <button key={c.value} ref={(el) => { items.current[i] = el; }} type="button" role="menuitemradio"
              aria-checked={c.value === choice} tabIndex={-1}
              onClick={() => { setThemeChoice(c.value); close(true); }}
              className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-start text-sm outline-none hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground">
              <c.icon aria-hidden="true" className="size-4 shrink-0" />
              <span className="flex-1">{c.label}</span>
              {c.value === choice && <span aria-hidden="true" className="size-2 rounded-full bg-current" />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** For a page with one card in the middle and no header. */
export function ThemeCorner() {
  return <ThemeMenu className="fixed end-3 top-3 z-30" />;
}
