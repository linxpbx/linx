import Foundation

// One SIP message, read and written (RFC 3261 §7). The app writes its own SIP
// because every maintained library for it is GPL or LGPL, which client
// bundles may not carry (ADR-006); the subset is small, because the /sip
// relay allows a small subset and nothing else (internal/siprelay).
//
// Everything here is plain text in and plain text out, so it is all testable
// without a network, a server or WebRTC.

/// A SIP message: its first line, its headers in the order they came, and its
/// body (an SDP offer or answer, or nothing).
struct SIPMessage: Equatable, Sendable {
    enum Start: Equatable, Sendable {
        case request(method: String, uri: String)
        case response(status: Int, reason: String)
    }

    struct Header: Equatable, Sendable {
        var name: String
        var value: String
    }

    var start: Start
    var headers: [Header]
    var body: String

    init(start: Start, headers: [Header] = [], body: String = "") {
        self.start = start
        self.headers = headers
        self.body = body
    }

    static func request(_ method: String, _ uri: String) -> SIPMessage {
        SIPMessage(start: .request(method: method, uri: uri))
    }

    /// A response that answers `request`, carrying the headers RFC 3261 §8.2.6
    /// says to copy back: Via, From, To, Call-ID and CSeq.
    static func response(_ status: Int, _ reason: String, to request: SIPMessage) -> SIPMessage {
        var message = SIPMessage(start: .response(status: status, reason: reason))
        for name in ["Via", "From", "To", "Call-ID", "CSeq"] {
            for value in request.all(name) {
                message.add(name, value)
            }
        }
        return message
    }

    var method: String? {
        if case .request(let method, _) = start { return method }
        return nil
    }

    var requestURI: String? {
        if case .request(_, let uri) = start { return uri }
        return nil
    }

    var status: Int? {
        if case .response(let status, _) = start { return status }
        return nil
    }

    var reason: String? {
        if case .response(_, let reason) = start { return reason }
        return nil
    }

    // MARK: - Headers

    /// The first value of a header, by either its full or its one-letter name
    /// (RFC 3261 §7.3.3). Names never matter in their case.
    func first(_ name: String) -> String? { all(name).first }

    func all(_ name: String) -> [String] {
        let wanted = Self.canonical(name)
        return headers.filter { Self.canonical($0.name) == wanted }.map(\.value)
    }

    mutating func add(_ name: String, _ value: String) {
        headers.append(Header(name: name, value: value))
    }

    /// Replaces every copy of a header with one, keeping where the first one
    /// was; adds it at the end if there was none.
    mutating func set(_ name: String, _ value: String) {
        let wanted = Self.canonical(name)
        guard let at = headers.firstIndex(where: { Self.canonical($0.name) == wanted }) else {
            headers.append(Header(name: name, value: value))
            return
        }
        for i in stride(from: headers.count - 1, to: at, by: -1)
        where Self.canonical(headers[i].name) == wanted {
            headers.remove(at: i)
        }
        headers[at] = Header(name: name, value: value)
    }

    mutating func remove(_ name: String) {
        let wanted = Self.canonical(name)
        headers.removeAll { Self.canonical($0.name) == wanted }
    }

    var callID: String? { first("Call-ID") }

    /// The CSeq's number and method ("314159 INVITE").
    var cseq: (number: Int, method: String)? {
        let parts = (first("CSeq") ?? "").split(separator: " ", omittingEmptySubsequences: true)
        guard parts.count == 2, let number = Int(parts[0]) else { return nil }
        return (number, String(parts[1]).uppercased())
    }

    var fromTag: String? { Self.parameter("tag", in: first("From") ?? "") }
    var toTag: String? { Self.parameter("tag", in: first("To") ?? "") }

    /// The user part of a header's URI ("sip:101@pbx.example.com" → "101").
    static func user(of header: String) -> String {
        let uri = Self.uri(in: header)
        let scheme = uri.firstIndex(of: ":").map { uri.index(after: $0) } ?? uri.startIndex
        let end = uri.lastIndex(of: "@") ?? uri.firstIndex(of: ";") ?? uri.endIndex
        guard scheme <= end else { return "" }
        return String(uri[scheme..<end])
    }

    /// The display name of a header, if it has one ("Sara" <sip:…>).
    static func displayName(of header: String) -> String? {
        guard let open = header.firstIndex(of: "<") else { return nil }
        let name = header[header.startIndex..<open].trimmingCharacters(in: .whitespaces)
        let unquoted = name.hasPrefix("\"") && name.hasSuffix("\"") && name.count > 1
        return unquoted ? String(name.dropFirst().dropLast()) : (name.isEmpty ? nil : name)
    }

    /// The URI inside a header, with its angle brackets and parameters gone
    /// if it had any (`"Sara" <sip:101@host;transport=ws>;tag=abc`).
    static func uri(in header: String) -> String {
        if let open = header.firstIndex(of: "<"), let close = header[open...].firstIndex(of: ">") {
            return String(header[header.index(after: open)..<close])
        }
        let head = header.split(separator: ";", maxSplits: 1, omittingEmptySubsequences: false)[0]
        return head.trimmingCharacters(in: .whitespaces)
    }

    /// A header parameter's value (`;tag=abc`), outside the angle brackets.
    static func parameter(_ name: String, in header: String) -> String? {
        var rest = Substring(header)
        if let close = header.firstIndex(of: ">") { rest = header[header.index(after: close)...] }
        for part in rest.split(separator: ";").dropFirst(0) {
            let pair = part.split(separator: "=", maxSplits: 1)
            guard pair.count == 2, pair[0].trimmingCharacters(in: .whitespaces).lowercased() == name.lowercased()
            else { continue }
            return pair[1].trimmingCharacters(in: .whitespaces)
        }
        return nil
    }

    private static let compactForms: [Character: String] = [
        "f": "from", "t": "to", "i": "call-id", "m": "contact", "v": "via",
        "l": "content-length", "c": "content-type", "s": "subject", "k": "supported",
        "e": "content-encoding", "x": "session-expires",
    ]

    private static func canonical(_ name: String) -> String {
        let lower = name.trimmingCharacters(in: .whitespaces).lowercased()
        if lower.count == 1, let first = lower.first, let full = compactForms[first] { return full }
        return lower
    }

    // MARK: - Reading and writing

    /// text is the message on the wire: CRLF everywhere, Content-Length always
    /// told the truth, and one blank line before the body.
    var text: String {
        var out = ""
        switch start {
        case .request(let method, let uri): out += "\(method) \(uri) SIP/2.0\r\n"
        case .response(let status, let reason): out += "SIP/2.0 \(status) \(reason)\r\n"
        }
        for header in headers where Self.canonical(header.name) != "content-length" {
            out += "\(header.name): \(header.value)\r\n"
        }
        out += "Content-Length: \(body.utf8.count)\r\n\r\n"
        return out + body
    }

    /// A message read off the wire. Returns nil for anything that isn't one,
    /// including the blank keep-alive frames (RFC 5626 §3.5.1).
    init?(text: String) {
        guard !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        // The body is kept exactly as it came — an SDP's line endings are
        // its own business (RFC 4566) — while the head is read with either.
        let separator = text.range(of: "\r\n\r\n") ?? text.range(of: "\n\n")
        let head = separator.map { String(text[text.startIndex..<$0.lowerBound]) } ?? text
        let body = separator.map { String(text[$0.upperBound...]) } ?? ""
        var lines = head.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        guard let firstLine = lines.first else { return nil }
        lines.removeFirst()

        let fields = firstLine.split(separator: " ", maxSplits: 2, omittingEmptySubsequences: true)
        if firstLine.hasPrefix("SIP/2.0") {
            guard fields.count >= 2, let status = Int(fields[1]) else { return nil }
            start = .response(status: status, reason: fields.count > 2 ? String(fields[2]) : "")
        } else {
            guard fields.count == 3, fields[2].hasPrefix("SIP/2.0") else { return nil }
            start = .request(method: String(fields[0]).uppercased(), uri: String(fields[1]))
        }

        var headers: [Header] = []
        for line in lines where !line.isEmpty {
            // A line that starts with a space belongs to the header above it
            // (RFC 3261 §7.3.1).
            if line.first == " " || line.first == "\t", !headers.isEmpty {
                headers[headers.count - 1].value += " " + line.trimmingCharacters(in: .whitespaces)
                continue
            }
            guard let colon = line.firstIndex(of: ":") else { return nil }
            headers.append(
                Header(
                    name: String(line[line.startIndex..<colon]).trimmingCharacters(in: .whitespaces),
                    value: String(line[line.index(after: colon)...]).trimmingCharacters(in: .whitespaces)))
        }
        self.headers = headers
        self.body = body
    }
}

/// Random text for the parts of SIP that have to be unguessable and unique:
/// branches, tags, Call-IDs and the one-time cnonce.
enum SIPRandom {
    static func token(_ length: Int = 12) -> String {
        let alphabet = Array("abcdefghijklmnopqrstuvwxyz0123456789")
        var out = ""
        for _ in 0..<length {
            out.append(alphabet[Int.random(in: 0..<alphabet.count)])
        }
        return out
    }

    /// The branch of a Via, which every transaction needs a new one of. The
    /// magic cookie in front is RFC 3261 §8.1.1.7.
    static func branch() -> String { "z9hG4bK" + token(16) }
}
