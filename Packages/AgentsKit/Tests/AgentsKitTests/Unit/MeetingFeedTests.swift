import AVFoundation
import Foundation
import Testing
@testable import AgentsKitCore

/// One stream's audio, to its transcriber and its diarizer (#7584), with a recorded made-up
/// line (`Fixtures/meetings/made-up-mic.wav`: macOS `say`, no real voice).
@Suite("Meeting feed")
struct MeetingFeedTests {
    static let file = URL(filePath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "Fixtures/meetings/made-up-mic.wav")

    /// What each consumer got, gathered off the realtime thread.
    final class Got: @unchecked Sendable {
        let lock = NSLock()
        var transcriber: [(frames: AVAudioFrameCount, rate: Double, start: TimeInterval)] = []
        var diarizer: [(count: Int, start: TimeInterval)] = []
    }

    /// The file in buffers of 341 frames (1,024 at 48 kHz), at 48 kHz stereo float, as a call app's tap gives it.
    static func played(into feed: MeetingFeed) throws -> TimeInterval {
        let audio = try AVAudioFile(forReading: file)
        let tap = try #require(AVAudioFormat(standardFormatWithSampleRate: 48_000, channels: 2))
        let converter = try #require(DictationAudio.converter(from: audio.processingFormat, to: tap))
        var seconds: TimeInterval = 0
        while audio.framePosition < audio.length {
            let buffer = try #require(AVAudioPCMBuffer(pcmFormat: audio.processingFormat, frameCapacity: 341))
            try audio.read(into: buffer, frameCount: 341)
            seconds += Double(buffer.frameLength) / audio.processingFormat.sampleRate
            if let converted = DictationAudio.convert(buffer, with: converter) { feed.append(converted) }
        }
        return seconds
    }

    @Test func eachConsumerGetsTheLineInItsFormatOnOneClock() throws {
        let feed = MeetingFeed()
        let got = Got()
        let analyser = try #require(AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 16_000, channels: 1, interleaved: true))
        feed.transcriber(analyser) { buffer, start in
            got.lock.withLock { got.transcriber.append((buffer.frameLength, buffer.format.sampleRate, start)) }
        }
        feed.diarizer { samples, start in got.lock.withLock { got.diarizer.append((samples.count, start)) } }
        let seconds = try Self.played(into: feed)

        #expect(abs(feed.fedSeconds - seconds) < 0.01)
        #expect(got.transcriber.allSatisfy { $0.rate == 16_000 })
        let heard = Double(got.diarizer.reduce(0) { $0 + $1.count }) / 16_000
        #expect(abs(heard - seconds) < 0.05, "the diarizer had \(heard) s of \(seconds) s")
        // Both are told the same start for each buffer, and the starts follow each other.
        #expect(got.transcriber.map(\.start) == got.diarizer.map(\.start))
        #expect(zip(got.diarizer.map(\.start), got.diarizer.map(\.start).dropFirst()).allSatisfy { $0 < $1 })
        #expect(got.diarizer.first?.start == 0)
        // The line is speech, not silence.
        #expect(feed.takeLevel() > 0.05)
        #expect(feed.takeLevel() == 0)
    }

    @Test func aClosedFeedHandsNothingOn() throws {
        let feed = MeetingFeed()
        let got = Got()
        feed.diarizer { samples, start in got.lock.withLock { got.diarizer.append((samples.count, start)) } }
        feed.close()
        _ = try Self.played(into: feed)
        #expect(got.diarizer.isEmpty)
        #expect(feed.fedSeconds == 0)
    }
}
