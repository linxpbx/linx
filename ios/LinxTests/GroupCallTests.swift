import Foundation
import Testing

@testable import Linx

// Group calls (ADR-088): LiveKit's messages as the app writes and reads
// them, and what the phone does when a call becomes one.

@MainActor
struct LiveKitWireTests {
    @Test("a request is the exact bytes LiveKit's protocol says")
    func requestBytes() {
        // SignalRequest { mute = 5: MuteTrackRequest { sid = 1: "TR_a", muted = 2: true } }
        #expect(
            [UInt8](LiveKitRequest.mute(sid: "TR_a", muted: true).data)
                == [0x2A, 0x08, 0x0A, 0x04, 0x54, 0x52, 0x5F, 0x61, 0x10, 0x01])
        // SignalRequest { leave = 8: LeaveRequest { reason = 2: CLIENT_INITIATED } }
        #expect([UInt8](LiveKitRequest.leave.data) == [0x42, 0x02, 0x10, 0x01])
        // SignalRequest { ping_req = 16: Ping { timestamp = 1: 300 } }: a field
        // number above 15 takes two bytes of key.
        #expect([UInt8](LiveKitRequest.ping(timestampMs: 300).data) == [0x82, 0x01, 0x03, 0x08, 0xAC, 0x02])
    }

    @Test("an answer says which offer it answers")
    func answerCarriesTheOffersID() throws {
        let data = LiveKitRequest.answer(LiveKitDescription(type: "answer", sdp: "v=0", id: 7)).data
        let outer = try protoFields(data)
        #expect(outer.first?.field == 2)
        let inner = try protoFields(outer.first?.value.data ?? Data())
        #expect(inner.map(\.field) == [1, 2, 3])
        #expect(inner[2].value.number == 7)
    }

    @Test("the join, a person arriving and leaving, a track published")
    func responses() throws {
        func participant(_ w: inout ProtoWriter, sid: String, identity: String, name: String, state: UInt64 = 0) {
            w.string(1, sid)
            w.string(2, identity)
            w.uint(3, state)
            w.message(4) {
                $0.string(1, "TR_cam")
                $0.uint(2, 1)
                $0.uint(9, 1)
            }
            w.string(9, name)
        }
        var join = ProtoWriter()
        join.message(1) {
            $0.message(2) { participant(&$0, sid: "PA_me", identity: "dev-1", name: "Sara") }
            $0.message(3) { participant(&$0, sid: "PA_om", identity: "dev-2", name: "Omar") }
            $0.bool(6, true)
            $0.uint(11, 10)
        }
        let joined = try LiveKitResponse(join.data)
        guard case .join(let me, let others, let primary, let interval) = joined else {
            Issue.record("not a join: \(joined)")
            return
        }
        #expect(me.sid == "PA_me" && me.name == "Sara")
        #expect(others.map(\.identity) == ["dev-2"])
        var camera = LiveKitTrack()
        camera.sid = "TR_cam"
        camera.type = .video
        camera.source = .camera
        #expect(others.first?.tracks.first == camera)
        #expect(primary && interval == 10)

        var update = ProtoWriter()
        update.message(5) {
            $0.message(1) { participant(&$0, sid: "PA_om", identity: "dev-2", name: "Omar", state: 3) }
        }
        guard case .update(let people) = try LiveKitResponse(update.data) else {
            Issue.record("not an update")
            return
        }
        #expect(people.first?.state == LiveKitParticipant.disconnected)

        var published = ProtoWriter()
        published.message(6) {
            $0.string(1, GroupCall.microphoneID)
            $0.message(2) { $0.string(1, "TR_mic") }
        }
        var mic = LiveKitTrack()
        mic.sid = "TR_mic"
        #expect(try LiveKitResponse(published.data) == .trackPublished(cid: GroupCall.microphoneID, track: mic))
    }

    @Test("a route, both ways, says which connection it is for")
    func trickle() throws {
        let sent = LiveKitRequest.trickle(LiveKitTrickle(candidateInit: "{}", target: .subscriber)).data
        var back = ProtoWriter()
        back.bytes(4, try protoFields(sent).first?.value.data ?? Data())
        #expect(try LiveKitResponse(back.data) == .trickle(LiveKitTrickle(candidateInit: "{}", target: .subscriber)))
    }

    @Test("bytes cut short are refused, not read past their end")
    func truncated() {
        #expect(throws: ProtoTrouble.self) { try protoFields(Data([0x0A, 0x05, 0x41])) }
        #expect(throws: ProtoTrouble.self) { try protoFields(Data([0x80])) }
    }

    @Test("the websocket goes to this phone's own Linx, at the path the server named")
    func socketAddress() throws {
        let join = GroupJoin(room: "call-x", token: "tok", url: "/livekit/")
        let socket = try #require(GroupCall.socket(server: URL(string: "https://pbx.example.com:8443")!, join: join))
        let live = try #require(socket as? LiveKitSocket)
        let url = try #require(Mirror(reflecting: live).descendant("url") as? URL)
        #expect(url.scheme == "wss" && url.host == "pbx.example.com" && url.port == 8443 && url.path == "/livekit/rtc")
        #expect(url.query?.contains("access_token=tok") == true)
    }
}

@MainActor
struct GroupCallPhoneTests {
    private static func line() -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@sip.example.com",
                websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600),
                    maxBitrateBps: 1_280_000)),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    private func inACall() async throws -> (PhoneModel, FakeTransport, FakeMedia, FakeSystemCalls) {
        let transport = FakeTransport()
        let media = FakeMedia()
        let system = FakeSystemCalls()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media }, calls: system)
        await phone.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: try #require(transport.last("REGISTER"))))
        phone.callNumber("1024", name: "Omar Khalil")
        #expect(await eventually { transport.last("INVITE") != nil })
        var ok = SIPMessage.response(200, "OK", to: try #require(transport.last("INVITE"))).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        return (phone, transport, media, system)
    }

    @Test("the other side added someone: this phone moves into the group call")
    func movedByTheOtherSide() async throws {
        let (phone, _, media, _) = try await inACall()
        media.onGroupMove?(GroupJoin(room: "call-x", token: "t", url: "/livekit/"))
        #expect(media.groupsJoined.map(\.room) == ["call-x"])
        #expect(phone.group != nil)
        #expect(phone.call?.speaker == true)
    }

    @Test("the phone call underneath ends: the group call carries on")
    func phoneCallEndsGroupStays() async throws {
        let (phone, transport, media, system) = try await inACall()
        media.onGroupMove?(GroupJoin(room: "call-x", token: "t", url: "/livekit/"))
        // Omar leaves: Asterisk hangs up this phone's leg too.
        var bye = SIPMessage.request("BYE", "sip:d_Ab12Cd34@pbx.example.com")
        bye.add("Via", "SIP/2.0/WSS pbx.example.com;branch=z9hG4bK-bye")
        bye.add("From", "<sip:1024@sip.example.com>;tag=theirs")
        bye.add("To", try #require(transport.last("INVITE")?.first("From")))
        bye.set("Call-ID", try #require(transport.last("INVITE")?.callID))
        bye.add("CSeq", "9 BYE")
        transport.asterisk(bye)
        #expect(await eventually { transport.sent.contains { $0.status == 200 && $0.cseq?.method == "BYE" } })
        #expect(phone.call != nil)
        #expect(phone.group != nil)
        #expect(!media.stopped)
        #expect(system.ended.isEmpty)

        // Then the person leaves: everything ends, the group call with it.
        phone.hangUp()
        #expect(await eventually { phone.call == nil })
        #expect(media.stopped)
        #expect(phone.group == nil)
    }

    @Test("a group call keeps the screen awake while there is a picture in it")
    func theScreenStaysAwakeInAGroupCall() async throws {
        let (phone, _, media, _) = try await inACall()
        media.onGroupMove?(GroupJoin(room: "call-x", token: "t", url: "/livekit/"))
        #expect(phone.group != nil)
        // Nobody's camera is on: a group call by voice is left to iOS, as a
        // phone call is.
        media.pretendGroup([Self.who("me", me: true, video: false), Self.who("raisa", video: false)])
        #expect(!phone.screenIsHeldAwake)
        // Someone else turns their camera on. This phone is watching them,
        // so the screen stays on even with its own camera off — the rule
        // reads the room, not this phone's own link, which knows nothing
        // about a group call's pictures (owner, 2026-10-20).
        media.pretendGroup([Self.who("me", me: true, video: false), Self.who("raisa", video: true)])
        #expect(phone.screenIsHeldAwake)
        media.pretendGroup([Self.who("me", me: true, video: true), Self.who("raisa", video: false)])
        #expect(phone.screenIsHeldAwake)
        // Every camera off again, and the screen is iOS's once more.
        media.pretendGroup([Self.who("me", me: true, video: false), Self.who("raisa", video: false)])
        #expect(!phone.screenIsHeldAwake)
        phone.hangUp()
        #expect(await eventually { phone.call == nil })
        #expect(!phone.screenIsHeldAwake)
    }

    @Test("two people left in a group call swap pictures as a call between two does")
    func swappingInAGroupCallOfTwo() async throws {
        let (phone, _, media, _) = try await inACall()
        media.onGroupMove?(GroupJoin(room: "call-x", token: "t", url: "/livekit/"))
        // Only one picture in the room: there is nothing to swap.
        media.pretendGroup([Self.who("me", me: true, video: true), Self.who("raisa", video: false)])
        #expect(!phone.canSwapPictures)
        phone.swapPictures()
        #expect(!phone.myPictureIsBig)
        // Both cameras on: a tap puts this phone's own picture on the big
        // screen, and another puts it back.
        media.pretendGroup([Self.who("me", me: true, video: true), Self.who("raisa", video: true)])
        #expect(phone.canSwapPictures)
        phone.swapPictures()
        #expect(phone.myPictureIsBig)
        phone.swapPictures()
        #expect(!phone.myPictureIsBig)
    }

    @Test("every tile fits the screen, so a group call never scrolls")
    func theGridAlwaysFits() {
        let space = CGSize(width: 390, height: 500)
        let spacing: CGFloat = 8
        for people in 1...8 {
            let rows = GroupGrid.rows(people: people)
            let columns = GroupGrid.columns(people: people)
            #expect(rows * columns >= people)
            #expect(rows * columns < people + columns, "no empty row")
            let tile = GroupGrid.tile(people: people, in: space, spacing: spacing)
            let high = tile.height * CGFloat(rows) + spacing * CGFloat(rows - 1)
            let wide = tile.width * CGFloat(columns) + spacing * CGFloat(columns - 1)
            #expect(abs(high - space.height) < 0.5)
            #expect(abs(wide - space.width) < 0.5)
        }
        // Two people are laid out as a call between two instead, so the grid
        // is never asked for one column of two tall tiles — which is what
        // had to be scrolled (owner, 2026-10-20).
        #expect(GroupGrid.columns(people: 2) == 2)
    }

    private static func who(_ name: String, me: Bool = false, video: Bool) -> GroupPerson {
        GroupPerson(id: name, name: name, me: me, video: video, sound: true, speaking: false)
    }

    @Test("Add person is there only on a call between two Linx devices")
    func addPersonOnlyBetweenLinxDevices() async throws {
        let (phone, _, media, _) = try await inACall()
        #expect(!phone.canAddPeople)
        media.pretendTheirVideo(false)
        #expect(await eventually { phone.call?.video.available == true })
        #expect(phone.canAddPeople)
    }

    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }
}
