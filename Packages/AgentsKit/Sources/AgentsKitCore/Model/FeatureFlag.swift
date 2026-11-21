import Foundation

/// A feature in work, drawn only for a person who turns its flag on (epic-lifecycle stage 6,
/// #6253). The flags a person turns on are one display choice, `features` (comma-separated
/// names), kept with their other choices on the host in `~/.agents/settings/display.json`
/// and mirrored into each client's defaults, so a flag follows the person, not the machine.
/// #6254 gives the list a Settings section; until then, a person turns one on with the host's
/// `preferences/set` (`{"features": "commit-trailers"}`), which every client then mirrors.
public enum FeatureFlag: String, CaseIterable, Sendable {
    /// The Commit trailers group of Agents ▸ Policy (#6373, epic #6365).
    case commitTrailers = "commit-trailers"

    /// The flags `value` (the display choice) turns on; a name this build does not know is left out.
    public static func on(in value: String?) -> Set<FeatureFlag> {
        Set((value ?? "").split(separator: ",").compactMap {
            FeatureFlag(rawValue: $0.trimmingCharacters(in: .whitespaces))
        })
    }
}
