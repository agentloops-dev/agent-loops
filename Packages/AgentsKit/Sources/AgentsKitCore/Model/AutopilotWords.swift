import Foundation

/// Project Settings ▸ Autopilot (#5904, epic #5897): the words and the lines, the same as the web
/// page's `Web/src/model/autopilotSettings.ts`.
public enum AutopilotWords {
    public static let title = "Autopilot"
    public static let off = "Off"
    public static let high = "Only high priority"
    public static let on = "On: start a lead for each epic, and a lane for each ready issue"
    public static let askTitle = "Turn on Autopilot?"
    public static let askDetail = "Autopilot starts paid agent work with no press from you, within these limits."
    public static let turnOn = "Turn on"
    public static let changeLimits = "Change limits"
    public static let seeWhatWaits = "See what waits"
    public static let never = "Autopilot has not run in this folder yet."
    public static let offLine = "Autopilot is off: it starts nothing."
    public static let tooOld = "This machine's agents are too old to run Autopilot. Update them first."
    public static let cannotOpen = "Agents can’t open Work ▸ Next"

    public static func title(of mode: DaemonAPI.AutopilotMode) -> String {
        switch mode {
        case .off: off
        case .high: high
        case .on: on
        }
    }

    /// Turning it on, or to high priority only, from off asks once.
    public static func needsConfirm(from: DaemonAPI.AutopilotMode, to: DaemonAPI.AutopilotMode) -> Bool {
        from == .off && to != .off
    }

    /// "Within this folder's limits: 3 running, 5 queued, 5 in total".
    public static func limitsLine(_ helpers: String) -> String { "Within this folder's limits: \(helpers)" }

    /// "Last run 2 min ago · 1 started, 4 waiting".
    public static func lastRunLine(_ status: DaemonAPI.AutopilotStatus, now: Date = Date()) -> String {
        guard let ran = status.lastRunAt else { return never }
        func count(_ state: DaemonAPI.AutopilotRow.State) -> Int { status.rows.filter { $0.state == state }.count }
        var parts = ["\(count(.started)) started", "\(count(.waiting)) waiting"]
        if count(.paused) > 0 { parts.append("\(count(.paused)) paused") }
        return "Last run \(LastUpdate.age(since: ran, now: now)) · \(parts.joined(separator: ", "))"
    }
}
