import AgentsKitCore
import AppKit
import EventKit
import SwiftUI

/// New Meeting (#7585, the gate-2 wireframes of #7329), behind the flag `meeting-mode`: the
/// title (from the calendar event now, by EventKit), the folder, what to listen to (the
/// microphone, and the sound of each call app that runs), and the runtime. Start Listening
/// makes the meeting's session and listens; the rising tone plays then (#7584).
struct NewMeetingSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    /// The folder it opens on: the one New Session would start in.
    let folder: FolderKey?

    @State private var title = ""
    @State private var calendarHint: String?
    @State private var chosenFolder: FolderKey?
    @State private var listensToMic = true
    @State private var callApps: [(bundleID: String, name: String)] = []
    @State private var listensTo: Set<String> = []
    @State private var runtimeID: String = MeetingDesk.autoRuntime

    /// This Mac's folders: the listening is this Mac's.
    private var folders: [DaemonAPI.FolderSummary] { model.liveFolders.filter { $0.key.host == .mac } }

    private var sources: [String] {
        (listensToMic ? [DaemonAPI.MeetingSource.mic] : [])
            + callApps.map(\.bundleID).filter(listensTo.contains).map(DaemonAPI.MeetingSource.call)
    }

    private var folderURL: URL? {
        folders.first { $0.key == chosenFolder }?.key.folder
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(MeetingLines.sheetTitle)
                .appText(.title)
            field("Title") {
                TextField("Meeting", text: $title, prompt: Text("Meeting"))
                    .textFieldStyle(.roundedBorder)
                if let calendarHint { hint(calendarHint) }
            }
            field("Folder") {
                Picker("Folder", selection: $chosenFolder) {
                    ForEach(folders, id: \.key) { summary in
                        Text(summary.name).tag(Optional(summary.key))
                    }
                }
                .labelsHidden()
                hint("The agent keeps the notes, and files the action items in this folder's forge.")
            }
            field("Listen to") {
                Toggle(MeetingLines.microphone, isOn: $listensToMic)
                ForEach(callApps, id: \.bundleID) { app in
                    Toggle(MeetingLines.callAppLine(app.name), isOn: Binding(
                        get: { listensTo.contains(app.bundleID) },
                        set: { on in if on { listensTo.insert(app.bundleID) } else { listensTo.remove(app.bundleID) } }))
                }
                if callApps.isEmpty {
                    hint("No call app is open. Open Zoom, Teams or FaceTime first to hear the others on the call.")
                }
            }
            field("Runtime") {
                Picker("Runtime", selection: $runtimeID) {
                    Text("Auto").tag(MeetingDesk.autoRuntime)
                    ForEach(model.runtimes.filter(\.availability.isAvailable), id: \.runtime.id) { status in
                        Text(status.runtime.name).tag(status.runtime.id)
                    }
                }
                .labelsHidden()
            }
            HStack(spacing: 8) {
                Text(MeetingLines.sheetNote)
                    .appText(.fine)
                    .foregroundStyle(Paper.tertiary)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: 250, alignment: .leading)
                Spacer(minLength: 0)
                Button("Cancel") { dismiss() }
                    .buttonStyle(.paper)
                    .keyboardShortcut(.cancelAction)
                Button(MeetingLines.startButton) { start() }
                    .buttonStyle(.paperProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(folderURL == nil || sources.isEmpty || model.meetings.isStarting)
            }
            .padding(.top, 4)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 22)
        .frame(width: 520)
        .task {
            chosenFolder = folder.flatMap { key in folders.contains { $0.key == key } ? key : nil } ?? folders.first?.key
            callApps = MeetingDesk.runningCallApps()
            listensTo = Set(callApps.prefix(1).map(\.bundleID))
            if let event = await MeetingCalendar.now() {
                if title.isEmpty { title = event.title }
                calendarHint = MeetingLines.calendarHint(start: event.start, end: event.end, attendees: event.attendees)
            }
        }
    }

    private func field(_ label: String, @ViewBuilder _ content: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(label)
                .appText(.supporting)
                .fontWeight(.semibold)
                .foregroundStyle(Paper.secondary)
            content()
        }
    }

    private func hint(_ words: String) -> some View {
        Text(words)
            .appText(.fine)
            .foregroundStyle(Paper.tertiary)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func start() {
        guard let folderURL, let chosenFolder else { return }
        let sources = sources
        let runtime = runtimeID == MeetingDesk.autoRuntime ? nil : runtimeID
        let words = title.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            guard let agentID = await model.meetings.start(folder: folderURL, title: words, sources: sources,
                                                           runtimeID: runtime) else { return }
            model.rememberNewSessionFolder(chosenFolder)
            dismiss()
            model.openAgent(agentID)
        }
    }
}

extension MeetingDesk {
    /// The runtime picker's Auto: the daemon chooses (`meetings/start` with no runtime).
    static let autoRuntime = ""

    /// The call apps that run now, in the list's order.
    static func runningCallApps() -> [(bundleID: String, name: String)] {
        #if DEBUG
        if let walked = walkCallApps { return walked }
        #endif
        let running = Set(NSWorkspace.shared.runningApplications.compactMap(\.bundleIdentifier))
        return callApps.filter { running.contains($0.bundleID) }
    }

    #if DEBUG
    /// A walk's call apps, as no call app runs on a scratch Mac.
    nonisolated(unsafe) static var walkCallApps: [(bundleID: String, name: String)]?
    #endif
}

/// The calendar event on now, for New Meeting's title (EventKit, on this Mac: no network).
/// A scratch copy of the app never reads the person's calendar: a walk gives its own event.
enum MeetingCalendar {
    struct Event: Sendable {
        let title: String
        let start: Date
        let end: Date
        let attendees: [String]
    }

    #if DEBUG
    nonisolated(unsafe) static var walked: Event?
    #endif

    @MainActor
    static func now() async -> Event? {
        #if DEBUG
        if let walked { return walked }
        #endif
        guard WindowDefaults.scratchDomain == nil else { return nil }
        let store = EKEventStore()
        switch EKEventStore.authorizationStatus(for: .event) {
        case .fullAccess: break
        case .notDetermined:
            guard (try? await store.requestFullAccessToEvents()) == true else { return nil }
        default: return nil
        }
        let now = Date()
        let predicate = store.predicateForEvents(withStart: now.addingTimeInterval(-15 * 60),
                                                 end: now.addingTimeInterval(15 * 60), calendars: nil)
        // The one on now, else the next to start within a quarter hour; never an all-day one.
        let events = store.events(matching: predicate).filter { !$0.isAllDay }
        let pick = events.first { $0.startDate <= now && $0.endDate > now }
            ?? events.filter { $0.startDate > now }.min { $0.startDate < $1.startDate }
        guard let pick else { return nil }
        let attendees = (pick.attendees ?? []).filter { !$0.isCurrentUser }.compactMap(\.name)
        return Event(title: pick.title ?? "", start: pick.startDate, end: pick.endDate, attendees: attendees)
    }
}
