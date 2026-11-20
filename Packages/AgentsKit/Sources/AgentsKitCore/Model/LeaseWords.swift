import Foundation

/// Every sentence about a lease, in one place (036).
///
/// The agent reads the tool replies and repeats them to the person (FR-014), and the
/// person reads the notes in the transcript, so the words are part of the contract
/// (`contracts/lease-tools.md`, `contracts/daemon-api.md`) and are tested as such. Here,
/// in the half both platforms hold, so a phone and a Mac cannot say it two ways.
public enum LeaseWords {
    // MARK: Pieces

    /// A local wall-clock time, "14:05". Twenty-four hours whatever the locale: these
    /// are read by agents as well as people, and "2:05" is a question.
    public static func clock(_ date: Date) -> String {
        let parts = Calendar.current.dateComponents([.hour, .minute], from: date)
        return String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
    }

    public static func ordinal(_ n: Int) -> String {
        let tens = (n / 10) % 10, units = n % 10
        let suffix = tens == 1 ? "th" : (units == 1 ? "st" : units == 2 ? "nd" : units == 3 ? "rd" : "th")
        return "\(n)\(suffix)"
    }

    /// An agent as another agent or the person reads it: its title in quotes.
    public static func agentName(_ title: String?) -> String {
        guard let title = title?.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty else {
            return "another agent"
        }
        return "\u{201C}\(title)\u{201D}"
    }

    /// Whole minutes left, rounded up, so a lease with ten seconds left says one.
    public static func minutesLeft(until end: Date, now: Date) -> Int {
        max(0, Int((end.timeIntervalSince(now) / 60).rounded(.up)))
    }

    // The briefing's paragraph (FR-015), with what the person declared (#116, #599), is the
    // Go daemon's, leasesWithDeclared in Go/internal/agentsd/brief.go (#1801).

    /// "one at a time", "3 at once".
    public static func placesWords(_ places: Int) -> String {
        places == 1 ? "one at a time" : "\(places) at once"
    }
}
