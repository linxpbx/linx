import { describe, expect, it } from "vitest";
import { opensBySelf } from "./serverSettings";

describe("opensBySelf", () => {
  it("follows a move to another port of the same name, both ways", () => {
    expect(opensBySelf("https://vps.example.com:8443", "https://vps.example.com/admin/system/server")).toBe(true);
    expect(opensBySelf("https://vps.example.com", "https://vps.example.com:8443/admin/system/server")).toBe(true);
  });
  it("leaves a new domain, and staying put, to the person", () => {
    expect(opensBySelf("https://pbx.example.org", "https://vps.example.com/admin/system/server")).toBe(false);
    expect(opensBySelf("https://vps.example.com", "https://vps.example.com/admin/system/server")).toBe(false);
    expect(opensBySelf("", "https://vps.example.com/")).toBe(false);
  });
});
