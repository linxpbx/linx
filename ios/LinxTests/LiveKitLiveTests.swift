import CryptoKit
import Foundation
import Testing
@preconcurrency import WebRTC

@testable import Linx

// The app's group call against a real LiveKit server (ADR-088): the one
// thing the wire tests can't prove is that LiveKit agrees with how the app
// speaks to it. Runs only when LINX_LIVEKIT_URL names a server started with
// `livekit-server --dev` (its key devkey/secret), e.g.
//
//   docker run --rm -d -p 7880:7880 livekit/livekit-server:<pinned> --dev --bind 0.0.0.0
//   TEST_RUNNER_LINX_LIVEKIT_URL=http://127.0.0.1:7880 xcodebuild test … -only-testing:LinxTests/LiveKitLiveTests
//
// Media doesn't flow (the server's addresses are inside Docker); the
// signalling is what is checked: joining, publishing, and being offered
// each other's tracks.

@MainActor
struct LiveKitLiveTests {
    nonisolated static var server: URL? {
        ProcessInfo.processInfo.environment["LINX_LIVEKIT_URL"].flatMap(URL.init(string:))
    }

    /// A dev token, as the control plane would mint one (HS256).
    static func token(identity: String, name: String, room: String) -> String {
        func b64(_ data: Data) -> String {
            data.base64EncodedString().replacingOccurrences(of: "+", with: "-")
                .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
        }
        let now = Int(Date().timeIntervalSince1970)
        let header = #"{"alg":"HS256","typ":"JWT"}"#
        let claims =
            #"{"iss":"devkey","sub":"\#(identity)","name":"\#(name)","nbf":\#(now - 10),"exp":\#(now + 600),"#
            + #""video":{"room":"\#(room)","roomJoin":true,"canPublish":true,"canSubscribe":true}}"#
        let signing = b64(Data(header.utf8)) + "." + b64(Data(claims.utf8))
        let key = SymmetricKey(data: Data("secret".utf8))
        let mac = HMAC<SHA256>.authenticationCode(for: Data(signing.utf8), using: key)
        return signing + "." + b64(Data(mac))
    }

    private func join(_ identity: String, _ name: String, room: String, server: URL) throws -> GroupCall {
        // As the app has it: WebRTC never starts the sound itself, CallKit
        // hands it over (WebRTCMedia's manual audio). Here nobody does.
        RTCAudioSession.sharedInstance().useManualAudio = true
        RTCAudioSession.sharedInstance().isAudioEnabled = false
        let join = GroupJoin(room: room, token: Self.token(identity: identity, name: name, room: room), url: "/")
        let socket = try #require(GroupCall.socket(server: server, join: join))
        let call = GroupCall(
            socket: socket, configuration: RTCConfiguration(), factory: WebRTCMedia.factory, camera: false)
        call.start()
        return call
    }

    @Test("two phones join a room, publish, and each is offered the other", .enabled(if: server != nil))
    func twoPhones() async throws {
        let server = try #require(Self.server)
        let room = "linx-test-\(UUID().uuidString.prefix(8))"
        let sara = try join("dev-sara", "Sara", room: room, server: server)
        #expect(await eventually { sara.state.phase == .joined })
        let omar = try join("dev-omar", "Omar", room: room, server: server)
        #expect(await eventually { omar.state.phase == .joined })
        // Each sees the other, by the name in their token.
        #expect(await eventually { sara.state.people.contains { $0.name == "Omar" } })
        #expect(await eventually { omar.state.people.contains { $0.name == "Sara" } })
        // Omar's microphone was published and offered to Sara: LiveKit
        // listed it on him, which it does only once his offer was answered.
        #expect(await eventually { sara.state.people.first { $0.name == "Omar" }?.sound == true })
        omar.leave()
        #expect(await eventually { !sara.state.people.contains { $0.name == "Omar" } })
        sara.leave()
    }

    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<300 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(50))
        }
        return check()
    }
}
