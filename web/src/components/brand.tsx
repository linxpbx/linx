import { cn } from "@/lib/utils";

// The X mark (docs/ui/linx-tokens.json "logo"): two strokes, drawn in the
// current text colour or the accent. A vector source from the owner will
// replace this traced placeholder before release (DESIGN_TOKENS.md).
//
// The viewBox is cropped to the ink (the paths plus half the stroke on every
// side), so the mark's box is exactly what's drawn: in the wordmark it can sit
// on the baseline at the n's height with no gap after it.
const markViewBox = "19.5 5.5 61 89";

export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox={markViewBox} aria-hidden="true" className={className} fill="none" stroke="currentColor"
      strokeWidth={17} strokeLinecap="round">
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
      {/* 1ex = the font's x-height: the mark stands on the baseline as tall as the n. */}
      <LogoMark className={cn("ml-[0.04em] h-[1ex] w-[calc(1ex*61/89)]", onDark ? "text-link-on-dark" : "text-link")} />
    </span>
  );
}
