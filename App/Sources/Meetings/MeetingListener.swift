import AgentsKitCore
import AVFoundation
import Foundation
import Observation

/// The Mac listening to a meeting (#7584, epic #7327), behind the flag `meeting-mode`: the
/// microphone and the call app, each with its own transcriber and diarizer (#7580), the
/// segments sent with `meetings/append` as they are final, and each speaker sent again when
/// its diarizer settles it. The daemon keeps them and decides each gap (#7582). Audio is
/// never kept. Its screens are #7585; this is what they drive.
@MainActor
@Observable
final class MeetingListener {
    enum Phase: String, Sendable {
        case idle, starting, listening, paused, ended
    }

    /// The most a meeting listens, as the daemon's cap (6,000 segments, 4 hours).
    static let audioCap: TimeInterval = 4 * 60 * 60

    private(set) var phase: Phase = .idle
    private(set) var meeting: DaemonAPI.Meeting?
    /// The live line of each stream, by source.
    private(set) var volatile: [String: String] = [:]
    private(set) var problem: String?
    /// The last seq the daemon kept.
    private(set) var lastSeq: Int64 = 0
    /// Segments sent, new and changed, for the log and a walk.
    private(set) var sentCount = 0

    @ObservationIgnored private let client: DaemonClient
    @ObservationIgnored private var streams: [MeetingStream] = []
    @ObservationIgnored private var clerk = MeetingClerk(options: .init(diarized: [], micCancelsEcho: true))
    /// What waits to go, in order; one append at a time, so seqs arrive in order.
    @ObservationIgnored private var outbox: [DaemonAPI.MeetingSegment] = []
    @ObservationIgnored private var sending: Task<Void, Never>?
    @ObservationIgnored private var cap: Task<Void, Never>?
    #if DEBUG
    /// A walk's files in place of the microphone and the call app, by source.
    @ObservationIgnored var walkFiles: [String: URL] = [:]
    #endif

    init(client: DaemonClient) {
        self.client = client
    }

    /// Listens to `meeting`, which `meetings/start` made. Its sources say what to open.
    func listen(to meeting: DaemonAPI.Meeting) async {
        guard phase == .idle || phase == .ended else { return }
        self.meeting = meeting
        phase = .starting
        problem = nil
        lastSeq = meeting.lastSeq
        sentCount = 0
        streams = meeting.sources.map { MeetingStream(source: $0, input: input(for: $0)) }
        if meeting.sources.contains(DaemonAPI.MeetingSource.mic), !isWalked(DaemonAPI.MeetingSource.mic),
           !(await AVCaptureDevice.requestAccess(for: .audio)) {
            problem = "Agent Loops may not use the microphone: System Settings ▸ Privacy & Security ▸ Microphone."
            streams.removeAll { $0.source == DaemonAPI.MeetingSource.mic }
        }
        var prepared: [MeetingStream] = []
        for stream in streams {
            do {
                try await stream.prepare()
                prepared.append(stream)
            } catch {
                note("meeting: \(stream.source) has no transcriber: \(error)")
                problem = "\(stream.source): \(error.localizedDescription)"
            }
        }
        // From here to the last open, nothing waits: no stream's words come before the
        // clerk knows the streams.
        clerk = MeetingClerk(options: .init(diarized: Set(prepared.filter(\.isDiarized).map(\.source)), micCancelsEcho: true),
                             lastSeq: meeting.lastSeq)
        let now = meetingTime(of: meeting)
        streams = []
        for stream in prepared {
            stream.onWords = { [weak self] words in self?.send(self?.clerk.heard(words) ?? []) }
            stream.onDiarizer = { [weak self, source = stream.source] news in
                self?.send(self?.clerk.diarizer(news, of: source) ?? [])
            }
            stream.onVolatile = { [weak self, source = stream.source] text in self?.volatile[source] = text }
            do {
                try stream.open(at: now)
                streams.append(stream)
                if let mic = stream.input as? MicrophoneInput { clerk.options.micCancelsEcho = mic.cancelsEcho }
            } catch {
                note("meeting: \(stream.source) did not open: \(error)")
                problem = "\(stream.source): \(error.localizedDescription)"
                await stream.stop()
            }
        }
        guard !streams.isEmpty else {
            phase = .ended
            return
        }
        phase = .listening
        MeetingTones.listening()
        watchTheCap(from: now)
        if meeting.state != .listening { await setState(.listening) }
    }

    func pause() async {
        guard phase == .listening else { return }
        phase = .paused
        MeetingTones.stopped()
        cap?.cancel()
        for stream in streams { await stream.pause() }
        send(clerk.finish())
        await drained()
        await setState(.paused)
    }

    func resume() async {
        guard phase == .paused, let meeting else { return }
        let now = meetingTime(of: meeting)
        for stream in streams {
            do {
                try stream.open(at: now)
            } catch {
                problem = "\(stream.source): \(error.localizedDescription)"
            }
        }
        phase = .listening
        MeetingTones.listening()
        watchTheCap(from: now)
        await setState(.listening)
    }

    /// The end: the last words and speakers go, then the meeting ends.
    func stop() async {
        guard phase == .listening || phase == .paused || phase == .starting else { return }
        let wasListening = phase == .listening
        phase = .ended
        cap?.cancel()
        if wasListening { MeetingTones.stopped() }
        for stream in streams { await stream.stop() }
        send(clerk.finish())
        await drained()
        await setState(.ended)
        streams = []
        volatile = [:]
        note("meeting: ended, \(sentCount) segments sent, \(clerk.dropped) mic pieces the gate dropped")
    }

    // MARK: -

    private func isWalked(_ source: String) -> Bool {
        #if DEBUG
        walkFiles[source] != nil
        #else
        false
        #endif
    }

    private func input(for source: String) -> any MeetingInput {
        #if DEBUG
        if let file = walkFiles[source] { return FileInput(file: file) }
        #endif
        if let bundleID = DaemonAPI.MeetingSource.bundleID(of: source) { return CallAppInput(bundleID: bundleID) }
        return MicrophoneInput()
    }

    private func meetingTime(of meeting: DaemonAPI.Meeting) -> TimeInterval {
        max(0, Date().timeIntervalSince(meeting.startedAt))
    }

    /// Stops at 4 hours from the meeting's start, as the daemon does.
    private func watchTheCap(from now: TimeInterval) {
        cap?.cancel()
        let left = max(0, Self.audioCap - now)
        cap = Task { [weak self] in
            try? await Task.sleep(for: .seconds(left))
            guard !Task.isCancelled else { return }
            note("meeting: 4 hours, stopping")
            await self?.stop()
        }
    }

    private func send(_ segments: [DaemonAPI.MeetingSegment]) {
        guard !segments.isEmpty else { return }
        outbox += segments
        guard sending == nil else { return }
        sending = Task { [weak self] in
            await self?.drain()
            self?.sending = nil
        }
    }

    /// Sends the outbox, 200 at a time, until it is empty or the daemon refuses.
    private func drain() async {
        while !outbox.isEmpty, let meeting {
            let batch = Array(outbox.prefix(DaemonAPI.MeetingAppendRequest.segmentLimit))
            do {
                let kept = try await client.call(DaemonAPI.Method.meetingsAppend,
                                                 DaemonAPI.MeetingAppendRequest(meetingID: meeting.id, segments: batch),
                                                 returning: DaemonAPI.MeetingAppended.self)
                outbox.removeFirst(batch.count)
                lastSeq = kept.lastSeq
                sentCount += batch.count
            } catch {
                // Kept for the next try: the next words, or the stop, send them again.
                note("meeting: append of \(batch.count) refused: \(error)")
                problem = "The meeting's words did not reach the host: \(error.localizedDescription)"
                return
            }
        }
    }

    private func drained() async {
        await sending?.value
        if !outbox.isEmpty { await drain() }
    }

    private func setState(_ state: DaemonAPI.MeetingState) async {
        guard let id = meeting?.id else { return }
        do {
            meeting = try await client.call(DaemonAPI.Method.meetingsSetState,
                                            DaemonAPI.MeetingSetStateRequest(meetingID: id, state: state),
                                            returning: DaemonAPI.Meeting.self)
        } catch {
            note("meeting: setState \(state) refused: \(error)")
        }
    }
}
