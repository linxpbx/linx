import SwiftUI
import UIKit

/// Call details: why this call sounds the way it does.
///
/// A call that connects and carries no sound looks, from the call screen,
/// exactly like a call that is working — the name, the timer and "Encrypted"
/// are all there. This is the screen that says where the sound stops: whether
/// a way through was found at all, whether it goes straight there or through
/// Linx's relay, whether anything is coming in, whether anything is going
/// out, and what Linx's relay said if it wouldn't have this phone.
///
/// It is reached by tapping the "Encrypted · …" line during a call, and
/// **Copy these details** puts the lot on the clipboard, which is what makes a
/// silent call on somebody else's network something they can report rather
/// than something that has to be guessed at.
struct CallDetailsView: View {
    let call: PhoneModel.Call
    let diagnostics: CallDiagnostics
    let dismiss: () -> Void

    @State private var copied = false

    var body: some View {
        NavigationStack {
            List {
                Section {
                    LabeledContent("Getting through", value: routeWords)
                    LabeledContent("Coming in", value: Self.size(call.connection?.audioBytesIn))
                    LabeledContent("Going out", value: Self.size(call.connection?.audioBytesOut))
                    if let rtt = call.connection?.roundTripMs {
                        LabeledContent("Round trip", value: "\(rtt) ms")
                    }
                    LabeledContent("Connection", value: diagnostics.ice)
                    if let picture = diagnostics.picture, call.video.mine {
                        LabeledContent("Your picture", value: picture)
                    }
                } header: {
                    Text("The sound")
                } footer: {
                    Text(helpWords)
                }

                if diagnostics.steps.count > 1 {
                    Section {
                        ForEach(Array(diagnostics.steps.dropFirst().enumerated()), id: \.offset) { _, step in
                            LabeledContent(step.what, value: Self.after(step.at, diagnostics.steps[0].at))
                        }
                    } header: {
                        Text("How long it took")
                    } footer: {
                        Text(
                            diagnostics.movedToRelay
                                ? "No sound was getting through the first route, so this phone moved the call onto Linx's relay."
                                : "Counted from the moment you started the call.")
                    }
                }

                Section("Routes this phone found") {
                    if diagnostics.found.isEmpty {
                        Text("None yet").foregroundStyle(LinxColor.textMuted)
                    } else {
                        ForEach(diagnostics.found, id: \.self) { found in Text(found) }
                    }
                }

                Section {
                    ForEach(diagnostics.relayURLs, id: \.self) { url in
                        Text(url).font(.footnote.monospaced())
                    }
                    if let expires = diagnostics.relayExpiresAt {
                        LabeledContent("Credentials good for", value: Self.until(expires))
                    }
                    LabeledContent(
                        "Looked up by this phone",
                        value: diagnostics.relayAddresses.isEmpty
                            ? "no" : diagnostics.relayAddresses.joined(separator: ", "))
                    ForEach(diagnostics.relayTrouble, id: \.url) { trouble in
                        Text("\(trouble.url) — \(trouble.code) \(trouble.said)")
                            .font(.footnote)
                            .foregroundStyle(LinxColor.end)
                    }
                } header: {
                    Text("Linx's relay")
                } footer: {
                    Text(
                        "On a mobile network the relay is usually the only way the sound can go. A relay that can be reached over one of these addresses and not the other is perfectly normal."
                    )
                }

                Section {
                    Button(copied ? "Copied" : "Copy these details") {
                        UIPasteboard.general.string = Self.words(call: call, diagnostics: diagnostics)
                        copied = true
                    }
                    .foregroundStyle(LinxColor.accent)
                }
            }
            .navigationTitle("Call details")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done", action: dismiss)
                }
            }
        }
    }

    private var routeWords: String {
        guard let connection = call.connection else { return "No way through yet" }
        guard connection.route == .relayed else { return "Straight there" }
        guard let how = connection.relayProtocol else { return "Through Linx's relay" }
        return "Through Linx's relay (\(how))"
    }

    private var helpWords: String {
        if call.connection == nil {
            return
                "The call is connected but no route for the sound has been agreed yet. If it stays this way, the phone couldn't reach Linx's relay — the section below says what it answered."
        }
        if (call.connection?.audioBytesIn ?? 0) == 0 {
            return
                "A route was found but nothing is arriving on it. Linx's relay can be reached and the other end can't be, which is a thing to show whoever looks after this Linx server."
        }
        return "Every Linx call is encrypted end to end. These numbers only say which way the sound is flowing."
    }

    /// Bytes, as a person reads them.
    static func size(_ bytes: Int?) -> String {
        guard let bytes, bytes > 0 else { return "nothing" }
        if bytes < 1024 { return "\(bytes) bytes" }
        if bytes < 1024 * 1024 { return String(format: "%.0f KB", Double(bytes) / 1024) }
        return String(format: "%.1f MB", Double(bytes) / (1024 * 1024))
    }

    /// "+0.4 s": how long after the call began a step came.
    static func after(_ when: Date, _ start: Date) -> String {
        String(format: "+%.1f s", max(0, when.timeIntervalSince(start)))
    }

    static func until(_ when: Date) -> String {
        let minutes = Int(when.timeIntervalSinceNow / 60)
        if minutes < 0 { return "expired" }
        if minutes < 1 { return "under a minute" }
        return "\(minutes) min"
    }

    /// The whole thing in one block of text, for pasting into a message to
    /// whoever looks after the server.
    static func words(call: PhoneModel.Call, diagnostics: CallDiagnostics) -> String {
        var lines = [
            "Linx call details",
            "to: \(call.peer.number)",
            "connection: \(diagnostics.ice)",
        ]
        if let connection = call.connection {
            lines.append("route: \(connection.route.rawValue)\(connection.relayProtocol.map { " (\($0))" } ?? "")")
            lines.append("sound in: \(connection.audioBytesIn) bytes, out: \(connection.audioBytesOut) bytes")
            if let rtt = connection.roundTripMs { lines.append("round trip: \(rtt) ms") }
        } else {
            lines.append("route: none agreed")
        }
        if let picture = diagnostics.picture { lines.append("picture: \(picture)") }
        lines.append("found: \(diagnostics.found.isEmpty ? "none" : diagnostics.found.joined(separator: ", "))")
        lines.append("relay: \(diagnostics.relayURLs.joined(separator: " "))")
        if let expires = diagnostics.relayExpiresAt {
            lines.append("relay credentials: \(until(expires))")
        }
        lines.append(
            "relay looked up by this phone: \(diagnostics.relayAddresses.isEmpty ? "no" : diagnostics.relayAddresses.joined(separator: ", "))"
        )
        for trouble in diagnostics.relayTrouble {
            lines.append("relay said: \(trouble.url) \(trouble.code) \(trouble.said)")
        }
        if let start = diagnostics.steps.first?.at {
            for step in diagnostics.steps.dropFirst() {
                lines.append("\(after(step.at, start)) \(step.what)")
            }
        }
        if diagnostics.movedToRelay { lines.append("moved onto the relay: yes") }
        return lines.joined(separator: "\n")
    }
}

#Preview("Call details") {
    CallDetailsView(
        call: PhoneModel.Call(
            peer: SIPPeer(name: "Omar Khalil", number: "1024"), incoming: false, phase: .active,
            answeredAt: Date(timeIntervalSinceNow: -30),
            connection: MediaConnection(
                route: .relayed, roundTripMs: 64, relayProtocol: "tls", audioBytesIn: 0,
                audioBytesOut: 48_000)),
        diagnostics: CallDiagnostics(
            found: ["this network", "the internet"],
            relayTrouble: [
                CallDiagnostics.RelayTrouble(
                    url: "turn:turn.example.com:443?transport=udp", code: 701,
                    said: "TURN allocate request timed out")
            ],
            ice: "finding a way through",
            relayURLs: ["turn:turn.example.com:443?transport=udp", "turns:turn.example.com:443?transport=tcp"],
            relayExpiresAt: Date(timeIntervalSinceNow: 2400)), dismiss: {})
}
