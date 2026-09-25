import { cn } from "@/lib/utils";

// The X mark (docs/ui/linx-tokens.json "logo"): two strokes, drawn in the
// current text colour or the accent. A vector source from the owner will
// replace this traced placeholder before release (DESIGN_TOKENS.md).
export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 100 100" aria-hidden="true" className={className} fill="none" stroke="currentColor"
      strokeWidth={12} strokeLinecap="round">
      <path d="M28 14 Q72 50 28 86" />
      <path d="M72 14 Q28 50 72 86" />
    </svg>
  );
}

/** "lin" + the mark as the x, with the name spoken as "Linx". */
export function Wordmark({ className, onDark = false }: { className?: string; onDark?: boolean }) {
  return (
    <span className={cn("inline-flex items-baseline font-display font-bold tracking-tight", className)} role="img" aria-label="Linx">
      <span aria-hidden="true">lin</span>
      <LogoMark className={cn("-ml-[0.02em] h-[0.62em] w-[0.62em] translate-y-[0.02em] self-center", onDark ? "text-link-on-dark" : "text-link")} />
    </span>
  );
}
