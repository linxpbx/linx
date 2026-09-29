// What phones can call, as switches (docs/ui/ADMIN_SCREENS_PHASE1E.md §3.2
// "Calls" and §8): each switch is one or more numbering categories. Shared
// by the setup wizard and Outgoing calls.
import type { components } from "@/api/schema";

export type NumberCategory = components["schemas"]["NumberCategory"];

export const CATEGORY_SWITCHES: { key: NumberCategory[]; label: string; hint?: string; recommended: boolean; costly?: boolean }[] = [
  { key: ["landline", "service"], label: "Local numbers", recommended: true },
  { key: ["mobile"], label: "Mobiles", recommended: true },
  { key: ["national"], label: "Other cities in the UAE", recommended: true },
  { key: ["toll_free"], label: "Free numbers (800)", recommended: true },
  { key: ["international"], label: "Abroad", hint: "Recommended off: most phone fraud is calls abroad", recommended: false, costly: true },
  { key: ["premium"], label: "Premium-rate", hint: "Costs a lot per minute", recommended: false, costly: true },
];

/** Only with Simple mode off. */
export const EXPERT_CATEGORY_SWITCHES: { key: NumberCategory[]; label: string; hint?: string }[] = [
  { key: ["shared_cost"], label: "Shared-cost numbers", hint: "The caller pays part of the cost" },
];
