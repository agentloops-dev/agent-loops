import Foundation

/// How long a workflow waits after one run starts before the next may (#103).
///
/// `cooldown: 15m` in the file. Written as a number and a unit, or several of them,
/// `1h30m`: minutes, hours or days, because the clock that releases a held fire looks
/// every fifteen seconds and a cooldown in seconds would promise more than it keeps.
public enum WorkflowCooldown {
    /// The length as the file writes it: `15m`, `2h`, `1h30m`, `1d`.
    public static func fileText(_ length: TimeInterval) -> String {
        var left = Int(length / 60)
        var parts: [String] = []
        for (unit, minutes) in [("d", 24 * 60), ("h", 60), ("m", 1)] where left >= minutes {
            parts.append("\(left / minutes)\(unit)")
            left %= minutes
        }
        return parts.isEmpty ? "0m" : parts.joined()
    }

    /// The length in words: `15 minutes`, `2 hours`, `1 hour 30 minutes`, `1 day`.
    public static func words(_ length: TimeInterval) -> String {
        var left = Int(length / 60)
        var parts: [String] = []
        for (unit, minutes) in [("day", 24 * 60), ("hour", 60), ("minute", 1)] where left >= minutes {
            let count = left / minutes
            parts.append("\(count) \(unit)\(count == 1 ? "" : "s")")
            left %= minutes
        }
        return parts.joined(separator: " ")
    }
}
