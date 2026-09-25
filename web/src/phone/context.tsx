import { createContext, useContext, useSyncExternalStore } from "react";
import type { LineState, PhoneLine } from "./line";

export const PhoneContext = createContext<PhoneLine | null>(null);

export function usePhoneLine(): PhoneLine {
  const line = useContext(PhoneContext);
  if (!line) throw new Error("usePhoneLine outside PhoneContext");
  return line;
}

export function usePhoneState(): LineState {
  const line = usePhoneLine();
  return useSyncExternalStore(line.subscribe, line.getState);
}
