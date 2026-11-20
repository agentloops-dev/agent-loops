import Foundation

/// The daily checks' one control (#3355, #5853), for all folders: on or off, and how often they
/// run, every few hours from midnight. The home folder's ready-made Daily checks workflow keeps
/// it, so a hand edit and the control agree. `dailyChecks/settings` reads it,
/// `dailyChecks/setSettings` changes it, and `dailyChecks/changed` says when it changed. It was
/// the daily summary's control (#3351, #3395) until #5853.
public struct DailyChecksSettings: Codable, Sendable, Hashable {
    /// The Daily checks workflow is on.
    public var enabled: Bool
    /// How many hours apart the checks run, from midnight.
    public var every: Int

    public init(enabled: Bool = false, every: Int = 4) {
        self.enabled = enabled
        self.every = every
    }

    /// How often the checks can run: each divides the day evenly.
    public static let checksEveryChoices = [2, 3, 4, 6, 8, 12]

    /// How often, as a person reads it, such as "Every 4 hours".
    public static func everyLabel(_ hours: Int) -> String { "Every \(hours) hours" }

    /// On or off, as a person reads it.
    public static func stateLabel(_ enabled: Bool) -> String { enabled ? "On" : "Off" }
}
