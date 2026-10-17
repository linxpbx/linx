import AVFoundation
import Foundation
import Network
import Synchronization
import UIKit

/// Settings → **Test this network**: a diagnostic, not a feature (build 45,
/// `docs/RESOURCES.md`). Take it out with `CallWatch` once the cause is known.
///
/// On Etisalat mobile data the owner's iPhone stops sending everything for
/// about two seconds, every few seconds, through a call's first minute — a
/// separate connection that has nothing to do with the call is held at the
/// same moments (build 44, 2026-10-17). The same SIM sending at a call's
/// rate with no call up was not held, Safari's calls were held far less,
/// and 3CX isn't held at all. So something the app's call does is what
/// sets it off. This puts the call together a piece at a time, with no one
/// on the other end, and counts the holds on the side line in each step:
///
///  1. nothing else running;
///  2. a call on the system's books (CallKit), with the sound handed over;
///  3. the microphone and speaker running (Apple's voice processing, the
///     unit WebRTC uses);
///  4. the camera running;
///  5. sending as much as a video call does, on a line of its own.
///
/// A second side line runs the whole time to a Linx abroad (the test VPS,
/// build 46): held at the same moments as the line home, the pause is on
/// the phone's mobile side; held only on the line home, it is on the way
/// into the owner's own network (their line, router or Pangolin).
///
/// A third runs to Linx at home marked as a voice call (build 47): iOS sends
/// a voice call's packets ahead of everything else the phone has queued, so
/// held on the ordinary line and not on this one, the pause is the phone's
/// own queue behind other uploads after a quiet spell, which an app that
/// marks its call traffic as voice (as calling apps do) never meets.
///
/// The step the holds start in is what to look at. The record goes to Linx
/// like a call's.
@MainActor @Observable final class NetworkTest {
    enum Step: Int, CaseIterable, Sendable {
        case alone, systemCall, sound, camera, busy

        var words: String {
            switch self {
            case .alone: return "Nothing else running"
            case .systemCall: return "With a call on the iPhone's books"
            case .sound: return "With the microphone and speaker on"
            case .camera: return "With the camera on"
            case .busy: return "Sending as much as a video call"
            }
        }
    }

    /// How long each step lasts: the holds came every three or four seconds.
    static let stepLasts: TimeInterval = 20
    /// The busy line: a 1 KB question a hundredth of a second, about
    /// 800 kbit/s, what a 540p picture and its sound send.
    static let busyEvery: TimeInterval = 0.01
    static let busyPadding = 1000

    let id: UUID
    /// The step running now; nil before and after.
    private(set) var step: Step?
    /// One line for each step once it's done, for the screen.
    private(set) var results: [String] = []
    private(set) var startedAt = Date()

    private let startCall: @MainActor () -> Void
    private let endCall: @MainActor () -> Void
    private var marks: [(at: Date, what: String)] = []
    private var holds: [(at: Date, length: TimeInterval)] = []
    private var farHolds: [(at: Date, length: TimeInterval)] = []
    private var voiceHolds: [(at: Date, length: TimeInterval)] = []
    /// The Linx abroad the second side line asks: the owner's test VPS
    /// (`docs/RESOURCES.md`). Its relay answers a Binding request over TLS
    /// like any Linx's.
    static let elsewhere = (host: "turn.vps.mym.ae", port: UInt16(443))
    private var steps: [(step: Step, from: Date, to: Date?)] = []
    private var cancelled = false
    private var callUp = false
    private var soundGiven = false
    private var relay: (host: String, port: UInt16)?
    private var side: SideLine?
    private var far: SideLine?
    private var voice: SideLine?
    private var busy: SideLine?
    private var engine: AVAudioEngine?
    private var camera: TestCamera?

    init(id: UUID, startCall: @escaping @MainActor () -> Void, endCall: @escaping @MainActor () -> Void) {
        self.id = id
        self.startCall = startCall
        self.endCall = endCall
    }

    var running: Bool { step != nil }

    /// Stops it where it is: a call is coming, or the person pressed Stop.
    func cancel() {
        guard !cancelled else { return }
        cancelled = true
        mark("Stopped before the end")
        finishTheCall()
    }

    /// The system ended the test's call, or wouldn't start it: the steps
    /// go on without it, and the record says so.
    func callEnded() {
        guard callUp else { return }
        callUp = false
        mark("iOS ended the call")
    }

    /// The system handed the sound over, or took it back.
    func sound(_ on: Bool) {
        soundGiven = on
        mark(on ? "iOS gave the call its sound" : "iOS took the sound back")
    }

    /// Runs every step and answers the record.
    func run(relay urls: [String]) async -> String {
        startedAt = Date()
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
        mark("Network test, build \(build), \(MobileData.now ? "on mobile data" : "not on mobile data")")
        if HeldNetworks.holdingHere { mark("This network was seen holding back UDP before") }
        mark("Connections on this phone: \(Self.interfaces())")
        mark("In use by iOS: \(await Self.inUse())")
        guard let (host, port) = RelayCertificates.tlsEndpoints(in: urls).first else {
            mark("No relay over TLS to test against")
            return record()
        }
        relay = (host, port)
        side = SideLine(
            host: host, port: port,
            held: { at, length in Task { @MainActor [weak self] in self?.holds.append((at, length)) } },
            tell: { words, at in Task { @MainActor [weak self] in self?.mark(words, at: at) } })
        side?.start()
        // The bench has no Linx abroad: a second line to the same relay.
        let elsewhere = Bench.on ? (host: host, port: port) : Self.elsewhere
        far = SideLine(
            host: elsewhere.host, port: elsewhere.port, name: "Side line abroad",
            held: { at, length in Task { @MainActor [weak self] in self?.farHolds.append((at, length)) } },
            tell: { words, at in Task { @MainActor [weak self] in self?.mark(words, at: at) } })
        far?.start()
        voice = SideLine(
            host: host, port: port, name: "Voice line", serviceClass: .interactiveVoice,
            held: { at, length in Task { @MainActor [weak self] in self?.voiceHolds.append((at, length)) } },
            tell: { words, at in Task { @MainActor [weak self] in self?.mark(words, at: at) } })
        voice?.start()
        await wait(1.5)
        for step in Step.allCases {
            guard !cancelled else { break }
            self.step = step
            steps.append((step, Date(), nil))
            mark("Step \(step.rawValue + 1): \(step.words)")
            await begin(step)
            await wait(Self.stepLasts)
            steps[steps.count - 1].to = Date()
            results.append(summary(of: steps[steps.count - 1]))
        }
        // The busy line and the camera first: they are the loud ones.
        busy?.stop()
        busy = nil
        if let camera { await camera.stop() }
        camera = nil
        engine?.stop()
        engine = nil
        finishTheCall()
        side?.stop()
        side = nil
        far?.stop()
        far = nil
        voice?.stop()
        voice = nil
        await wait(0.5)
        step = nil
        return record()
    }

    private func begin(_ step: Step) async {
        switch step {
        case .alone:
            break
        case .systemCall:
            // The category a call's sound needs, set before the system
            // activates it, while nothing else owns the session.
            try? AVAudioSession.sharedInstance().setCategory(
                .playAndRecord, mode: .voiceChat, options: [.allowBluetoothHFP, .duckOthers])
            callUp = true
            startCall()
            mark("Call put on the iPhone's books")
        case .sound:
            if !soundGiven { await wait(3) }
            if !soundGiven {
                if CallStyle.usesCallKit {
                    mark("iOS never gave the call its sound")
                } else {
                    try? AVAudioSession.sharedInstance().setActive(true)
                }
            }
            do {
                let engine = AVAudioEngine()
                try engine.inputNode.setVoiceProcessingEnabled(true)
                engine.inputNode.installTap(onBus: 0, bufferSize: 1024, format: nil) { _, _ in }
                _ = engine.mainMixerNode
                engine.prepare()
                try engine.start()
                self.engine = engine
                mark("Microphone and speaker on")
            } catch {
                mark("The microphone wouldn't start: \(error.localizedDescription)")
            }
        case .camera:
            guard AVCaptureDevice.authorizationStatus(for: .video) == .authorized else {
                mark("The camera isn't allowed: step skipped")
                return
            }
            let camera = TestCamera()
            if await camera.start() {
                self.camera = camera
                mark("Camera on")
            } else {
                mark("No camera to turn on")
            }
        case .busy:
            guard let (host, port) = relay else { return }
            busy = SideLine(
                host: host, port: port, name: "Busy line", every: Self.busyEvery, padding: Self.busyPadding,
                tell: { words, at in Task { @MainActor [weak self] in self?.mark(words, at: at) } })
            busy?.start()
        }
    }

    private func finishTheCall() {
        guard callUp else { return }
        callUp = false
        endCall()
        mark("Call taken off the iPhone's books")
    }

    /// Waits, a quarter of a second at a time, so Stop is quick.
    private func wait(_ seconds: TimeInterval) async {
        let until = Date().addingTimeInterval(seconds)
        while !cancelled, Date() < until {
            try? await Task.sleep(for: .milliseconds(250))
        }
    }

    private func mark(_ what: String, at: Date = Date()) {
        guard marks.count < 400 else { return }
        marks.append((at, what))
    }

    private func holdsIn(_ step: (step: Step, from: Date, to: Date?)) -> [TimeInterval] {
        let to = step.to ?? Date()
        return holds.filter { $0.at >= step.from && $0.at < to }.map(\.length)
    }

    private func summary(of step: (step: Step, from: Date, to: Date?)) -> String {
        let held = holdsIn(step)
        let words = "\(step.step.rawValue + 1). \(step.step.words): "
        guard let longest = held.max() else { return words + "never held" }
        if held.count == 1 { return words + String(format: "held once, for %.1f s", longest) }
        return words + String(format: "held %d times, longest %.1f s", held.count, longest)
    }

    /// How another line did, and how many of the holds here it shared.
    private func compare(_ name: String, _ others: [(at: Date, length: TimeInterval)], where place: String) -> String {
        let together = holds.filter { here in
            let hereEnds = here.at.addingTimeInterval(here.length)
            return others.contains { there in
                there.at < hereEnds && here.at < there.at.addingTimeInterval(there.length)
            }
        }
        let longest = others.map(\.length).max() ?? 0
        return String(format: "%@: held %d times, longest %.1f s; ", name, others.count, longest)
            + "\(together.count) of the \(holds.count) holds here were held \(place) at the same moment"
    }

    private func record() -> String {
        var lines = ["Linx network test"]
        let first = steps.first { !holdsIn($0).isEmpty }
        if let first {
            lines.append("verdict: held from step \(first.step.rawValue + 1) (\(first.step.words.lowercased()))")
        } else if steps.count == Step.allCases.count {
            lines.append("verdict: never held")
        } else {
            lines.append("verdict: not finished")
        }
        lines.append(compare("abroad", farHolds, where: "abroad"))
        lines.append(compare("voice priority", voiceHolds, where: "on the voice line"))
        lines += steps.map { summary(of: $0) }
        let sorted = marks.sorted { $0.at < $1.at }
        lines += sorted.map { String(format: "+%.1f s %@", $0.at.timeIntervalSince(startedAt), $0.what) }
        return String(lines.joined(separator: "\n").prefix(16_000))
    }
}

extension NetworkTest {
    /// Every network interface that is up and has an address, with what
    /// kind of addresses: what WebRTC would try without iOS's path monitor
    /// (`WebRTCMedia.fieldTrials`). An iPhone with two lines lists one
    /// more mobile interface (pdp_ip…) than the line carrying its data.
    nonisolated static func interfaces() -> String {
        var first: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&first) == 0, let first else { return "unknown" }
        defer { freeifaddrs(first) }
        var kinds: [String: Set<String>] = [:]
        var order: [String] = []
        for pointer in sequence(first: first, next: { $0.pointee.ifa_next }) {
            let entry = pointer.pointee
            let flags = Int32(entry.ifa_flags)
            guard flags & IFF_UP != 0, flags & IFF_RUNNING != 0, flags & IFF_LOOPBACK == 0,
                let address = entry.ifa_addr
            else { continue }
            let name = String(cString: entry.ifa_name)
            let kind: String
            switch Int32(address.pointee.sa_family) {
            case AF_INET: kind = "IPv4"
            case AF_INET6:
                // A link-local address alone isn't a way out.
                let v6 = UnsafeRawPointer(address).assumingMemoryBound(to: sockaddr_in6.self).pointee
                let bytes = v6.sin6_addr.__u6_addr.__u6_addr8
                if bytes.0 == 0xFE, bytes.1 & 0xC0 == 0x80 { continue }
                kind = "IPv6"
            default: continue
            }
            if kinds[name] == nil { order.append(name) }
            kinds[name, default: []].insert(kind)
        }
        let words = order.map { "\($0) (\(kinds[$0]!.sorted().joined(separator: ", ")))" }
        return words.isEmpty ? "none" : words.joined(separator: ", ")
    }

    /// The interfaces of the path iOS is using now: what WebRTC keeps.
    nonisolated static func inUse() async -> String {
        await withCheckedContinuation { done in
            let monitor = NWPathMonitor()
            let once = Mutex(false)
            monitor.pathUpdateHandler = { path in
                let already = once.withLock { told in
                    defer { told = true }
                    return told
                }
                guard !already else { return }
                monitor.cancel()
                let names = path.availableInterfaces.map(\.name)
                done.resume(returning: names.isEmpty ? "none" : names.joined(separator: ", "))
            }
            monitor.start(queue: DispatchQueue(label: "linx.network-test.path"))
        }
    }
}

/// The front camera running at 720p with nothing taking its frames: the
/// camera's share of a video call, without WebRTC.
private final class TestCamera: @unchecked Sendable {
    private let session = AVCaptureSession()
    private let queue = DispatchQueue(label: "linx.network-test.camera")

    func start() async -> Bool {
        await withCheckedContinuation { done in
            queue.async {
                guard
                    let device = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .front)
                        ?? AVCaptureDevice.default(for: .video),
                    let input = try? AVCaptureDeviceInput(device: device)
                else {
                    done.resume(returning: false)
                    return
                }
                self.session.beginConfiguration()
                if self.session.canSetSessionPreset(.hd1280x720) { self.session.sessionPreset = .hd1280x720 }
                // The call's sound session is the system's: the camera may
                // not set up one of its own (build 5).
                self.session.automaticallyConfiguresApplicationAudioSession = false
                if self.session.canAddInput(input) { self.session.addInput(input) }
                let output = AVCaptureVideoDataOutput()
                output.alwaysDiscardsLateVideoFrames = true
                if self.session.canAddOutput(output) { self.session.addOutput(output) }
                self.session.commitConfiguration()
                self.session.startRunning()
                done.resume(returning: self.session.isRunning)
            }
        }
    }

    func stop() async {
        await withCheckedContinuation { done in
            queue.async {
                self.session.stopRunning()
                done.resume()
            }
        }
    }
}
