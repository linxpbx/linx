// Light, dark or the device's own setting (owner, 2026-09-30): the choice
// is kept in this browser, so it holds on the sign-in page too. The page
// always carries the resolved theme on <html data-theme>, which the tokens
// and Tailwind's `dark:` variant both follow.
import { useSyncExternalStore } from "react";

export type ThemeChoice = "light" | "dark" | "device";
export type Theme = "light" | "dark";

const KEY = "linx-theme";
const DARK = "(prefers-color-scheme: dark)";

let choice: ThemeChoice = "device";
const listeners = new Set<() => void>();

function stored(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "device";
  } catch {
    return "device";
  }
}

function deviceTheme(): Theme {
  return typeof matchMedia === "function" && matchMedia(DARK).matches ? "dark" : "light";
}

function apply() {
  document.documentElement.dataset.theme = choice === "device" ? deviceTheme() : choice;
  listeners.forEach((l) => l());
}

/** Once, before the first render: the saved choice, and the device's changes. */
export function startTheme() {
  choice = stored();
  apply();
  if (typeof matchMedia === "function") {
    matchMedia(DARK).addEventListener("change", () => { if (choice === "device") apply(); });
  }
  // Another tab changed it.
  addEventListener("storage", (e) => { if (e.key === KEY) { choice = stored(); apply(); } });
}

export function setThemeChoice(next: ThemeChoice) {
  choice = next;
  try {
    if (next === "device") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, next);
  } catch {
    // Not saved (private window, storage blocked): it still applies here.
  }
  apply();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => { listeners.delete(l); };
}

export function useThemeChoice(): ThemeChoice {
  return useSyncExternalStore(subscribe, () => choice);
}

/** What the page shows now: light or dark. */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, () => (document.documentElement.dataset.theme === "dark" ? "dark" : "light"));
}
