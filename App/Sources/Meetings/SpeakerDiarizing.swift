import AgentsKitCore
import FluidAudio
import Foundation

/// What meeting mode needs of a diarizer, and nothing more (#7584), as Actionable's
/// `DiarizationService.swift` (the reference). FluidAudio moves quickly and its model is a
/// Core ML conversion we do not own: when it changes, or Apple ships one, only the type
/// behind this changes. Above it, all is `DiarizerNews` on the stream's fed clock.
protocol SpeakerDiarizing: AnyObject, Sendable {
    /// Loads the model and starts. `news` is called off the main actor, in audio order.
    /// Throws when the model will not load: the stream then has no speakers.
    func start(news: @escaping @Sendable (DiarizerNews) -> Void) async throws
    /// One chunk of 16 kHz mono audio that starts at `fedStart` on the stream's fed clock.
    /// Called on the realtime thread: it only hands the chunk on.
    func append(_ samples: [Float], fedStart: TimeInterval)
    /// The turns still open, finished, and the model let go. Only at the meeting's end: a
    /// pause stops the feed, and the diarizer keeps the voices it knows (Actionable's note).
    func finish() async
}

/// FluidAudio's streaming LS-EEND, `dihard3` weights (#7580), one for each stream. It keeps
/// its speakers in its recurrent state for the whole meeting, so a voice keeps its slot.
final class FluidSpeakerDiarizer: SpeakerDiarizing, @unchecked Sendable {
    private let lock = NSLock()
    private var input: AsyncStream<(samples: [Float], fedStart: TimeInterval)>.Continuation?
    private var work: Task<Void, Never>?

    func start(news: @escaping @Sendable (DiarizerNews) -> Void) async throws {
        // The first time, FluidAudio fetches the model (about 100 MB) into its cache.
        let diarizer = try await LSEENDDiarizer(variant: .dihard3)
        let frame = diarizer.modelFrameHz.map { 1 / $0 } ?? 0.1
        let (stream, input) = AsyncStream<(samples: [Float], fedStart: TimeInterval)>.makeStream()
        lock.withLock { self.input = input }
        work = Task.detached(priority: .utility) {
            // The diarizer's frame 0 is the first chunk it gets, not the stream's start: the
            // model loads while the stream already runs.
            var offset: TimeInterval?
            func say(_ update: DiarizerTimelineUpdate?) {
                guard let update, let offset else { return }
                for segment in update.finalizedSegments {
                    news(.turn(SpeakerTurn(speakerIndex: segment.speakerIndex,
                                           start: offset + Double(segment.startFrame) * frame,
                                           end: offset + Double(segment.endFrame) * frame)))
                }
                news(.through(offset + Double(diarizer.numFramesProcessed) * frame))
            }
            for await chunk in stream {
                let start = offset ?? chunk.fedStart
                offset = start
                guard (try? diarizer.addAudio(chunk.samples, sourceSampleRate: 16_000)) != nil else { continue }
                say(try? diarizer.process())
            }
            say(try? diarizer.finalizeSession())
            diarizer.cleanup()
        }
    }

    func append(_ samples: [Float], fedStart: TimeInterval) {
        lock.withLock { _ = input?.yield((samples, fedStart)) }
    }

    func finish() async {
        lock.withLock {
            input?.finish()
            input = nil
        }
        await work?.value
        work = nil
    }
}
