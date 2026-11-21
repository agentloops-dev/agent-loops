import AgentsKitCore
import Foundation

/// The person's `work-cache` flag (#6353): Work's pages read the client cache, not the local copy.
enum WorkCacheFlag {
    static var isOn: Bool { WindowDefaults.store.bool(forKey: "flag:work-cache") }
}
