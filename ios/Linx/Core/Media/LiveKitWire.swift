import Foundation

// LiveKit's signalling, as the app speaks it (ADR-088, docs/VIDEO_CALLS.md
// §4.3): protobuf messages on a websocket. Only the handful of messages a
// group call needs, read and written by hand from LiveKit's own
// livekit_rtc.proto and livekit_models.proto — field numbers are the
// protocol and never change meaning — so the app needs neither LiveKit's
// app kit (a second copy of WebRTC, fighting the first for the audio
// session CallKit owns) nor a protobuf library.

// MARK: - The wire format

/// One field as protobuf encodes it.
enum ProtoValue: Equatable, Sendable {
    case varint(UInt64)
    case bytes(Data)
    case fixed64(UInt64)
    case fixed32(UInt32)
}

enum ProtoTrouble: Error { case truncated, badWireType }

/// Writes protobuf fields in order. Defaults (0, "", false) are left out,
/// as proto3 does.
struct ProtoWriter {
    private(set) var data = Data()

    mutating func varint(_ value: UInt64) {
        var v = value
        while v >= 0x80 {
            data.append(UInt8(v & 0x7F) | 0x80)
            v >>= 7
        }
        data.append(UInt8(v))
    }

    private mutating func key(_ field: Int, _ wire: UInt64) { varint(UInt64(field) << 3 | wire) }

    mutating func uint(_ field: Int, _ value: UInt64) {
        guard value != 0 else { return }
        key(field, 0)
        varint(value)
    }

    mutating func int(_ field: Int, _ value: Int64) { uint(field, UInt64(bitPattern: value)) }

    mutating func bool(_ field: Int, _ value: Bool) { uint(field, value ? 1 : 0) }

    mutating func string(_ field: Int, _ value: String) {
        guard !value.isEmpty else { return }
        bytes(field, Data(value.utf8))
    }

    mutating func bytes(_ field: Int, _ value: Data) {
        key(field, 2)
        varint(UInt64(value.count))
        data.append(value)
    }

    /// A message inside this one; an empty one is still written, because
    /// which field of a `oneof` is present is what it says.
    mutating func message(_ field: Int, _ build: (inout ProtoWriter) -> Void) {
        var inner = ProtoWriter()
        build(&inner)
        bytes(field, inner.data)
    }
}

/// Reads every field of a protobuf message, in order.
func protoFields(_ data: Data) throws -> [(field: Int, value: ProtoValue)] {
    var out: [(Int, ProtoValue)] = []
    var i = data.startIndex
    func varint() throws -> UInt64 {
        var result: UInt64 = 0
        var shift: UInt64 = 0
        while true {
            guard i < data.endIndex, shift < 64 else { throw ProtoTrouble.truncated }
            let b = data[i]
            i += 1
            result |= UInt64(b & 0x7F) << shift
            if b & 0x80 == 0 { return result }
            shift += 7
        }
    }
    while i < data.endIndex {
        let key = try varint()
        let field = Int(key >> 3)
        switch key & 7 {
        case 0:
            out.append((field, .varint(try varint())))
        case 1:
            guard data.endIndex - i >= 8 else { throw ProtoTrouble.truncated }
            var v: UInt64 = 0
            for n in 0..<8 { v |= UInt64(data[i + n]) << (8 * UInt64(n)) }
            i += 8
            out.append((field, .fixed64(v)))
        case 2:
            let length = Int(try varint())
            guard length >= 0, data.endIndex - i >= length else { throw ProtoTrouble.truncated }
            out.append((field, .bytes(Data(data[i..<(i + length)]))))
            i += length
        case 5:
            guard data.endIndex - i >= 4 else { throw ProtoTrouble.truncated }
            var v: UInt32 = 0
            for n in 0..<4 { v |= UInt32(data[i + n]) << (8 * UInt32(n)) }
            i += 4
            out.append((field, .fixed32(v)))
        default:
            throw ProtoTrouble.badWireType
        }
    }
    return out
}

extension ProtoValue {
    var number: UInt64 {
        if case .varint(let v) = self { return v }
        return 0
    }
    var flag: Bool { number != 0 }
    var data: Data {
        if case .bytes(let d) = self { return d }
        return Data()
    }
    var text: String { String(decoding: data, as: UTF8.self) }
}

// MARK: - LiveKit's messages

enum LiveKitTrackType: UInt64, Sendable { case audio = 0, video = 1, data = 2 }
enum LiveKitTrackSource: UInt64, Sendable {
    case unknown = 0, camera = 1, microphone = 2, screenShare = 3, screenShareAudio = 4
}
/// SignalTarget: which of the two connections a route is for.
enum LiveKitTarget: UInt64, Sendable { case publisher = 0, subscriber = 1 }

struct LiveKitTrack: Equatable, Sendable {
    var sid = ""
    var type = LiveKitTrackType.audio
    var muted = false
    var source = LiveKitTrackSource.unknown

    init() {}

    init(_ data: Data) throws {
        for (field, value) in try protoFields(data) {
            switch field {
            case 1: sid = value.text
            case 2: type = LiveKitTrackType(rawValue: value.number) ?? .data
            case 4: muted = value.flag
            case 9: source = LiveKitTrackSource(rawValue: value.number) ?? .unknown
            default: break
            }
        }
    }
}

struct LiveKitParticipant: Equatable, Sendable {
    /// JOINING 0, JOINED 1, ACTIVE 2, DISCONNECTED 3.
    static let disconnected: UInt64 = 3

    var sid = ""
    var identity = ""
    var name = ""
    var state: UInt64 = 0
    var tracks: [LiveKitTrack] = []

    init() {}

    init(_ data: Data) throws {
        for (field, value) in try protoFields(data) {
            switch field {
            case 1: sid = value.text
            case 2: identity = value.text
            case 3: state = value.number
            case 4: tracks.append(try LiveKitTrack(value.data))
            case 9: name = value.text
            default: break
            }
        }
    }
}

struct LiveKitDescription: Equatable, Sendable {
    var type = ""
    var sdp = ""
    /// The server numbers its offers; an answer says which it answers.
    var id: UInt64 = 0

    init(type: String, sdp: String, id: UInt64 = 0) {
        self.type = type
        self.sdp = sdp
        self.id = id
    }

    init(_ data: Data) throws {
        for (field, value) in try protoFields(data) {
            switch field {
            case 1: type = value.text
            case 2: sdp = value.text
            case 3: id = value.number
            default: break
            }
        }
    }

    func write(_ w: inout ProtoWriter) {
        w.string(1, type)
        w.string(2, sdp)
        w.uint(3, id)
    }
}

struct LiveKitTrickle: Equatable, Sendable {
    /// The route as WebRTC's JSON (`RTCIceCandidateInit`).
    var candidateInit = ""
    var target = LiveKitTarget.publisher

    init(candidateInit: String, target: LiveKitTarget) {
        self.candidateInit = candidateInit
        self.target = target
    }

    init(_ data: Data) throws {
        for (field, value) in try protoFields(data) {
            switch field {
            case 1: candidateInit = value.text
            case 2: target = LiveKitTarget(rawValue: value.number) ?? .publisher
            default: break
            }
        }
    }
}

/// What the server says (SignalResponse), as far as a group call cares.
enum LiveKitResponse: Equatable, Sendable {
    case join(me: LiveKitParticipant, others: [LiveKitParticipant], subscriberPrimary: Bool, pingInterval: Int)
    case answer(LiveKitDescription)
    case offer(LiveKitDescription)
    case trickle(LiveKitTrickle)
    case update([LiveKitParticipant])
    case trackPublished(cid: String, track: LiveKitTrack)
    case trackUnpublished(sid: String)
    case leave
    case speakers([String])
    case refreshToken(String)
    case pong
    case other

    init(_ data: Data) throws {
        guard let (field, value) = try protoFields(data).first else {
            self = .other
            return
        }
        switch field {
        case 1:
            var me = LiveKitParticipant()
            var others: [LiveKitParticipant] = []
            var primary = false
            var interval = 0
            for (f, v) in try protoFields(value.data) {
                switch f {
                case 2: me = try LiveKitParticipant(v.data)
                case 3: others.append(try LiveKitParticipant(v.data))
                case 6: primary = v.flag
                case 11: interval = Int(Int32(truncatingIfNeeded: v.number))
                default: break
                }
            }
            self = .join(me: me, others: others, subscriberPrimary: primary, pingInterval: interval)
        case 2: self = .answer(try LiveKitDescription(value.data))
        case 3: self = .offer(try LiveKitDescription(value.data))
        case 4: self = .trickle(try LiveKitTrickle(value.data))
        case 5:
            self = .update(
                try protoFields(value.data).filter { $0.field == 1 }.map { try LiveKitParticipant($0.value.data) })
        case 6:
            var cid = ""
            var track = LiveKitTrack()
            for (f, v) in try protoFields(value.data) {
                if f == 1 { cid = v.text }
                if f == 2 { track = try LiveKitTrack(v.data) }
            }
            self = .trackPublished(cid: cid, track: track)
        case 8: self = .leave
        case 10:
            // SpeakerInfo { sid = 1, level = 2, active = 3 }
            var speaking: [String] = []
            for (f, v) in try protoFields(value.data) where f == 1 {
                var sid = ""
                var active = false
                for (g, w) in try protoFields(v.data) {
                    if g == 1 { sid = w.text }
                    if g == 3 { active = w.flag }
                }
                if active { speaking.append(sid) }
            }
            self = .speakers(speaking)
        case 16: self = .refreshToken(value.text)
        case 17:
            self = .trackUnpublished(sid: try protoFields(value.data).first { $0.field == 1 }?.value.text ?? "")
        case 18, 20: self = .pong
        default: self = .other
        }
    }
}

/// What the app says (SignalRequest).
enum LiveKitRequest: Equatable, Sendable {
    case offer(LiveKitDescription)
    case answer(LiveKitDescription)
    case trickle(LiveKitTrickle)
    case addTrack(
        cid: String, name: String, type: LiveKitTrackType, source: LiveKitTrackSource, width: Int, height: Int)
    case mute(sid: String, muted: Bool)
    case leave
    case ping(timestampMs: Int64)

    var data: Data {
        var w = ProtoWriter()
        switch self {
        case .offer(let d): w.message(1) { d.write(&$0) }
        case .answer(let d): w.message(2) { d.write(&$0) }
        case .trickle(let t):
            w.message(3) {
                $0.string(1, t.candidateInit)
                $0.uint(2, t.target.rawValue)
            }
        case .addTrack(let cid, let name, let type, let source, let width, let height):
            w.message(4) {
                $0.string(1, cid)
                $0.string(2, name)
                $0.uint(3, type.rawValue)
                $0.uint(4, UInt64(width))
                $0.uint(5, UInt64(height))
                $0.uint(8, source.rawValue)
            }
        case .mute(let sid, let muted):
            w.message(5) {
                $0.string(1, sid)
                $0.bool(2, muted)
            }
        case .leave:
            // CLIENT_INITIATED (1), DISCONNECT.
            w.message(8) { $0.uint(2, 1) }
        case .ping(let ms):
            w.message(16) { $0.int(1, ms) }
        }
        return w.data
    }
}
