import Foundation
import Testing

/// A `UserDefaults` that is only in memory, for a test that needs a defaults store of its
/// own (#1028).
///
/// `UserDefaults(suiteName:)` is a plist in `~/Library/Preferences`, and
/// `removePersistentDomain(forName:)` empties it but leaves the file: cfprefsd writes it
/// back even after the test deletes it. A suite per test left 11,028 domains on one Mac.
/// This one writes nothing to disk, so there is nothing to remove.
///
/// The typed getters (`data(forKey:)`, `stringArray(forKey:)`, …) read through
/// `object(forKey:)`, and the typed setters write through `set(_:forKey:)`, so these four
/// are all a store sees.
final class MemoryDefaults: UserDefaults, @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: Any] = [:]

    init() {
        super.init(suiteName: nil)!
    }

    override func object(forKey defaultName: String) -> Any? {
        lock.withLock { values[defaultName] }
    }

    override func set(_ value: Any?, forKey defaultName: String) {
        lock.withLock { values[defaultName] = value }
    }

    override func removeObject(forKey defaultName: String) {
        lock.withLock { _ = values.removeValue(forKey: defaultName) }
    }

    override func dictionaryRepresentation() -> [String: Any] {
        lock.withLock { values }
    }
}
