// The repair page (docs/INSTALL.md §7, docs/ui/INSTALL_SCREENS.md §5.3):
// when https://<domain> is broken, `sudo linx setup` opens the Server
// settings page on port 6464 too, at the server's bare address. Passkeys
// and company sign-in belong to the domain, so they can't work there.

export const REPAIR_PATH = "/repair";

/** True on the repair page. */
export function onRepairPage(): boolean {
  return window.location.pathname === REPAIR_PATH;
}

/** Where the sign-in goes back to when it starts over. */
export function signInHome(): string {
  return onRepairPage() ? REPAIR_PATH : "/";
}
