import Foundation

// The Team list, live (docs/WEB.md §6, docs/PHASE2.md §12 step 7). Linx
// sends the whole list at once and again whenever anything in it changes —
// someone picks up, someone goes Do not disturb, an admin renames an
// extension — so there are no differences to get wrong at this end.
//
// It is the same websocket the web page opens, with the phone's device token
// in the Authorization header instead of a cookie (and so no Origin, exactly
// as /sip works). Nothing is ever sent up it.
//
// It lives only while the app is in front: a phone that is asleep learns
// what it missed from a push, not from a connection held open all night
// (docs/PHASE2.md §7, "Battery").

@MainActor final class TeamLive: NSObject {
    /// A new list arrived.
    var onList: ((TeamList) -> Void)?
    /// Whether the list on the screen is live right now.
    var onConnected: ((Bool) -> Void)?
    /// One of the two counters moved: ask for the badge's own count again.
    var onCountersMoved: (() -> Void)?

    static let path = "/api/v1/team/live"
    /// What the server insists the connection is for.
    static let subprotocol = "linx.team.v1"

    private let server: URL
    private let token: () async -> String?
    private var task: URLSessionWebSocketTask?
    private var session: URLSession?
    private var reopen: Task<Void, Never>?
    private var attempts = 0
    private var stopped = true
    private var counters: (voicemail: Int64, calls: Int64)?

    init(server: URL, token: @escaping () async -> String?) {
        self.server = server
        self.token = token
    }

    func start() {
        guard stopped else { return }
        stopped = false
        open()
    }

    func stop() {
        stopped = true
        reopen?.cancel()
        reopen = nil
        close()
        onConnected?(false)
    }

    private func open() {
        guard !stopped else { return }
        Task { [weak self] in
            guard let self, let token = await self.token(), !self.stopped else {
                self?.tryAgainLater()
                return
            }
            guard var parts = URLComponents(url: self.server.appending(path: Self.path), resolvingAgainstBaseURL: true)
            else { return }
            // Always wss: the app never opens a plain websocket, and never
            // falls back to one.
            parts.scheme = "wss"
            guard let url = parts.url else { return }
            var request = URLRequest(url: url)
            request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
            request.setValue(Self.subprotocol, forHTTPHeaderField: "Sec-WebSocket-Protocol")
            request.timeoutInterval = 20
            let configuration = URLSessionConfiguration.ephemeral
            configuration.httpCookieAcceptPolicy = .never
            configuration.httpShouldSetCookies = false
            let session = URLSession(configuration: configuration, delegate: self, delegateQueue: .main)
            self.session = session
            let task = session.webSocketTask(with: request)
            self.task = task
            task.resume()
            self.receive()
        }
    }

    private func receive() {
        task?.receive { [weak self] result in
            Task { @MainActor in
                guard let self, !self.stopped else { return }
                switch result {
                case .success(let message):
                    switch message {
                    case .string(let text): self.read(Data(text.utf8))
                    case .data(let data): self.read(data)
                    @unknown default: break
                    }
                    self.receive()
                case .failure:
                    self.close()
                    self.onConnected?(false)
                    self.tryAgainLater()
                }
            }
        }
    }

    private func read(_ data: Data) {
        guard let list = try? JSONDecoder.linx.decode(TeamList.self, from: data) else {
            // A message this app can't read changes nothing: the next one
            // replaces it whole.
            return
        }
        attempts = 0
        onConnected?(true)
        onList?(list)
        // The two counters never carry anyone's numbers — they only move.
        // The badges then ask Linx for their own counts, so one person's
        // count can never reach another's phone.
        let moved = (list.voicemail ?? 0, list.calls ?? 0)
        if let counters, counters != moved { onCountersMoved?() }
        counters = moved
    }

    private func close() {
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        session?.invalidateAndCancel()
        session = nil
    }

    /// Waits longer each time, up to half a minute: a phone on a train must
    /// not spend its battery knocking on a door that isn't there.
    private func tryAgainLater() {
        guard !stopped, reopen == nil else { return }
        attempts += 1
        let wait = min(30, Int(pow(2.0, Double(min(attempts, 5)))))
        reopen = Task { [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self, !self.stopped else { return }
            self.reopen = nil
            self.open()
        }
    }
}

extension TeamLive: URLSessionWebSocketDelegate {
    nonisolated func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocolName: String?
    ) {
        let agreed = protocolName
        Task { @MainActor in
            guard agreed == Self.subprotocol else {
                self.close()
                self.tryAgainLater()
                return
            }
            self.onConnected?(true)
        }
    }

    nonisolated func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask,
        didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?
    ) {
        Task { @MainActor in
            guard !self.stopped else { return }
            self.close()
            self.onConnected?(false)
            self.tryAgainLater()
        }
    }
}
