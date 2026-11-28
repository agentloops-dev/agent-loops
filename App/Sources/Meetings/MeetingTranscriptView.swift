import AgentsKitCore
import SwiftUI

/// A meeting's transcript in the chat's place (#7585, the gate-2 wireframes of #7329): the
/// lines grouped by speaker with the time from the start, a thin line at each gap the agent
/// was sent, the live line in italic, and the listening capsule floating over the top. The
/// prompt bar stays under it (ChatView's), so the person can tell the agent something.
struct MeetingTranscriptPage: View {
    @Environment(AppModel.self) private var model
    let meeting: DaemonAPI.Meeting
    let bottomInset: CGFloat

    private var desk: MeetingDesk { model.meetings }
    /// The meeting as the desk has it now, not as it was handed.
    private var current: DaemonAPI.Meeting { desk.meeting(for: meeting.agentID) ?? meeting }

    var body: some View {
        let current = current
        let segments = desk.segments[current.id] ?? []
        ZStack(alignment: .top) {
            if segments.isEmpty, desk.liveLine(current) == nil {
                empty
            } else {
                lines(current, segments)
            }
            MeetingCapsule(meeting: current)
                .padding(.top, 12)
        }
        .task(id: current.id) { await desk.open(current) }
    }

    private var empty: some View {
        VStack(spacing: 8) {
            Text(MeetingLines.emptyTitle)
                .appText(.title)
                .foregroundStyle(Paper.ink)
            Text(MeetingLines.emptyText)
                .appText(.reading)
                .foregroundStyle(Paper.secondary)
            Text(MeetingLines.emptyFoot)
                .appText(.fine)
                .foregroundStyle(Paper.tertiary)
                .padding(.top, 4)
        }
        .multilineTextAlignment(.center)
        .frame(maxWidth: 420)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(.bottom, bottomInset)
    }

    private func lines(_ meeting: DaemonAPI.Meeting, _ segments: [DaemonAPI.MeetingSegment]) -> some View {
        let rows = MeetingLines.rows(segments, meeting: meeting, gaps: desk.gaps[meeting.id] ?? [])
        let now = Date()
        return ScrollView {
            LazyVStack(alignment: .leading, spacing: 2) {
                if let earlier = MeetingLines.earlierLine(desk.earlier(meeting)) {
                    GapLine(icon: "arrow.up", text: earlier)
                }
                ForEach(rows) { row in
                    MeetingLineRow(meeting: meeting, time: row.time, speaker: row.speaker, voice: row.voice,
                                   text: row.text, isLive: false)
                    if let gap = row.gapAfter {
                        GapLine(icon: "arrow.turn.down.right", text: gap.line)
                    }
                }
                if let live = desk.liveLine(meeting) {
                    let voice = MeetingLines.speaker(meeting, source: live.source, index: nil)
                    MeetingLineRow(meeting: meeting, time: MeetingLines.clock(MeetingLines.elapsed(meeting, now: now)),
                                   speaker: nil, voice: voice, text: live.text, isLive: true)
                }
                switch desk.capsuleState(meeting) {
                case .paused:
                    GapLine(icon: "pause.circle", text: MeetingLines.pausedAt(MeetingLines.elapsed(meeting, now: now)))
                case .ended:
                    GapLine(icon: "stop", text: MeetingLines.endedAt(MeetingLines.elapsed(meeting, now: now)))
                default:
                    EmptyView()
                }
            }
            .chatColumn()
            .padding(.horizontal, 28)
            .padding(.top, 64)
            .padding(.bottom, bottomInset + 12)
        }
        .defaultScrollAnchor(.bottom)
    }
}

/// One line: the speaker where it changes and the time, then the words. The live line is in
/// italic and the tertiary ink, as the wireframe draws the words still being heard.
private struct MeetingLineRow: View {
    let meeting: DaemonAPI.Meeting
    let time: String
    let speaker: MeetingLines.Speaker?
    let voice: MeetingLines.Speaker
    let text: String
    let isLive: Bool

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            VStack(alignment: .trailing, spacing: 1) {
                if let speaker {
                    SpeakerName(meeting: meeting, speaker: speaker)
                }
                Text(time)
                    .appText(.fine)
                    .monospacedDigit()
                    .foregroundStyle(Paper.tertiary)
            }
            .frame(width: 96, alignment: .trailing)
            Text(text)
                .appText(.reading)
                .italic(isLive)
                .foregroundStyle(isLive ? Paper.tertiary : Paper.ink)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityLabel(isLive ? "Being heard: \(text)" : text)
        }
        .padding(.vertical, 5)
    }
}

/// A speaker's name over its lines: click it to name the voice for the whole meeting
/// (`meetings/setSpeaker`). An unnamed voice is "Speaker 4" in italic.
private struct SpeakerName: View {
    @Environment(AppModel.self) private var model
    let meeting: DaemonAPI.Meeting
    let speaker: MeetingLines.Speaker
    @State private var naming = false

    var body: some View {
        if let index = speaker.index {
            Button {
                naming = true
            } label: {
                label
            }
            .buttonStyle(.plain)
            .help(MeetingLines.namingTitle(speaker))
            .popover(isPresented: $naming, arrowEdge: .trailing) {
                SpeakerNamePopover(title: MeetingLines.namingTitle(speaker), name: speaker.name ?? "") { name in
                    naming = false
                    Task { await model.meetings.setSpeaker(meeting, source: speaker.source, index: index, name: name) }
                } cancel: {
                    naming = false
                }
                .paperPopover()
            }
        } else {
            label
        }
    }

    private var label: some View {
        Text(speaker.label)
            .appText(.supporting)
            .fontWeight(speaker.isNamed ? .semibold : .medium)
            .italic(!speaker.isNamed)
            .foregroundStyle(speaker.isNamed ? Paper.secondary : Paper.tertiary)
            .lineLimit(1)
    }
}

/// Name Speaker 4: a field, the hint that the name is for the whole meeting, Cancel and Name.
struct SpeakerNamePopover: View {
    let title: String
    @State var name: String
    let save: (String) -> Void
    let cancel: () -> Void
    @FocusState private var focused: Bool

    init(title: String, name: String, save: @escaping (String) -> Void, cancel: @escaping () -> Void) {
        self.title = title
        _name = State(initialValue: name)
        self.save = save
        self.cancel = cancel
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .appText(.supporting)
                .fontWeight(.semibold)
            TextField("Name", text: $name)
                .textFieldStyle(.roundedBorder)
                .focused($focused)
                .onSubmit { save(name) }
            Text(MeetingLines.namingHint)
                .appText(.fine)
                .foregroundStyle(Paper.tertiary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Cancel", action: cancel)
                    .buttonStyle(.paper)
                    .keyboardShortcut(.cancelAction)
                Button("Name") { save(name) }
                    .buttonStyle(.paperProminent)
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(12)
        .frame(width: 260)
        .onAppear { focused = true }
    }
}

/// A thin line across the transcript with a few words: a gap sent to the agent, the earlier
/// lines not shown, a pause or the end.
private struct GapLine: View {
    let icon: String
    let text: String

    var body: some View {
        HStack(spacing: 10) {
            Label {
                Text(text)
            } icon: {
                Image(icon: icon)
            }
            .labelStyle(.titleAndIcon)
            .appText(.fine)
            .foregroundStyle(Paper.tertiary)
            .lineLimit(1)
            Rectangle()
                .fill(Paper.rule)
                .frame(height: 1)
        }
        .padding(.leading, 108)
        .padding(.vertical, 6)
    }
}

/// The capsule over the transcript: the state, the clock, the level, the sources, and
/// Pause and Stop, or Resume, or Start Listening. Pause and Stop play the falling tone;
/// Start and Resume the rising one (the listener's, #7584). The dot is the accent cobalt,
/// not a recording red: red is a failure in the design system (gate 2).
struct MeetingCapsule: View {
    @Environment(AppModel.self) private var model
    let meeting: DaemonAPI.Meeting

    private var desk: MeetingDesk { model.meetings }

    var body: some View {
        let state = desk.capsuleState(meeting)
        HStack(spacing: 12) {
            ListeningDot(state: state)
            Text(MeetingLines.capsuleTitle(state))
                .appText(.supporting)
                .fontWeight(state == .notListening ? .regular : .semibold)
            if state != .notListening {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Text(MeetingLines.clock(MeetingLines.elapsed(meeting, now: context.date)))
                        .appText(.supporting)
                        .monospacedDigit()
                        .foregroundStyle(Paper.secondary)
                }
            }
            if state == .listening {
                LevelMeter(level: desk.level)
            }
            Text(detail(state))
                .appText(.fine)
                .foregroundStyle(Paper.tertiary)
                .lineLimit(1)
            buttons(state)
        }
        .padding(.leading, 14)
        .padding(.trailing, state == .ended ? 14 : 8)
        .padding(.vertical, 7)
        .paperRaised(in: Capsule())
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Meeting: \(MeetingLines.capsuleTitle(state))")
    }

    private func detail(_ state: MeetingLines.CapsuleState) -> String {
        switch state {
        case .paused: MeetingLines.pausedLine
        case .ended: MeetingLines.linesCount(meeting.lastSeq)
        default: MeetingLines.sourcesLine(meeting.sources, appName: MeetingDesk.appName)
        }
    }

    @ViewBuilder
    private func buttons(_ state: MeetingLines.CapsuleState) -> some View {
        switch state {
        case .notListening:
            Button(MeetingLines.startButton) { Task { await desk.listen(to: meeting) } }
                .buttonStyle(.paperProminent)
                .help("Listen to this meeting on this Mac: a rising tone plays")
        case .starting:
            ProgressView().controlSize(.small)
        case .listening:
            Button("Pause") { Task { await desk.pause(meeting) } }
                .buttonStyle(.paper)
                .help("Stop listening for now: a falling tone plays")
            Button("Stop") { Task { await desk.stop(meeting) } }
                .buttonStyle(.paper)
                .help("End the meeting: a falling tone plays, and the agent gets the last lines")
        case .paused:
            Button("Resume") { Task { await desk.resume(meeting) } }
                .buttonStyle(.paperProminent)
                .help("Listen again: a rising tone plays")
            Button("Stop") { Task { await desk.stop(meeting) } }
                .buttonStyle(.paper)
                .help("End the meeting, and the agent gets the last lines")
        case .ended:
            EmptyView()
        }
    }
}

/// The capsule's dot: the accent with a ring while it listens, grey when paused, a hollow
/// ring when not listening or ended.
private struct ListeningDot: View {
    let state: MeetingLines.CapsuleState
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var ringing = false

    var body: some View {
        ZStack {
            switch state {
            case .listening, .starting:
                Circle().fill(Paper.accent)
                if !reduceMotion {
                    Circle()
                        .stroke(Paper.accent, lineWidth: 2)
                        .scaleEffect(ringing ? 1.6 : 0.5)
                        .opacity(ringing ? 0 : 0.8)
                        .frame(width: 22, height: 22)
                        .onAppear {
                            withAnimation(.easeOut(duration: 1.6).repeatForever(autoreverses: false)) { ringing = true }
                        }
                }
            case .paused:
                Circle().fill(Paper.tertiary)
            case .notListening, .ended:
                Circle().strokeBorder(Paper.edge, lineWidth: 2)
            }
        }
        .frame(width: 10, height: 10)
        .accessibilityHidden(true)
    }
}

/// Eight bars of the level, in the accent.
private struct LevelMeter: View {
    let level: Float
    private static let shape: [CGFloat] = [0.4, 0.75, 0.3, 0.95, 0.55, 0.8, 0.35, 0.6]

    var body: some View {
        HStack(alignment: .center, spacing: 2) {
            ForEach(Self.shape.indices, id: \.self) { index in
                RoundedRectangle(cornerRadius: 1.5)
                    .fill(Paper.accent)
                    .frame(width: 3, height: max(3, 15 * CGFloat(level) * Self.shape[index]))
            }
        }
        .frame(height: 16)
        .animation(.easeOut(duration: 0.1), value: level)
        .accessibilityHidden(true)
    }
}

/// The toolbar's Transcript | Chat switch on a meeting's session (#7585).
enum MeetingSwitchWords {
    static let transcript = "Transcript"
    static let chat = "Chat"
    static let help = "Show the meeting's transcript, or the session's chat with the agent"
}
