// "How to enter this" for a phone system or gateway that signs in to Linx
// (docs/SIMPLER.md §1.2): the same few values, in each product's words.
// Menu names change between firmware versions; the values never do.

export interface GatewayLogin {
  server: string;
  port: number;
  username: string;
  password: string;
}

export const GATEWAY_PRODUCTS = [
  { id: "ucm", label: "Grandstream UCM" },
  { id: "gxw", label: "Grandstream GXW or HT" },
  { id: "yeastar", label: "Yeastar" },
  { id: "freepbx", label: "FreePBX or Asterisk" },
  { id: "other", label: "Something else" },
] as const;
export type GatewayProduct = (typeof GATEWAY_PRODUCTS)[number]["id"];

export function gatewaySteps(product: GatewayProduct, l: GatewayLogin): string[] {
  const hostPort = `${l.server}:${l.port}`;
  switch (product) {
    case "ucm":
      // As done on a UCM6304 (firmware 1.0.33) in the Phase 1E demo.
      return [
        "Extension/Trunk → VoIP Trunks → Add SIP Trunk: Register SIP Trunk (if it asks for a provider template, any will do: you change the fields).",
        `Host name: ${hostPort}. Transport: TLS. SIP URI scheme when using TLS: SIP. Need registration: on. NAT: off. TEL URI: disabled.`,
        `Username and Auth ID: ${l.username}. Password: the one above. Keep original CID: on. Verify inbound request: off (Linx doesn't answer a password challenge).`,
        "Advanced settings: codecs PCMA then PCMU; SRTP: Enabled and forced; DTMF: RFC4733.",
        "Linx's calls out through your landline: Inbound Routes, trunk Linx → Add: pattern _X., Default Destination By DID, Strip 0, then Dial Trunk on with your analog trunk. Give it the privilege your landline's own outbound route requires — Local is often not enough for mobile or national numbers, and then the call gets a spoken \"you are not allowed\" and ends (the UCM refusing it, with a 603; Linx has already done its part). Watch Strip 0 too: it is there for calls coming in, and Linx dials outgoing numbers in full, so the analog trunk must still receive the leading 0.",
        "What Linx shows as the caller is this line's Caller ID, and when that is empty, the extension's own number (201, 202…). So a landline outbound route with privilege Disable or a Source Caller ID Pattern lets some extensions out and tells others they aren't allowed — the UCM saying it, not Linx. The cure is on this page: set this line's Caller ID to the landline's own number, which is what the phone company shows for an analog line anyway. Otherwise add the extensions to that route's Source Caller ID Pattern (e.g. _2XX).",
        "Landline calls to Linx: Outbound Routes → Add To_Linx: pattern _*88X., privilege Internal, main trunk Linx, Strip 3. Then the analog trunk's inbound route: Default Destination External Number *88 followed by the landline number.",
      ];
    case "gxw":
      return [
        "Open the gateway's web page → Profiles (or FXS/FXO Ports → Profile 1).",
        `Primary SIP Server: ${hostPort}. SIP Transport: TLS. SIP Registration: Yes.`,
        `SIP User ID and Authenticate ID: ${l.username}. Password: the one above.`,
        "SRTP Mode: Enabled and forced. DTMF: RFC2833 (RFC4733). Preferred codecs: PCMA, PCMU.",
        "On each port using this profile, set the same User ID and password (or the gateway's \"account\" for all ports).",
      ];
    case "yeastar":
      return [
        "Extension and Trunk → Trunk → Add.",
        `Trunk type: Register Trunk. Transport: TLS. Hostname/IP: ${l.server}, port ${l.port}.`,
        `Username and Authentication name: ${l.username}. Password: the one above.`,
        "Codecs: PCMA and PCMU. SRTP: Enabled.",
        "Then an inbound route from this trunk, and an outbound route that uses it.",
      ];
    case "freepbx":
      return [
        "Connectivity → Trunks → Add SIP (chan_pjsip) Trunk.",
        `pjsip Settings: Authentication: Outbound; Registration: Send; SIP Server: ${l.server}; SIP Server Port: ${l.port}; Transport: a TLS transport.`,
        `Username and Auth username: ${l.username}. Secret: the password above.`,
        "Advanced: Media Encryption: SRTP via in-SDP (SDES). Codecs: alaw, ulaw.",
      ];
    default:
      return [
        "Add a SIP trunk (sometimes called a VoIP account or SIP account) that registers.",
        `Server (registrar and proxy): ${l.server}, port ${l.port}, transport TLS.`,
        `Username (and authentication ID, if asked): ${l.username}. Password: the one above.`,
        "Encrypted audio (SRTP, SDES): required. Codecs: G.711 A-law and µ-law.",
      ];
  }
}
