/// How a machine updates Agent Loops by itself, in words: Settings ▸ Machines ▸ This Mac ▸ Updates
/// (#2178) and a machine's page (#6114) on the Mac, and the same on the web page
/// (`Web/src/model/selfUpdate.ts`).
public enum SelfUpdateWords {
    /// The switch's label, and a machine page's row.
    public static let automatic = "Update Agent Loops automatically"

    /// The row's value: a machine that cannot update itself does not, whatever its switch says.
    public static func value(_ update: DaemonAPI.SelfUpdate) -> String {
        update.autoUpdate && update.state != .cannot ? "On" : "Off"
    }

    /// Where its update is.
    public static func line(_ update: DaemonAPI.SelfUpdate) -> String {
        let release = update.release ?? ""
        switch update.state {
        case .upToDate: return "Up to date."
        case .off: return release.isEmpty ? "Off." : "Off. Your control plane runs \(release)."
        case .downloading: return "Downloading \(release)…"
        case .waiting: return "\(release) is ready. It installs when no agent is in a turn."
        case .restarting: return "Installed \(release). Restarting…"
        case .failed, .cannot: return update.message ?? "Agent Loops cannot update itself."
        }
    }

    /// Whether the line is in the failure tint.
    public static func failed(_ update: DaemonAPI.SelfUpdate) -> Bool { update.state == .failed }

    /// The line when the machine did not answer.
    public static func noAnswer(_ name: String) -> String { "\(name) can’t be asked about its updates now." }
}
