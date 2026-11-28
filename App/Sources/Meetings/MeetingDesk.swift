import AgentsKitCore
import AppKit
import Foundation
import Observation

/// The window's meetings (#7585, epic #7327), behind the flag `meeting-mode`: each meeting by
/// its session, the transcript of the one open, the gaps this window saw go to the agent, and
/// the one meeting this Mac listens to. Meetings are this Mac's host's only in the MVP: the
/// listening is this Mac's (#7584).
@MainActor
@Observable
final class MeetingDesk {
    /// The meetings this window knows, by their session.
    private(set) var byAgent: [UUID: DaemonAPI.Meeting] = [:]
    /// The open transcript's segments, by meeting, oldest first: at most `kept` of the last.
    private(set) var segments: [UUID: [DaemonAPI.MeetingSegment]] = [:]
    /// The gaps this window saw, by meeting (`meetings/changed` moving `sentThroughSeq`); the
    /// first one is everything sent before the window read it, with no time.
    private(set) var gaps: [UUID: [MeetingLines.Gap]] = [:]
    /// The sessions switched to Chat; any other meeting shows its transcript.
    private(set) var showsChat: Set<UUID> = []
    /// The one meeting this Mac listens to.
    private(set) var listener: MeetingListener?
    /// The capsule's level, 0 to 1, read ten times a second while this Mac listens.
    private(set) var level: Float = 0
    /// Starting a meeting from the sheet.
    private(set) var isStarting = false

    /// The most segments the window holds for a transcript: the last ones; earlier ones are
    /// a count over the first line.
    static let kept = 400

    /// The app model, for this Mac's host and the window's failure toast.
    @ObservationIgnored weak var model: AppModel?
    @ObservationIgnored private var reading: Set<UUID> = []
    @ObservationIgnored private var levels: Task<Void, Never>?

    private var client: DaemonClient {
        get throws {
            guard let model else { throw CancellationError() }
            return model.macClient
        }
    }

    private func problem(_ words: String) { model?.problem = words }

    static var isOn: Bool { FeatureFlag.meetingMode.isOn(in: WindowDefaults.store) }

    func meeting(for agentID: UUID?) -> DaemonAPI.Meeting? {
        guard Self.isOn, let agentID else { return nil }
        return byAgent[agentID]
    }

    func meeting(id: UUID) -> DaemonAPI.Meeting? { byAgent.values.first { $0.id == id } }

    // MARK: Reads

    /// The newest page of meetings, after a connect.
    func refresh() async {
        guard Self.isOn else { return }
        do {
            let page = try await client.call(DaemonAPI.Method.meetingsList,
                                               DaemonAPI.MeetingListRequest(limit: DaemonAPI.MeetingListRequest.pageLimit),
                                               returning: DaemonAPI.MeetingPage.self)
            for meeting in page.meetings { keep(meeting) }
        } catch {
            note("meetings: list refused: \(error)")
        }
    }

    /// A `meetings/changed`: the meeting as it is now, and its new words for an open transcript.
    func changed(_ change: DaemonAPI.MeetingChangedNotification) {
        guard Self.isOn else { return }
        if let meeting = change.meeting { keep(meeting) }
        guard let meeting = meeting(id: change.meetingID), segments[meeting.id] != nil else { return }
        let have = segments[meeting.id]?.last?.seq ?? 0
        if change.lastSeq > have { Task { await readAfter(meeting.id, have) } }
    }

    private func keep(_ meeting: DaemonAPI.Meeting) {
        let old = byAgent[meeting.agentID]
        if let old, old.updatedAt > meeting.updatedAt { return }
        byAgent[meeting.agentID] = meeting
        guard segments[meeting.id] != nil else { return }
        let before = old?.sentThroughSeq ?? 0
        if let gap = MeetingLines.gap(movedFrom: before, to: meeting.sentThroughSeq,
                                      at: old == nil ? nil : MeetingLines.elapsed(meeting, now: Date())) {
            gaps[meeting.id, default: []].append(gap)
            // The words of a gap have settled speakers by now: read them again.
            Task { await readAfter(meeting.id, max(0, gap.from - 1), replacing: true) }
        }
    }

    /// The transcript of a meeting that opens: its last `kept` segments.
    func open(_ meeting: DaemonAPI.Meeting) async {
        guard segments[meeting.id] == nil else { return }
        segments[meeting.id] = []
        if meeting.sentThroughSeq > 0 {
            gaps[meeting.id] = [MeetingLines.Gap(from: 1, through: meeting.sentThroughSeq, at: nil)]
        }
        await readAfter(meeting.id, max(0, meeting.lastSeq - Int64(Self.kept)))
    }

    /// The segments after `seq`, page by page; with `replacing`, those already held are
    /// read again (a changed speaker).
    private func readAfter(_ id: UUID, _ seq: Int64, replacing: Bool = false) async {
        guard !reading.contains(id) else { return }
        reading.insert(id)
        defer { reading.remove(id) }
        var after = seq
        while true {
            let page: DaemonAPI.MeetingSegmentsPage
            do {
                page = try await client.call(DaemonAPI.Method.meetingsSegments,
                                               DaemonAPI.MeetingSegmentsRequest(meetingID: id, afterSeq: after,
                                                                                limit: DaemonAPI.MeetingAppendRequest.segmentLimit),
                                               returning: DaemonAPI.MeetingSegmentsPage.self)
            } catch {
                note("meetings: segments of \(id) refused: \(error)")
                return
            }
            merge(page.segments, into: id)
            guard page.more, let last = page.segments.last?.seq else { return }
            after = last
        }
    }

    private func merge(_ new: [DaemonAPI.MeetingSegment], into id: UUID) {
        guard !new.isEmpty, var held = segments[id] else { return }
        let first = new[0].seq
        held.removeAll { $0.seq >= first && $0.seq <= new[new.count - 1].seq }
        held += new
        held.sort { $0.seq < $1.seq }
        if held.count > Self.kept { held.removeFirst(held.count - Self.kept) }
        segments[id] = held
    }

    /// The segments before the first one held.
    func earlier(_ meeting: DaemonAPI.Meeting) -> Int64 {
        max(0, (segments[meeting.id]?.first?.seq ?? meeting.lastSeq + 1) - 1)
    }

    // MARK: Transcript | Chat

    func setShowsChat(_ on: Bool, for agentID: UUID) {
        if on { showsChat.insert(agentID) } else { showsChat.remove(agentID) }
    }

    // MARK: Listening

    /// What the capsule says of `meeting`, from this Mac's listener when it is the one.
    func capsuleState(_ meeting: DaemonAPI.Meeting) -> MeetingLines.CapsuleState {
        if meeting.state == .ended { return .ended }
        #if DEBUG
        if let walked = walkCapsule[meeting.id] { return walked }
        #endif
        guard let listener, listener.meeting?.id == meeting.id else { return .notListening }
        switch listener.phase {
        case .idle, .starting: return .starting
        case .listening: return .listening
        case .paused: return .paused
        case .ended: return .ended
        }
    }

    /// A meeting from New Meeting: its session, then this Mac listening to it.
    func start(folder: URL, title: String, sources: [String], runtimeID: String?) async -> UUID? {
        isStarting = true
        defer { isStarting = false }
        do {
            let made = try await client.call(
                DaemonAPI.Method.meetingsStart,
                DaemonAPI.MeetingStartRequest(folder: folder, title: title.isEmpty ? nil : title, sources: sources,
                                              runtimeID: runtimeID),
                returning: DaemonAPI.MeetingStarted.self)
            keep(made.meeting)
            Task { await listen(to: made.meeting) }
            return made.agentID
        } catch {
            problem("The meeting did not start: \(error.localizedDescription)")
            return nil
        }
    }

    /// This Mac listens to `meeting`: Start Listening on a meeting nobody listens to.
    func listen(to meeting: DaemonAPI.Meeting) async {
        if let listener, listener.phase != .ended, listener.phase != .idle {
            guard listener.meeting?.id != meeting.id else { return }
            await listener.stop()
        }
        guard let model else { return }
        let listener = MeetingListener(client: model.macClient)
        #if DEBUG
        listener.walkFiles = walkFiles
        #endif
        self.listener = listener
        watchLevel()
        await listener.listen(to: meeting)
        if let problem = listener.problem { self.problem(problem) }
        if let now = listener.meeting { keep(now) }
    }

    func pause(_ meeting: DaemonAPI.Meeting) async {
        if let listener, listener.meeting?.id == meeting.id {
            await listener.pause()
            if let now = listener.meeting { keep(now) }
        } else {
            await setState(meeting, .paused)
        }
    }

    func resume(_ meeting: DaemonAPI.Meeting) async {
        if let listener, listener.meeting?.id == meeting.id, listener.phase == .paused {
            await listener.resume()
            if let now = listener.meeting { keep(now) }
        } else {
            await listen(to: meeting)
        }
    }

    func stop(_ meeting: DaemonAPI.Meeting) async {
        if let listener, listener.meeting?.id == meeting.id {
            await listener.stop()
            if let now = listener.meeting { keep(now) }
            levels?.cancel()
            level = 0
        } else {
            await setState(meeting, .ended)
        }
    }

    private func setState(_ meeting: DaemonAPI.Meeting, _ state: DaemonAPI.MeetingState) async {
        do {
            keep(try await client.call(DaemonAPI.Method.meetingsSetState,
                                         DaemonAPI.MeetingSetStateRequest(meetingID: meeting.id, state: state),
                                         returning: DaemonAPI.Meeting.self))
        } catch {
            problem("The meeting did not change: \(error.localizedDescription)")
        }
    }

    private func watchLevel() {
        levels?.cancel()
        levels = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(100))
                guard let self else { return }
                #if DEBUG
                if let walked = self.walkLevel {
                    self.level = walked
                    continue
                }
                #endif
                guard let listener = self.listener else { return }
                // Falls back slowly, as a meter does, so the bars do not flicker.
                let now = listener.phase == .listening ? listener.takeLevel() : 0
                self.level = max(now, self.level * 0.6)
            }
        }
    }

    /// The live line, while this Mac listens to `meeting`: the newest stream's words not final yet.
    func liveLine(_ meeting: DaemonAPI.Meeting) -> (source: String, text: String)? {
        #if DEBUG
        if let walked = walkLive[meeting.id] { return walked }
        #endif
        guard let listener, listener.meeting?.id == meeting.id, listener.phase == .listening else { return nil }
        return listener.volatile.first { !$0.value.isEmpty }.map { ($0.key, $0.value) }
    }

    // MARK: Speakers

    /// Names a voice for the whole meeting (`meetings/setSpeaker`); an empty name takes it off.
    func setSpeaker(_ meeting: DaemonAPI.Meeting, source: String, index: Int, name: String) async {
        let trimmed = String(name.trimmingCharacters(in: .whitespacesAndNewlines)
            .prefix(DaemonAPI.MeetingSetSpeakerRequest.nameLimit))
        do {
            keep(try await client.call(DaemonAPI.Method.meetingsSetSpeaker,
                                         DaemonAPI.MeetingSetSpeakerRequest(meetingID: meeting.id, source: source,
                                                                            index: index, name: trimmed),
                                         returning: DaemonAPI.Meeting.self))
        } catch {
            problem("Could not name the speaker: \(error.localizedDescription)")
        }
    }

    // MARK: The call apps

    /// The call apps New Meeting offers: those that run now, by bundle id, with their names.
    static let callApps: [(bundleID: String, name: String)] = [
        ("us.zoom.xos", "Zoom"),
        ("com.microsoft.teams2", "Microsoft Teams"),
        ("com.apple.FaceTime", "FaceTime"),
        ("com.tinyspeck.slackmacgap", "Slack"),
        ("com.cisco.webexmeetingsapp", "Webex"),
        ("com.hnc.Discord", "Discord"),
        ("com.google.Chrome", "Google Chrome"),
        ("com.apple.Safari", "Safari"),
    ]

    /// A call app's name, for the capsule: the list's, else the running app's, else the id.
    static func appName(_ bundleID: String) -> String {
        if let known = callApps.first(where: { $0.bundleID == bundleID }) { return known.name }
        return NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).first?.localizedName ?? bundleID
    }

    #if DEBUG
    /// A walk's sound files in place of the devices, by source (`WalkMeeting`).
    @ObservationIgnored var walkFiles: [String: URL] = [:]
    /// A walk's fixed level for the meter, as no sound plays.
    @ObservationIgnored var walkLevel: Float?
    /// A walk's capsule and live line for a meeting it scripts, with no listener (screenshots).
    var walkCapsule: [UUID: MeetingLines.CapsuleState] = [:]
    var walkLive: [UUID: (source: String, text: String)] = [:]
    /// A walk keeps a meeting it scripts, as `meetings/changed` would.
    func walkKeep(_ meeting: DaemonAPI.Meeting) { keep(meeting) }
    /// The meter's walked level, ticking as a listener's does.
    func walkMeter(_ on: Float?) {
        walkLevel = on
        if on != nil { watchLevel() }
    }
    #endif
}
