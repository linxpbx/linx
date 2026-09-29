import { describe, expect, it } from "vitest";
import { parseProviderText } from "./providerPaste";

describe("parseProviderText", () => {
  it("reads a typical welcome email", () => {
    const got = parseProviderText(`Welcome to Example Voice!

Your SIP trunk settings:
SIP Server: sip.example-voice.com
Outbound Proxy: proxy.example-voice.com:5060
Username: 4412345
Auth ID: 4412345a
Password: S3cr3t!pw
Port: 5061
Transport: TLS
Your DID: +971 4 200 0100`);
    expect(got).toEqual({
      host: "sip.example-voice.com", port: 5061, username: "4412345a", password: "S3cr3t!pw", transport: "tls",
      numbers: ["+97142000100"],
    });
  });

  it("prefers the registrar, and a value on the next line", () => {
    const got = parseProviderText("Proxy = edge.example.net\nRegistrar = sips:reg.example.net:5061;transport=tls\nUser name\nalice01\nSecret\txyz789");
    expect(got.host).toBe("reg.example.net");
    expect(got.port).toBe(5061);
    expect(got.username).toBe("alice01");
    expect(got.password).toBe("xyz789");
  });

  it("finds the domain in a user@domain login and lists several numbers", () => {
    const got = parseProviderText("Login: bob@voip.example.org\nPhone numbers: 042000101, 042000102");
    expect(got.host).toBe("voip.example.org");
    expect(got.username).toBe("bob");
    expect(got.numbers).toEqual(["042000101", "042000102"]);
  });

  it("returns nothing it isn't sure of", () => {
    expect(parseProviderText("Hello, thanks for signing up.\nRegards")).toEqual({ numbers: [] });
  });
});
