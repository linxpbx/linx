import AVFoundation
import Foundation

// The ring for a call coming in while the app is open. It is made in memory,
// not shipped as a sound file: two short tones every three seconds, the same
// pattern the web client rings (web/src/phone/ringtone.ts), so the two sound
// like one product.
//
// This is not the ring for a locked or sleeping phone: that one is the
// system's own, through CallKit, played with the person's own ringtone. The
// app only rings for itself where CallKit may not be used — mainland China
// (ADR-078) — and while it is already open there.

/// A tone the app plays to itself during a call: the ring for one coming
/// in, the ringing tone for one going out. Behind a protocol so a test can
/// hear it without a speaker.
@MainActor protocol CallTone: AnyObject {
    func start()
    func stop()
}

@MainActor final class Ringer: CallTone {
    private var player: AVAudioPlayer?

    func start() {
        guard player == nil else { return }
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playback, mode: .default, options: [.duckOthers])
            try session.setActive(true)
            let player = try AVAudioPlayer(data: Self.sound)
            player.numberOfLoops = -1
            player.prepareToPlay()
            player.play()
            self.player = player
        } catch {
            // No sound available; the call still shows on the screen.
            player = nil
        }
    }

    func stop() {
        player?.stop()
        player = nil
    }

    /// One three-second turn of the ring, as a WAV in memory: 480 Hz, a
    /// breath, 440 Hz, then silence.
    private static let sound: Data = Tones.wav(seconds: 3) { time in
        let level: Double
        switch time {
        case 0..<0.4: level = Tones.sine(480, time)
        case 0.45..<0.85: level = Tones.sine(440, time)
        default: return 0
        }
        // A short fade in and out, so the tone doesn't click.
        let into = min(time.truncatingRemainder(dividingBy: 0.45), 0.02) / 0.02
        return level * 0.25 * min(1, into)
    }

}

/// The tone a phone plays to itself while a call it is making rings at the
/// other end.
///
/// Nobody else makes it. Asterisk answers an extension's INVITE with a bare
/// 180 Ringing and no sound in it, because the standard leaves the ringing
/// tone to the phone that dialled — which is what a desk phone does. The app
/// made none, so a call to an extension was silent from the moment it was
/// dialled until the other person picked up (owner, 2026-10-21). A call out
/// through a phone line is different: the provider sends its own ringing (or
/// "the number you have dialled…") in a 183, and that is played instead of
/// this one.
///
/// By then the call's audio session belongs to CallKit, and **nothing here
/// may touch it**: setting the category on a session CallKit has handed over
/// stops a call's sound dead, both ways (2026-10-04). So this plays into
/// whatever is already set up and follows the call's own route — the
/// earpiece, the loudspeaker or a headset, like the voice that follows it.
@MainActor final class Ringback: CallTone {
    private var player: AVAudioPlayer?

    func start() {
        guard player == nil, let player = try? AVAudioPlayer(data: Self.sound) else { return }
        player.numberOfLoops = -1
        player.prepareToPlay()
        player.play()
        self.player = player
    }

    func stop() {
        player?.stop()
        player = nil
    }

    /// One turn of it: 425 Hz for a second, then three seconds of quiet —
    /// the ringing tone of the UAE and most of the world outside North
    /// America (ITU-T E.180). Quieter than the ring, because this one is
    /// usually held to an ear.
    private static let sound: Data = Tones.wav(seconds: 4) { time in
        guard time < 1 else { return 0 }
        let edge = min(time, 1 - time, 0.02) / 0.02
        return Tones.sine(425, time) * 0.18 * max(0, edge)
    }
}

/// Making a tone in memory, so the app ships no sound files for its own
/// ringing.
enum Tones {
    static func sine(_ hertz: Double, _ time: Double) -> Double {
        sin(2 * .pi * hertz * time)
    }

    /// A mono 16-bit WAV at 16 kHz, built from a function of time.
    static func wav(seconds: Double, rate: Int = 16000, sample: (Double) -> Double) -> Data {
        let count = Int(seconds * Double(rate))
        var samples = Data(capacity: count * 2)
        for i in 0..<count {
            let value = Int16(max(-1, min(1, sample(Double(i) / Double(rate)))) * 32000)
            samples.append(UInt8(truncatingIfNeeded: value))
            samples.append(UInt8(truncatingIfNeeded: value >> 8))
        }
        var out = Data()
        func put(_ text: String) { out.append(contentsOf: Array(text.utf8)) }
        func put32(_ value: Int) {
            for shift in [0, 8, 16, 24] { out.append(UInt8(truncatingIfNeeded: value >> shift)) }
        }
        func put16(_ value: Int) {
            for shift in [0, 8] { out.append(UInt8(truncatingIfNeeded: value >> shift)) }
        }
        put("RIFF")
        put32(36 + samples.count)
        put("WAVEfmt ")
        put32(16)
        put16(1)
        put16(1)
        put32(rate)
        put32(rate * 2)
        put16(2)
        put16(16)
        put("data")
        put32(samples.count)
        out.append(samples)
        return out
    }
}
