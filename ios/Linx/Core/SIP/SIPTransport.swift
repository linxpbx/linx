import Foundation

// How SIP gets to Linx: one websocket to GET /sip (RFC 7118), with this
// phone's device token in the Authorization header and the "sip" subprotocol
// the relay insists on (internal/siprelay). Always wss, never plain — the app
// never turns certificate checking off.

/// What the user agent needs of a way to talk to Linx. The real one is a
/// websocket; the tests use one that answers from a script.
@MainActor protocol SIPTransport: AnyObject {
    var onOpen: (() -> Void)? { get set }
    var onText: ((String) -> Void)? { get set }
    /// Why the connection ended, in words, or nil when the app closed it.
    var onClose: ((String?) -> Void)? { get set }

    /// The line also carries this phone's call links (`LineLink`): Linx
    /// said so as it opened (`internal/siprelay`'s SideHeader).
    var carriesCallLinks: Bool { get }
    /// A call link's message may carry a SIP message with it
    /// (`LineLink.frame(_:msg:sip:)`): Linx said so as it opened.
    var carriesSIPInLinks: Bool { get }

    func start()
    func send(_ text: String)
    func stop()
    /// The bench's black hole on this line: for this long it carries
    /// nothing either way and closes nothing (`Bench.stallLineAtDial`).
    /// Nothing outside a bench run ever asks for it.
    func stallForBench(_ seconds: Int)
}

extension SIPTransport {
    var carriesCallLinks: Bool { false }
    var carriesSIPInLinks: Bool { false }
    func stallForBench(_ seconds: Int) {}
}

/// The websocket to Linx's /sip relay.
@MainActor final class WebSocketTransport: NSObject, SIPTransport {
    var onOpen: (() -> Void)?
    var onText: ((String) -> Void)?
    var onClose: ((String?) -> Void)?

    private let url: URL
    private let token: String
    private var task: URLSessionWebSocketTask?
    private var session: URLSession?
    private var keepAlive: Task<Void, Never>?
    private var closed = false
    private(set) var carriesCallLinks = false
    private(set) var carriesSIPInLinks = false

    /// Every 30 seconds the app sends a blank frame: enough to keep a
    /// phone's connection open through a mobile network's idle timers, and
    /// nothing more. **Nothing comes back** — Linx's relay drops a
    /// keep-alive rather than passing it to Asterisk (`internal/siprelay`'s
    /// `toAsterisk`), so this says nothing about whether the line still
    /// carries. What answers that question is `LineWatch`, which asks with
    /// a REGISTER (ADR-097).
    private static let keepAliveEvery: Duration = .seconds(30)

    init(url: URL, token: String) {
        self.url = url
        self.token = token
    }

    func start() {
        closed = false
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
        receive()
    }

    func send(_ text: String) {
        #if DEBUG
            if stalled { return }
        #endif
        task?.send(.string(text)) { [weak self] error in
            guard let error else { return }
            Task { @MainActor in self?.finish((error as NSError).localizedDescription) }
        }
    }

    func stop() {
        closed = true
        keepAlive?.cancel()
        keepAlive = nil
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        session?.invalidateAndCancel()
        session = nil
    }

    private func receive() {
        task?.receive { [weak self] result in
            Task { @MainActor in
                guard let self, !self.closed else { return }
                switch result {
                case .success(let message):
                    #if DEBUG
                        if self.stalled {
                            self.receive()
                            return
                        }
                    #endif
                    switch message {
                    case .string(let text): self.onText?(text)
                    case .data(let data): self.onText?(String(decoding: data, as: UTF8.self))
                    @unknown default: break
                    }
                    self.receive()
                case .failure(let error):
                    self.finish((error as NSError).localizedDescription)
                }
            }
        }
    }

    private func finish(_ why: String?) {
        guard !closed else { return }
        closed = true
        keepAlive?.cancel()
        keepAlive = nil
        task = nil
        session?.invalidateAndCancel()
        session = nil
        onClose?(why)
    }

    private func startKeepAlive() {
        keepAlive?.cancel()
        keepAlive = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.keepAliveEvery)
                guard !Task.isCancelled else { return }
                self?.send("\r\n\r\n")
            }
        }
    }

    #if DEBUG
        /// The bench's black hole (`Bench.stallLineAtDial`): what this phone
        /// sends is dropped and what arrives is thrown away, the socket left
        /// open and nothing told, exactly as a stalled mobile connection
        /// behaves. It is this one connection's; the line the phone opens
        /// after it carries normally.
        private var stalled = false

        func stallForBench(_ seconds: Int) {
            guard seconds > 0, !stalled else { return }
            stalled = true
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(seconds))
                self?.stalled = false
            }
        }
    #endif

    /// How a close Linx's relay meant begins (`internal/siprelay`'s
    /// policy-violation closes: signed out, not your line, ...).
    static let closedByLinx = "Linx closed this phone's line: "

    /// Linx's word that this line carries call links too.
    nonisolated static let callLinksHeader = "Linx-Call-Links"
    /// And that a call link's message may carry a SIP message.
    nonisolated static let sipInLinksHeader = "Linx-Call-Links-SIP"

    /// The subprotocol RFC 7118 gives SIP, which the relay requires.
    static let subprotocol = "sip"
}

extension WebSocketTransport: URLSessionWebSocketDelegate {
    nonisolated func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask,
        didOpenWithProtocol protocolName: String?
    ) {
        let agreed = protocolName
        let response = webSocketTask.response as? HTTPURLResponse
        let links = response?.value(forHTTPHeaderField: Self.callLinksHeader) == "1"
        let sip = links && response?.value(forHTTPHeaderField: Self.sipInLinksHeader) == "1"
        Task { @MainActor in
            guard agreed == Self.subprotocol else {
                self.finish("Linx didn't agree to speak SIP on that connection.")
                return
            }
            self.carriesCallLinks = links
            self.carriesSIPInLinks = sip
            self.startKeepAlive()
            self.onOpen?()
        }
    }

    nonisolated func urlSession(
        _ session: URLSession, webSocketTask: URLSessionWebSocketTask,
        didCloseWith closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?
    ) {
        let why = reason.flatMap { $0.isEmpty ? nil : String(decoding: $0, as: UTF8.self) }
        let code = closeCode
        Task { @MainActor in
            // Linx closed the line on purpose (the phone was stopped, its
            // person signed out or disabled): said so, so a call isn't held
            // for a line that won't come back.
            if code == .policyViolation {
                self.finish(Self.closedByLinx + (why ?? ""))
                return
            }
            self.finish(why ?? "The connection to Linx closed (\(code.rawValue)).")
        }
    }
}
