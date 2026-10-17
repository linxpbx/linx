import Foundation
import Network
import UIKit

/// What the phone itself was doing during the first two minutes of a call,
/// on the call's timeline: a diagnostic, not a fix.
///
/// The owner's iPhone on mobile data stops sending everything — the sound
/// and the picture together — for 2.5–3.5 s at a time, several times in a
/// call's first minute, and the held packets then arrive in one burst
/// (Linx's captures, 2026-10-17). Safari on the same phone was held far
/// less at the same rate, and calls the iPhone answers were held more than
/// calls it makes. Three things tell where the hold is:
///
/// - **The side line** (`SideLine`): a separate TLS connection to the same
///   relay, through the same front door, asking "are you there?" four
///   times a second from a queue of its own — nothing of WebRTC's and
///   nothing of the main thread's. Held at the same moments as the call:
///   the hold is below the app (iOS or the network). Answering while the
///   call is held: it is WebRTC's own sending.
/// - **The main thread**: a tick every 100 ms; a gap is the app's own main
///   thread stuck, which WebRTC's calls from the app wait on.
/// - **Where the app is**: in front or in the background, the screen locked
///   or not — a call answered from the lock screen runs in the background.
@MainActor final class CallWatch {
    static let lasts: Duration = .seconds(120)

    private let side: SideLine?
    private var ticking: Task<Void, Never>?
    private var observers: [NSObjectProtocol] = []
    private let mark: @MainActor @Sendable (String, Date) -> Void

    init(relay urls: [String], mark: @escaping @MainActor @Sendable (String, Date) -> Void) {
        self.mark = mark
        if let (host, port) = RelayCertificates.tlsEndpoints(in: urls).first {
            side = SideLine(host: host, port: port) { words, at in
                Task { @MainActor in mark(words, at) }
            }
        } else {
            side = nil
        }
    }

    func start() {
        side?.start()
        watchTheMainThread()
        watchWhereTheAppIs()
        let state: String
        switch UIApplication.shared.applicationState {
        case .active: state = "in front"
        case .inactive: state = "in front, not active"
        case .background: state = "in the background"
        @unknown default: state = "somewhere"
        }
        mark("App \(state)\(UIApplication.shared.isProtectedDataAvailable ? "" : ", screen locked")", Date())
        Task { [weak self] in
            try? await Task.sleep(for: Self.lasts)
            self?.stop()
        }
    }

    func stop() {
        side?.stop()
        ticking?.cancel()
        ticking = nil
        for observer in observers { NotificationCenter.default.removeObserver(observer) }
        observers = []
    }

    private func watchTheMainThread() {
        ticking = Task { [weak self] in
            let clock = ContinuousClock()
            var last = clock.now
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(100))
                let now = clock.now
                let gap = now - last
                last = now
                if gap > .milliseconds(600) {
                    let seconds = Double(gap.components.seconds) + Double(gap.components.attoseconds) / 1e18
                    self?.mark(
                        String(format: "The app's main thread was stuck for %.1f s", seconds),
                        Date().addingTimeInterval(-seconds))
                }
            }
        }
    }

    private func watchWhereTheAppIs() {
        let said: [(Notification.Name, String)] = [
            (UIApplication.didEnterBackgroundNotification, "App went to the background"),
            (UIApplication.willEnterForegroundNotification, "App coming to the front"),
            (UIApplication.didBecomeActiveNotification, "App active"),
            (UIApplication.willResignActiveNotification, "App no longer active"),
            (UIApplication.protectedDataWillBecomeUnavailableNotification, "Screen locked"),
            (UIApplication.protectedDataDidBecomeAvailableNotification, "Screen unlocked"),
            (ProcessInfo.thermalStateDidChangeNotification, "Phone's temperature changed"),
        ]
        for (name, words) in said {
            observers.append(
                NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                    let at = Date()
                    var thermal = ""
                    if name == ProcessInfo.thermalStateDidChangeNotification {
                        thermal = ": \(Self.thermal(ProcessInfo.processInfo.thermalState))"
                    }
                    MainActor.assumeIsolated { self?.mark(words + thermal, at) }
                })
        }
    }

    nonisolated private static func thermal(_ state: ProcessInfo.ThermalState) -> String {
        switch state {
        case .nominal: "normal"
        case .fair: "warm"
        case .serious: "hot"
        case .critical: "too hot"
        @unknown default: "unknown"
        }
    }
}

/// A separate TLS connection to Linx's relay that asks "are you there?"
/// (a STUN Binding Request, which the relay answers without asking who is
/// asking) every quarter of a second, and says each time no answer came
/// for a second or more. Everything here happens on `queue`, which is its
/// own: neither WebRTC's threads nor the main thread hold it up. About
/// 1 kbit/s each way.
final class SideLine: @unchecked Sendable {
    static let every: TimeInterval = 0.25
    static let stall: TimeInterval = 1.0

    private let host: String
    private let port: UInt16
    private let tell: @Sendable (String, Date) -> Void
    private let queue = DispatchQueue(label: "linx.side-line", qos: .userInitiated)

    // Only ever touched on `queue`.
    private var connection: NWConnection?
    private var timer: DispatchSourceTimer?
    private var asked: [Data: Date] = [:]
    private var counter = StallCounter(stall: SideLine.stall, within: 3600)
    private var buffer = Data()
    private var waits: [TimeInterval] = []
    private var holds: [TimeInterval] = []
    private var opened: Date?
    private var stopped = false

    init(host: String, port: UInt16, tell: @escaping @Sendable (String, Date) -> Void) {
        self.host = host
        self.port = port
        self.tell = tell
    }

    func start() {
        queue.async { self.open() }
    }

    func stop() {
        queue.async {
            guard !self.stopped else { return }
            self.stopped = true
            self.timer?.cancel()
            self.timer = nil
            self.connection?.cancel()
            self.connection = nil
            guard self.opened != nil else { return }
            let sorted = self.waits.sorted()
            let typical = sorted.isEmpty ? 0 : sorted[sorted.count / 2]
            let longest = self.holds.max() ?? 0
            self.tell(
                String(
                    format: "Side line: %d answers, typically in %d ms; held %d times, longest %.1f s",
                    self.waits.count, Int(typical * 1000), self.holds.count, longest), Date())
        }
    }

    private func open() {
        guard !stopped else { return }
        let began = Date()
        let tls = NWProtocolTLS.Options()
        sec_protocol_options_set_tls_server_name(tls.securityProtocolOptions, host)
        let tcp = NWProtocolTCP.Options()
        tcp.noDelay = true
        let connection = NWConnection(
            host: NWEndpoint.Host(host), port: NWEndpoint.Port(rawValue: port) ?? 443,
            using: NWParameters(tls: tls, tcp: tcp))
        connection.stateUpdateHandler = { [weak self] state in
            guard let self else { return }
            switch state {
            case .ready:
                self.opened = Date()
                self.tell("Side line open in \(Int(Date().timeIntervalSince(began) * 1000)) ms", Date())
                self.receive(on: connection)
                self.startAsking()
            case .failed(let error):
                self.tell("Side line failed: \(error.localizedDescription)", Date())
                self.timer?.cancel()
                self.timer = nil
            default:
                break
            }
        }
        self.connection = connection
        connection.start(queue: queue)
    }

    private func startAsking() {
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now(), repeating: Self.every, leeway: .milliseconds(10))
        timer.setEventHandler { [weak self] in self?.ask() }
        timer.resume()
        self.timer = timer
    }

    private func ask() {
        guard let connection, !stopped else { return }
        let now = Date()
        let request = UploadWatch.bindingRequest()
        asked[request.id] = now
        connection.send(content: request.bytes, completion: .idempotent)
        asked = asked.filter { now.timeIntervalSince($0.value) < 10 }
        if let oldest = asked.values.min() { counter.waiting(since: oldest, now: now) }
    }

    private func receive(on connection: NWConnection) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 4096) { [weak self] data, _, done, error in
            guard let self, !self.stopped else { return }
            if let data { self.buffer.append(data) }
            for message in Self.messages(from: &self.buffer) {
                guard let id = UploadWatch.answeredID(message) else { continue }
                self.answered(id)
            }
            if error == nil, !done { self.receive(on: connection) }
        }
    }

    private func answered(_ id: Data) {
        guard let sent = asked.removeValue(forKey: id) else { return }
        let now = Date()
        // Over TCP nothing is lost, so questions asked before this one are
        // answered before it too; any left are from a line that broke.
        asked = asked.filter { $0.value > sent }
        let wait = now.timeIntervalSince(sent)
        if waits.count < 2000 { waits.append(wait) }
        switch counter.answered(after: wait, now: now) {
        case .fine:
            break
        case .stalled(let hold), .keepsStalling(let hold):
            holds.append(hold)
            tell(String(format: "Side line: no answer for %.1f s", hold), now.addingTimeInterval(-hold))
        }
    }

    /// Takes whole STUN messages (a 20-byte header whose bytes 2–3 say how
    /// much follows) off the front of what TCP delivered.
    static func messages(from buffer: inout Data) -> [Data] {
        var out: [Data] = []
        while buffer.count >= 20 {
            let bytes = [UInt8](buffer.prefix(4))
            let length = 20 + (Int(bytes[2]) << 8 | Int(bytes[3]))
            guard buffer.count >= length else { break }
            out.append(Data(buffer.prefix(length)))
            buffer = Data(buffer.dropFirst(length))
        }
        return out
    }
}
