// Role checks shared by the admin shell, home and setup wizard
// (docs/ADMIN.md §3, §8).
import type { Me } from "@/api/client";

export function isAdminRole(role?: string): boolean {
  return role === "admin" || role === "system_admin";
}

/** Whether this signed-in person sees the Admin area at all (admin, system_admin or reporter). */
export function seesAdminArea(me: Me): boolean {
  return isAdminRole(me.role) || me.role === "reporter";
}

/** Reporters see every admin page, but can't change anything. */
export function isReadOnlyAdmin(me: Me): boolean {
  return me.role === "reporter";
}

export function hasScope(me: Me, scope: string): boolean {
  return me.scopes.includes(scope);
}
