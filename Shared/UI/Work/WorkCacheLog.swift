import Foundation
import os

/// The `work-cache` log of Work's client cache (#6353): what a page drew from the kept answer, in how long, and each
/// ask of a forge. A walk reads it to show that a page seen before opens with no forge call
/// (`log stream --predicate 'category == "work-cache"'`).
enum WorkCacheLog {
    static let log = Logger(subsystem: "com.alexecollins.agents", category: "work-cache")

    static func note(_ line: String) {
        log.notice("\(line, privacy: .public)")
    }

    /// Milliseconds since `start`, for a line.
    static func ms(since start: ContinuousClock.Instant) -> Int {
        let took = ContinuousClock.now - start
        return Int(took.components.seconds * 1000 + took.components.attoseconds / 1_000_000_000_000_000)
    }
}
