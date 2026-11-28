import AgentsKitCore
@preconcurrency import AVFoundation
import CoreMedia
import Foundation
import Speech

/// One stream of a meeting (#7584): its input, a `SpeechTranscriber` for its words, and a
/// diarizer of its own for its voices (#7580). Both get the same audio from the feed, with
/// its start on the fed clock, and `clock` makes that the meeting's time.
@MainActor
final class MeetingStream {
    let source: String
    let input: any MeetingInput
    let feed = MeetingFeed()
    private(set) var clock = MeetingAudioClock()
    /// The stream has a diarizer that loaded.
    private(set) var isDiarized = false
    /// The live line: the words being heard, not final yet.
    private(set) var volatile = ""

    var onWords: ((HeardWords) -> Void)?
    var onDiarizer: ((DiarizerNews) -> Void)?
    var onVolatile: ((String) -> Void)?

    private var analyzer: SpeechAnalyzer?
    private var analyzerInput: AsyncStream<AnalyzerInput>.Continuation?
    private var results: Task<Void, Never>?
    private var diarizer: (any SpeakerDiarizing)?
    private let makeDiarizer: () -> any SpeakerDiarizing

    init(source: String, input: any MeetingInput, diarizer: @escaping () -> any SpeakerDiarizing = { FluidSpeakerDiarizer() }) {
        self.source = source
        self.input = input
        self.makeDiarizer = diarizer
    }

    /// The transcriber and the diarizer ready, before the input opens. Throws when the
    /// words cannot be had; a diarizer that will not load leaves only the speakers out.
    func prepare() async throws {
        try await startTranscriber()
        await startDiarizer()
    }

    /// The input open: the first run starts at `meetingTime` seconds from the meeting's start.
    func open(at meetingTime: TimeInterval) throws {
        clock.startRun(fed: feed.fedSeconds, meeting: meetingTime)
        try input.open(into: feed)
        note("meeting: \(source) listening, \(input.route), diarized \(isDiarized)")
    }

    /// Paused: the input closes and the transcriber finishes the words it has. The
    /// diarizer stays, with the voices it knows; `open` is the next run.
    func pause() async {
        input.close()
        try? await analyzer?.finalize(through: nil)
    }


    /// The end: the input closes, the last words and turns come, and all is let go.
    func stop() async {
        input.close()
        feed.close()
        analyzerInput?.finish()
        if let analyzer {
            try? await analyzer.finalizeAndFinishThroughEndOfInput()
        }
        await results?.value
        await diarizer?.finish()
        analyzer = nil
        results = nil
        diarizer = nil
    }

    // MARK: -

    private func startTranscriber() async throws {
        guard SpeechTranscriber.isAvailable,
              let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Locale.current) else {
            throw MeetingStreamError.noTranscriber(Locale.current.identifier)
        }
        let transcriber = SpeechTranscriber(locale: locale, transcriptionOptions: [],
                                            reportingOptions: [.volatileResults], attributeOptions: [])
        // The model for this language, the first time: shared with every app on the Mac.
        if let install = try await AssetInventory.assetInstallationRequest(supporting: [transcriber]) {
            try await install.downloadAndInstall()
        }
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]) else {
            throw MeetingStreamError.noTranscriber(locale.identifier)
        }
        let analyzer = SpeechAnalyzer(modules: [transcriber])
        let (stream, input) = AsyncStream<AnalyzerInput>.makeStream()
        try await analyzer.start(inputSequence: stream)
        self.analyzer = analyzer
        analyzerInput = input
        feed.transcriber(format) { buffer, fedStart in
            input.yield(AnalyzerInput(buffer: buffer, bufferStartTime: CMTime(seconds: fedStart, preferredTimescale: 48_000)))
        }
        results = Task { [weak self] in
            do {
                for try await result in transcriber.results {
                    let text = String(result.text.characters)
                    let start = result.range.start.seconds
                    let end = result.range.end.seconds
                    let final = result.isFinal
                    guard let self else { return }
                    self.heard(text, from: start.isFinite ? start : 0, to: end.isFinite ? end : 0, final: final)
                }
            } catch {
                note("meeting: \(self?.source ?? "") transcriber ended: \(error)")
            }
        }
    }

    /// A diarizer that will not load leaves the stream with no speakers, not unheard.
    private func startDiarizer() async {
        let diarizer = makeDiarizer()
        do {
            try await diarizer.start { [weak self] news in
                Task { @MainActor in self?.diarized(news) }
            }
        } catch {
            note("meeting: \(source) has no diarizer: \(error)")
            return
        }
        self.diarizer = diarizer
        isDiarized = true
        feed.diarizer { samples, fedStart in diarizer.append(samples, fedStart: fedStart) }
    }

    private func heard(_ text: String, from start: Double, to end: Double, final: Bool) {
        guard final else {
            volatile = text
            onVolatile?(text)
            return
        }
        volatile = ""
        onVolatile?("")
        onWords?(HeardWords(source: source, start: clock.meetingTime(start), end: clock.meetingTime(end), text: text))
    }

    private func diarized(_ news: DiarizerNews) {
        switch news {
        case .turn(let turn):
            onDiarizer?(.turn(SpeakerTurn(speakerIndex: turn.speakerIndex, start: clock.meetingTime(turn.start),
                                          end: clock.meetingTime(turn.end))))
        case .through(let time):
            onDiarizer?(.through(clock.meetingTime(time)))
        }
    }
}

enum MeetingStreamError: LocalizedError {
    case noTranscriber(String)

    var errorDescription: String? {
        switch self {
        case .noTranscriber(let locale): "This Mac cannot transcribe \(locale) yet."
        }
    }
}
