// Help (docs/HELP.md): the guides the server lets this person read, and
// which one is about the screen they're on (the ? button).
import { api, CSRF_HEADER, csrfToken, problemMessage } from "@/api/client";
import type { components } from "@/api/schema";

export type HelpGuideSummary = components["schemas"]["HelpGuideSummary"];
export type HelpGuide = components["schemas"]["HelpGuide"];
export type HelpBlock = components["schemas"]["HelpBlock"];
export type HelpInline = components["schemas"]["HelpInline"];
export type HelpSearchResult = components["schemas"]["HelpSearchResult"];
export type HelpGuideRef = components["schemas"]["HelpGuideRef"];

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
// upgrade, a new role or written answers turned on or off, and each of
// those reloads it.
let list: Promise<components["schemas"]["HelpGuideList"] | null> | null = null;

function loadList() {
  list ??= api.GET("/api/v1/help/guides").then(({ data }) => {
    if (!data) list = null;
    return data ?? null;
  });
  return list;
}

export async function loadGuides(): Promise<HelpGuideSummary[]> {
  return (await loadList())?.guides ?? [];
}

/** Who writes answers for this person ("" when there are none). */
export async function loadAnswersBy(): Promise<string> {
  return (await loadList())?.answers_by ?? "";
}

/** Forget the list (after signing in or out, who may read what changes). */
export function forgetGuides() {
  list = null;
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

export type AnswerEnd = { guides: HelpGuideRef[]; by: string; cut: boolean } | { error: string };

/**
 * Asks for a written answer (docs/HELP.md §4), calling onText with each
 * piece as it's written. The answer is text, shown as text.
 */
export async function askHelp(question: string, onText: (text: string) => void, signal: AbortSignal): Promise<AnswerEnd> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const token = csrfToken();
  if (token) headers[CSRF_HEADER] = token;
  let resp: Response;
  try {
    resp = await fetch("/api/v1/help/answer", { method: "POST", headers, body: JSON.stringify({ question }), signal, credentials: "same-origin" });
  } catch {
    return { error: signal.aborted ? "" : "Linx couldn't be reached. Check your connection." };
  }
  if (!resp.ok || !resp.body) {
    return { error: problemMessage(await resp.json().catch(() => null)) };
  }
  const reader = resp.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  for (;;) {
    let chunk: ReadableStreamReadResult<string>;
    try {
      chunk = await reader.read();
    } catch {
      return { error: signal.aborted ? "" : "The answer stopped part way. Try again." };
    }
    if (chunk.done) break;
    buf += chunk.value;
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      const msg = JSON.parse(line) as { text?: string; done?: boolean; guides?: HelpGuideRef[]; by?: string; cut?: boolean; error?: string };
      if (msg.text !== undefined) onText(msg.text);
      else if (msg.error !== undefined) return { error: msg.error };
      else if (msg.done) return { guides: msg.guides ?? [], by: msg.by ?? "", cut: !!msg.cut };
    }
  }
  return { error: "The answer stopped part way. Try again." };
}
