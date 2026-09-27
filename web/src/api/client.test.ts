import { describe, expect, it } from "vitest";
import { mergePatch } from "./client";

const run = (method: string, path: string) => {
  const request = new Request(`https://meet.example.com${path}`, {
    method, body: method === "GET" ? undefined : "{}", headers: { "Content-Type": "application/json" },
  });
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const out = (mergePatch.onRequest as any)({ request }) as Request;
  return out.headers.get("Content-Type");
};

describe("mergePatch", () => {
  it("labels a PATCH as a JSON Merge Patch", () => {
    expect(run("PATCH", "/api/v1/backup-settings")).toBe("application/merge-patch+json");
    expect(run("PATCH", "/api/v1/users/0199")).toBe("application/merge-patch+json");
  });
  it("leaves renaming a passkey and every other method alone", () => {
    expect(run("PATCH", "/api/v1/me/passkeys/0199")).toBe("application/json");
    expect(run("POST", "/api/v1/backups")).toBe("application/json");
    expect(run("PUT", "/api/v1/setup")).toBe("application/json");
  });
});
