// The countries Linx can be set up in (GET /api/v1/countries, ADR-085):
// about 245, with each one's national prefix and emergency numbers. The
// list never changes while the server runs, so it's fetched once per page
// load and shared.
import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";

export type Country = components["schemas"]["Country"];

let loading: Promise<Country[]> | null = null;

function load(): Promise<Country[]> {
  if (!loading) {
    loading = api.GET("/api/v1/countries").then(({ data }) => {
      if (!data) {
        loading = null; // try again next time
        return [];
      }
      return data.items;
    });
  }
  return loading;
}

export function useCountries(): Country[] | null {
  const [list, setList] = useState<Country[] | null>(null);
  useEffect(() => {
    let stale = false;
    void load().then((l) => { if (!stale) setList(l); });
    return () => { stale = true; };
  }, []);
  return list;
}
