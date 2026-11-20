import Foundation

/// Something an agent left running while it went on (057): a shell, a workflow, a
/// monitor, or a subagent of its own.
///
/// Sent by a runtime that the app told it can show these, through the JetBrains "AIR"
/// opt-in (`asyncTasks`, `nativeSubagentSessions`). Keyed on what arrives, never on which
/// runtime sent it: Claude and Codex speak the same five updates.
///
/// The running ones are drawn over the prompt, each shell with Stop. A finished one is
/// kept a while so a subagent's steps can still be opened after it is done.
public struct BackgroundItem: Codable, Hashable, Sendable, Identifiable {
    /// The runtime's own id: `asyncTaskId` for a task, `subagentSessionId` for a
    /// subagent. What Stop sends back.
    public var id: String
    public var kind: Kind
    /// The runtime's short name for it: "Print a tick every second", "Count files".
    public var name: String
    /// A task's type as the runtime says it: `shell`, `workflow`, `monitor`, `task`.
    /// Nil for a subagent.
    public var taskType: String?
    /// A task's description (for a shell, its command), or what a subagent was asked.
    public var detail: String?
    /// A shell's command line, when the tool call that started it said. The row leads
    /// with the runtime's plain name for it and keeps this for hovering (Alex, 2026-09-26):
    /// `async_task_spawned` carries only the description, so it is found on the call.
    public var command: String?
    public var state: State
    /// Whether the runtime says Stop works on it. A task says so itself; a subagent says
    /// so with `capabilities.cancel`, which no runtime sends yet (research, 057).
    public var canStop: Bool
    /// Stop was pressed and the runtime has not answered yet.
    public var isStopping: Bool
    /// The subagent that started it, when it was not the agent itself.
    public var parentID: String?
    /// The tool call it came from, so the card in the chat can say it runs on.
    public var toolCallID: String?
    /// Where a task writes what it prints. The runtime's own file.
    public var outputFilePath: String?
    /// The latest line the runtime gave about it, and the tool it last used.
    public var summary: String?
    public var lastToolName: String?
    public var startedAt: Date
    public var endedAt: Date?

    public enum Kind: String, Codable, Hashable, Sendable {
        case task
        case subagent
    }

    public enum State: String, Codable, Hashable, Sendable {
        case running
        case paused
        case completed
        case failed
        /// Stopped: a task by Stop, a subagent by the agent being stopped.
        case stopped
        /// The runtime went, and took it with it: the app lets a runtime go when its
        /// turn ends (Alex, 2026-09-26), so this is how a shell left running ends. Ours,
        /// not the runtime's.
        case disconnected

        public var isRunning: Bool { self == .running || self == .paused }
    }

    public init(id: String, kind: Kind, name: String, taskType: String? = nil,
                detail: String? = nil, state: State = .running, canStop: Bool = false,
                isStopping: Bool = false, parentID: String? = nil, toolCallID: String? = nil,
                outputFilePath: String? = nil, summary: String? = nil, lastToolName: String? = nil,
                startedAt: Date = Date(), endedAt: Date? = nil) {
        self.id = id
        self.kind = kind
        self.name = name
        self.taskType = taskType
        self.detail = detail
        self.state = state
        self.canStop = canStop
        self.isStopping = isStopping
        self.parentID = parentID
        self.toolCallID = toolCallID
        self.outputFilePath = outputFilePath
        self.summary = summary
        self.lastToolName = lastToolName
        self.startedAt = startedAt
        self.endedAt = endedAt
    }

    public var isRunning: Bool { state.isRunning }

    /// Stop is offered: it runs, the runtime says Stop works on it, and it is not
    /// already on its way out.
    public var offersStop: Bool { isRunning && canStop && !isStopping }

    public var isShell: Bool { kind == .task && taskType == "shell" }
}

/// The words for background work, the same on the Mac and the phone.
public enum BackgroundWords {
    /// "1 shell, 1 subagent in the background". Nil when nothing runs.
    public static func mark(_ items: [BackgroundItem]) -> String? {
        let running = items.filter(\.isRunning)
        guard !running.isEmpty else { return nil }
        let shells = running.filter(\.isShell).count
        let subagents = running.filter { $0.kind == .subagent }.count
        let others = running.count - shells - subagents
        var parts: [String] = []
        if shells > 0 { parts.append(shells == 1 ? "1 shell" : "\(shells) shells") }
        if subagents > 0 { parts.append(subagents == 1 ? "1 subagent" : "\(subagents) subagents") }
        if others > 0 { parts.append(others == 1 ? "1 task" : "\(others) tasks") }
        return parts.joined(separator: ", ") + " in the background"
    }

    /// The symbol for one: a subagent is a person, anything else a prompt.
    public static func symbol(_ item: BackgroundItem) -> String {
        item.kind == .subagent ? "person.crop.circle.badge.clock" : "apple.terminal"
    }

    /// What kind of thing it is, in a word.
    public static func noun(_ item: BackgroundItem) -> String {
        if item.kind == .subagent { return "Subagent" }
        switch item.taskType {
        case "shell": return "Shell"
        case "workflow": return "Workflow"
        case "monitor": return "Monitor"
        default: return "Task"
        }
    }

    /// The line the chat keeps when one ends: "Subagent “Count files” finished".
    public static func ending(_ item: BackgroundItem) -> String {
        let what = "\(noun(item)) “\(item.name)”"
        switch item.state {
        case .completed: return "\(what) finished"
        case .failed: return "\(what) failed"
        case .stopped: return "\(what) stopped"
        case .disconnected: return "\(what) ended with the turn"
        case .running, .paused: return "\(what) is running in the background"
        }
    }

    /// How it ended, in a word or two, for a row that is still listed after it has:
    /// "Stopped", "Finished". Nil while it runs.
    public static func ended(_ item: BackgroundItem) -> String? {
        switch item.state {
        case .completed: return "Finished"
        case .failed: return "Failed"
        case .stopped: return "Stopped"
        case .disconnected: return "Ended with the turn"
        case .running, .paused: return nil
        }
    }

    /// How long it has run, or ran: "0:37", "12:04", "1:02:10".
    public static func age(_ item: BackgroundItem, now: Date = Date()) -> String {
        let seconds = max(0, Int((item.endedAt ?? now).timeIntervalSince(item.startedAt)))
        let (h, m, s) = (seconds / 3600, (seconds % 3600) / 60, seconds % 60)
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, s) : String(format: "%d:%02d", m, s)
    }
}
