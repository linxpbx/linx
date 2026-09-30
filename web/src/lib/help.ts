// Help (docs/HELP.md): the guides the server lets this person read, and
// which one is about the screen they're on (the ? button).
import { api } from "@/api/client";
import type { components } from "@/api/schema";

export type HelpGuideSummary = components["schemas"]["HelpGuideSummary"];
export type HelpGuide = components["schemas"]["HelpGuide"];
export type HelpBlock = components["schemas"]["HelpBlock"];
export type HelpInline = components["schemas"]["HelpInline"];
export type HelpSearchResult = components["schemas"]["HelpSearchResult"];

export const HELP_PATH = "/help";
const GUIDE = /^\/help\/([a-z0-9-]{1,64})$/;

/** The guide an address shows, if it's a guide's page. */
export function guideInPath(path: string): string | undefined {
  return GUIDE.exec(path)?.[1];
}

export function guidePath(name: string, anchor?: string): string {
  return `${HELP_PATH}/${name}${anchor ? `#${anchor}` : ""}`;
}

// The guide list, asked for once per page load: it only changes with an
// upgrade or a new role, and both reload the page.
let guides: Promise<HelpGuideSummary[]> | null = null;

export function loadGuides(): Promise<HelpGuideSummary[]> {
  guides ??= api.GET("/api/v1/help/guides").then(({ data }) => {
    if (!data) {
      guides = null;
      return [];
    }
    return data.guides;
  });
  return guides;
}

/** Forget the list (after signing in or out, who may read what changes). */
export function forgetGuides() {
  guides = null;
}

/**
 * The guide about the screen at path: the one naming it first in its
 * screens, else any naming it.
 */
export function guideForScreen(list: HelpGuideSummary[], path: string): HelpGuideSummary | undefined {
  return list.find((g) => g.screens[0] === path) ?? list.find((g) => g.screens.includes(path));
}

/** The guide list's sections, in the order people most often need them. */
export const HELP_SECTIONS: { id: HelpGuideSummary["section"]; title: string }[] = [
  { id: "whats-new", title: "What's new" },
  { id: "everyday", title: "Everyday use" },
  { id: "admin", title: "Admin tasks" },
  { id: "running", title: "Keeping it running" },
  { id: "install", title: "Installing and moving" },
];
