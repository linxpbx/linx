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
      return [
        "Extension/Trunk → VoIP Trunks → Add SIP Trunk.",
        `Type: Register SIP Trunk. Provider name: Linx. Host name: ${hostPort}. Transport: TLS.`,
        `Username and Auth ID: ${l.username}. Password: the one above.`,
        "Advanced Settings: codecs PCMA then PCMU; SRTP: Enabled and forced; DTMF: RFC4733.",
        "Inbound Routes for this trunk: send Linx's calls where they should go (for a landline: Dial Trunk, your analog trunk).",
        "Outbound Routes: to send a landline's calls to Linx, add a route whose main trunk is this one.",
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
