// "Two ways to add anything" (docs/ui/ADMIN_SCREENS_PHASE1E.md §0): every
// list's Add button opens a small chooser between a guided wizard and a
// quick-add dialog, with "Always use quick add" remembered per browser.
import { useState } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";

function readPreference(kind: string): boolean {
  try {
    return localStorage.getItem(`linx.quickadd.${kind}`) === "1";
  } catch {
    return false;
  }
}
function writePreference(kind: string, v: boolean) {
  try {
    if (v) localStorage.setItem(`linx.quickadd.${kind}`, "1"); else localStorage.removeItem(`linx.quickadd.${kind}`);
  } catch {
    // Best effort only; the chooser still works, it just won't remember.
  }
}

/** Whether "Always use quick add" is on for this kind of thing, remembered per browser. */
export function useAlwaysQuickAdd(kind: string): [boolean, (v: boolean) => void] {
  const [always, setAlways] = useState(() => readPreference(kind));
  return [always, (v: boolean) => { setAlways(v); writePreference(kind, v); }];
}

export function AddChooserDialog({ open, onOpenChange, title, guideHint, quickHint, always, onAlwaysChange, onGuide, onQuick }: {
  open: boolean; onOpenChange: (open: boolean) => void; title: string;
  guideHint: string; quickHint: string; always: boolean; onAlwaysChange: (v: boolean) => void;
  onGuide: () => void; onQuick: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <button type="button" onClick={() => { onOpenChange(false); onGuide(); }}
            className="flex flex-col items-start gap-1 rounded-lg border p-4 text-start hover:border-primary hover:bg-card">
            <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
              <span className="whitespace-nowrap">Guide me</span>
              <span className="rounded-sm bg-primary px-1.5 py-0.5 text-xs text-primary-foreground">Recommended</span>
            </span>
            <span className="text-sm text-muted-foreground">{guideHint}</span>
          </button>
          <button type="button" onClick={() => { onOpenChange(false); onQuick(); }}
            className="flex flex-col items-start gap-1 rounded-lg border p-4 text-start hover:border-primary hover:bg-card">
            <span className="text-sm font-medium">Quick add</span>
            <span className="text-sm text-muted-foreground">{quickHint}</span>
          </button>
        </div>
        <label className="flex items-center gap-3 text-sm">
          <Checkbox checked={always} onCheckedChange={(v) => onAlwaysChange(v === true)} />
          Always use quick add
        </label>
      </DialogContent>
    </Dialog>
  );
}
