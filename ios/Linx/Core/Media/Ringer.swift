import AVFoundation
import Foundation

// The ring for a call coming in while the app is open. It is made in memory,
// not shipped as a sound file: two short tones every three seconds, the same
// pattern the web client rings (web/src/phone/ringtone.ts), so the two sound
// like one product.
//
// On a locked or sleeping phone the ring is CallKit's, not this one
// (build-order step 6): the system rings with the person's own ringtone.

@MainActor final class Ringer {
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
    private static let sound: Data = wav(seconds: 3) { time in
        let level: Double
        switch time {
        case 0..<0.4: level = sine(480, time)
        case 0.45..<0.85: level = sine(440, time)
        default: return 0
        }
        // A short fade in and out, so the tone doesn't click.
        let into = min(time.truncatingRemainder(dividingBy: 0.45), 0.02) / 0.02
        return level * 0.25 * min(1, into)
    }

    private static func sine(_ hertz: Double, _ time: Double) -> Double {
        sin(2 * .pi * hertz * time)
    }

    /// A mono 16-bit WAV at 16 kHz, built from a function of time.
    private static func wav(seconds: Double, rate: Int = 16000, sample: (Double) -> Double) -> Data {
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
