import Foundation
import Network

/// Watches, for a call's first minute, whether the network is holding back
/// what this phone sends over UDP, and says so when it keeps happening.
///
/// The owner's iPhone on Etisalat (2026-10-16): for the first 30–45 s of
/// every call, everything it sent over UDP — sound, picture and all —
/// stopped reaching Linx for 2.3 s at a time, every 3–4 s, then arrived in
/// one burst; what Linx sent it was never late. Ordinary uploads from the
/// same phone over TCP went through without a pause, to Linx and to a server
/// abroad, and a call forced onto the relay's TLS stalled once instead of
/// ten times. du, and the same phone on du, never did it.
///
/// So the phone asks the relay "are you there?" four times a second over
/// UDP (a STUN Binding Request: 20 bytes, answered with about 32), and an
/// answer held up for `stall` or more is the network holding things back.
/// Two of those within `within` and `onStalls` is called, once; the call
/// then moves onto the secure route (`SIPUserAgent.moveToSecureRoute`).
/// About 2 kbit/s each way, for at most a minute of each call.
@MainActor final class UploadWatch {
    static let every: Duration = .milliseconds(250)
    /// Well above any round trip a working link has (a ship's satellite
    /// link is 0.8 s) and below Etisalat's 2.3 s holds.
    nonisolated static let stall: TimeInterval = 1.2
    nonisolated static let within: TimeInterval = 30

    private var connection: NWConnection?
    private var ticking: Task<Void, Never>?
    private var counter = StallCounter(stall: stall, within: within)
    private var asked: [Data: Date] = [:]
    private var onStall: ((TimeInterval) -> Void)?
    private var onStalls: (() -> Void)?
    /// Whether it is watching now.
    var on: Bool { ticking != nil }

    /// Starts watching the relay's UDP address, if it has one and the relay
    /// can also be reached over TLS (there is nowhere to move to
    /// otherwise). `onStall` hears of each hold as it ends, with how long it
    /// was; `onStalls` once there have been enough of them.
    func start(
        relay urls: [String], onStall: @escaping (TimeInterval) -> Void, onStalls: @escaping () -> Void
    ) {
        guard ticking == nil, urls.contains(where: { $0.hasPrefix("turns:") }),
            let (host, port) = Self.udpRelay(in: urls)
        else { return }
        let connection = NWConnection(
            host: NWEndpoint.Host(host), port: NWEndpoint.Port(rawValue: port) ?? 3478, using: .udp)
        connection.start(queue: DispatchQueue(label: "linx.upload-watch", qos: .userInitiated))
        self.connection = connection
        self.onStall = onStall
        self.onStalls = onStalls
        counter = StallCounter(stall: Self.stall, within: Self.within)
        asked = [:]
        receive(on: connection)
        ticking = Task { [weak self] in
            while !Task.isCancelled {
                self?.tick()
                try? await Task.sleep(for: Self.every)
            }
        }
    }

    func stop() {
        ticking?.cancel()
        ticking = nil
        connection?.cancel()
        connection = nil
        onStall = nil
        onStalls = nil
        asked = [:]
    }

    private func tick() {
        guard let connection else { return }
        let now = Date()
        let request = Self.bindingRequest()
        asked[request.id] = now
        connection.send(content: request.bytes, completion: .idempotent)
        // A question unanswered for longer than a hold is gone, not held.
        asked = asked.filter { now.timeIntervalSince($0.value) < 10 }
        if let oldest = asked.values.min() { counter.waiting(since: oldest, now: now) }
    }

    private func receive(on connection: NWConnection) {
        connection.receiveMessage { [weak self] data, _, _, error in
            let id = data.flatMap(Self.answeredID)
            Task { @MainActor [weak self] in
                guard let self, self.connection === connection else { return }
                if let id, let sent = self.asked.removeValue(forKey: id) {
                    // Questions asked before this one and still unanswered
                    // were lost on the way, not held: they are forgotten, so
                    // a lost packet never looks like a stall.
                    self.asked = self.asked.filter { $0.value > sent }
                    self.answered(after: Date().timeIntervalSince(sent))
                }
                if error == nil { self.receive(on: connection) }
            }
        }
    }

    private func answered(after wait: TimeInterval) {
        switch counter.answered(after: wait, now: Date()) {
        case .fine:
            break
        case .stalled(let hold):
            onStall?(hold)
        case .keepsStalling(let hold):
            onStall?(hold)
            let then = onStalls
            stop()
            then?()
        }
    }

    /// The relay's UDP address among its URLs ("turn:host:port?transport=udp";
    /// a "turn:" URL with no transport is UDP too, on 3478 if no port).
    nonisolated static func udpRelay(in urls: [String]) -> (host: String, port: UInt16)? {
        for url in urls where url.hasPrefix("turn:") {
            let rest = url.dropFirst("turn:".count)
            let parts = rest.split(separator: "?", maxSplits: 1).map(String.init)
            let address = parts.first ?? ""
            if parts.count > 1, parts[1].contains("transport=tcp") { continue }
            let host: String
            var port: UInt16 = 3478
            if let colon = address.lastIndex(of: ":"), !address.hasSuffix("]") {
                host = String(address[..<colon])
                guard let p = UInt16(address[address.index(after: colon)...]) else { continue }
                port = p
            } else {
                host = address
            }
            guard !host.isEmpty else { continue }
            return (host.trimmingCharacters(in: CharacterSet(charactersIn: "[]")), port)
        }
        return nil
    }

    /// A STUN Binding Request (RFC 8489) with nothing in it, which a TURN
    /// server answers without asking who is asking; `id` is its transaction.
    nonisolated static func bindingRequest() -> (bytes: Data, id: Data) {
        var id = Data(count: 12)
        for i in 0..<12 { id[i] = UInt8.random(in: 0...255) }
        return (Data([0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xA4, 0x42]) + id, id)
    }

    /// The transaction a STUN success answer is for, or nil.
    nonisolated static func answeredID(_ data: Data) -> Data? {
        let bytes = [UInt8](data)
        guard bytes.count >= 20, bytes[0] == 0x01, bytes[1] == 0x01,
            bytes[4] == 0x21, bytes[5] == 0x12, bytes[6] == 0xA4, bytes[7] == 0x42
        else { return nil }
        return Data(bytes[8..<20])
    }
}

/// When a held-back answer is a stall, and when stalls are a pattern: the
/// part of `UploadWatch` that has no network in it.
struct StallCounter {
    enum Verdict: Equatable {
        case fine
        /// A hold just ended, this long.
        case stalled(TimeInterval)
        /// …and it is one of enough of them.
        case keepsStalling(TimeInterval)
    }

    let stall: TimeInterval
    let within: TimeInterval
    /// When each stall so far ended.
    private(set) var stalls: [Date] = []
    /// The longest any question has waited in the stall that is going on.
    private var holding: TimeInterval = 0

    init(stall: TimeInterval, within: TimeInterval) {
        self.stall = stall
        self.within = within
    }

    /// The oldest unanswered question was asked at `since`.
    mutating func waiting(since: Date, now: Date) {
        holding = max(holding, now.timeIntervalSince(since))
    }

    /// An answer came, `wait` after its question.
    mutating func answered(after wait: TimeInterval, now: Date) -> Verdict {
        holding = max(holding, wait)
        // A burst of late answers is one stall: it ends with the first
        // answer that wasn't kept waiting.
        guard wait < stall, holding >= stall else { return .fine }
        let hold = holding
        holding = 0
        stalls = stalls.filter { now.timeIntervalSince($0) <= within } + [now]
        return stalls.count >= 2 ? .keepsStalling(hold) : .stalled(hold)
    }
}
