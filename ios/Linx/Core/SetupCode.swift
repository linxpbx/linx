import Foundation

// The three ways a setup code reaches the app (docs/PHASE2.md §4). All three
// carry the same one-time ticket, which is good for 10 minutes, for one
// phone, and has nobody's password in it:
//
//   * the QR code on the admin's screen, or on the page an emailed link
//     opens — a link, with the ticket in its #fragment;
//   * that link itself, pasted from the email;
//   * 8 characters typed by hand, with the server's address.

/// A setup code, and the Linx it belongs to.
struct SetupCode: Equatable, Sendable {
    /// The server, as "https://host" with nothing after it.
    let server: URL
    /// The signed ticket from a QR code or a link.
    let token: String?
    /// The 8 characters, when they were typed instead.
    let code: String?

    /// The characters a typed code is made of: no O/0 or I/1/L to mistake
    /// for each other (the server's own alphabet).
    static let alphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
    static let codeLength = 8

    /// The page an emailed link opens.
    static let path = "/set-up-phone"

    /// link reads a scanned or pasted setup link. Everything about it is
    /// checked here: it must be https, it must be Linx's setup page, and the
    /// ticket is in the fragment — the part a browser never sends to a
    /// server, which is why the link can carry it.
    static func link(_ text: String) -> SetupCode? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: trimmed), let parts = URLComponents(url: url, resolvingAgainstBaseURL: false),
            parts.scheme?.lowercased() == "https", let host = parts.host, !host.isEmpty,
            parts.user == nil, parts.password == nil, parts.query == nil,
            parts.path == path || parts.path == path + "/",
            let fragment = parts.fragment?.removingPercentEncoding, !fragment.isEmpty,
            let server = server(host: host, port: parts.port)
        else {
            return nil
        }
        return SetupCode(server: server, token: fragment, code: nil)
    }

    /// byHand reads what someone typed: the server's address and the 8
    /// characters. Spaces, dashes and lower case are all forgiven.
    static func byHand(server text: String, code typed: String) -> SetupCode? {
        let code = typed.uppercased().filter { alphabet.contains($0) }
        guard code.count == codeLength, let server = address(from: text) else { return nil }
        return SetupCode(server: server, token: nil, code: code)
    }

    /// address makes "https://host" out of what someone typed, which may or
    /// may not have https:// in front of it. Plain http is never accepted.
    static func address(from text: String) -> URL? {
        var trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        if trimmed.hasPrefix("https://") {
            trimmed = String(trimmed.dropFirst("https://".count))
        }
        trimmed = trimmed.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        guard !trimmed.isEmpty, !trimmed.contains("/"), !trimmed.contains(" "),
            let parts = URLComponents(string: "https://" + trimmed), let host = parts.host, host.contains(".")
        else {
            return nil
        }
        return server(host: host, port: parts.port)
    }

    private static func server(host: String, port: Int?) -> URL? {
        var parts = URLComponents()
        parts.scheme = "https"
        parts.host = host
        parts.port = port
        return parts.url
    }
}
