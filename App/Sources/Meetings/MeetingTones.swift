@preconcurrency import AVFoundation
import Foundation

/// The tones of meeting mode (#7584), so the people in the room hear it too: a rising pair of
/// notes when listening starts or resumes, the same pair falling on a pause and the stop (as
/// Actionable's `RecordingToneService.swift`). Two sine notes made once, not a recording. A
/// tone that will not play is skipped: it is never why a meeting does not listen.
@MainActor
enum MeetingTones {
    static func listening() { play(rising) }
    static func stopped() { play(falling) }

    #if DEBUG
    /// The tones a walk heard, in order, in place of the sound (no sound on a scratch app).
    static var walked: [String]?
    #endif

    private static let low = 587.33 // D5
    private static let high = 880.0 // A5
    private static let rate = 44_100.0
    private static let noteLength = 0.11

    private static var rising = player([low, high])
    private static var falling = player([high, low])

    private static func play(_ player: AVAudioPlayer?) {
        #if DEBUG
        if walked != nil {
            walked?.append(player === rising ? "rising" : "falling")
            return
        }
        #endif
        player?.currentTime = 0
        player?.play()
    }

    private static func player(_ notes: [Double]) -> AVAudioPlayer? {
        let player = try? AVAudioPlayer(data: wav(notes))
        player?.prepareToPlay()
        return player
    }

    /// The notes as 16-bit mono PCM in a WAV, the form `AVAudioPlayer(data:)` takes.
    private static func wav(_ notes: [Double]) -> Data {
        let frames = Int(rate * noteLength)
        let fade = Int(rate * 0.008)
        var samples = Data()
        for frequency in notes {
            for frame in 0..<frames {
                let edge = min(1, Double(min(frame, frames - frame)) / Double(fade))
                let value = sin(2 * .pi * frequency * Double(frame) / rate) * 0.25 * edge
                samples.append(little: Int16(value * Double(Int16.max)))
            }
        }
        var wav = Data("RIFF".utf8)
        wav.append(little: UInt32(36 + samples.count))
        wav.append(Data("WAVEfmt ".utf8))
        wav.append(little: UInt32(16))
        wav.append(little: UInt16(1))
        wav.append(little: UInt16(1))
        wav.append(little: UInt32(rate))
        wav.append(little: UInt32(rate) * 2)
        wav.append(little: UInt16(2))
        wav.append(little: UInt16(16))
        wav.append(Data("data".utf8))
        wav.append(little: UInt32(samples.count))
        wav.append(samples)
        return wav
    }
}

private extension Data {
    mutating func append<T: FixedWidthInteger>(little value: T) {
        Swift.withUnsafeBytes(of: value.littleEndian) { append(contentsOf: $0) }
    }
}
