// The 12-key pad: dials in the Dialer, sends tones during a call
// (WEB_SCREENS_PHASE1C.md §3, §5).
import { cn } from "@/lib/utils";

const KEYS: [string, string][] = [
  ["1", ""], ["2", "ABC"], ["3", "DEF"],
  ["4", "GHI"], ["5", "JKL"], ["6", "MNO"],
  ["7", "PQRS"], ["8", "TUV"], ["9", "WXYZ"],
  ["*", ""], ["0", "+"], ["#", ""],
];

export function Keypad({ onKey, size = "lg", dark = false }: { onKey: (k: string) => void; size?: "sm" | "lg"; dark?: boolean }) {
  return (
    <div className={cn("grid grid-cols-3 justify-items-center", size === "lg" ? "gap-x-6 gap-y-4" : "gap-2")} role="group" aria-label="Keypad">
      {KEYS.map(([k, letters]) => (
        <button key={k} type="button" onClick={() => onKey(k)} aria-label={k === "*" ? "Star" : k === "#" ? "Hash" : k}
          className={cn(
            "flex flex-col items-center justify-center rounded-full font-mono transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
            size === "lg" ? "size-18 text-2xl" : "size-12 text-lg",
            dark ? "bg-sidebar-foreground/10 text-sidebar-foreground hover:bg-sidebar-foreground/20"
              : "border bg-card hover:bg-background active:bg-border",
          )}>
          <span className="leading-none">{k}</span>
          {size === "lg" && <span className={cn("mt-1 h-3 text-[10px] leading-none tracking-widest font-sans", dark ? "text-sidebar-foreground/70" : "text-muted-foreground")}>{letters}</span>}
        </button>
      ))}
    </div>
  );
}
