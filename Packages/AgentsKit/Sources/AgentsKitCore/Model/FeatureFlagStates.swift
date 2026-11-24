import Foundation
import Observation

/// Which feature flags the person has on, from a client's mirror of their display settings
/// (`DisplayPreferenceMirror`), for a model and the views that read it (#6572). A view that needs
/// one flag alone can use `@AppStorage(FeatureFlag.x.key, …)`; what a model decides (the page a
/// window opens to, whether a list is read) is asked here, so a view that draws it draws again
/// when the person turns the flag on or off on any client.
@MainActor
@Observable
public final class FeatureFlagStates {
    public private(set) var on: Set<FeatureFlag>
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored nonisolated(unsafe) private var observer: (any NSObjectProtocol)?

    /// `defaults` is the store the client's mirror writes.
    public init(defaults: UserDefaults) {
        self.defaults = defaults
        on = Self.read(defaults)
        observer = NotificationCenter.default.addObserver(
            forName: UserDefaults.didChangeNotification, object: defaults, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.readAgain() }
        }
    }

    deinit {
        if let observer { NotificationCenter.default.removeObserver(observer) }
    }

    public func isOn(_ flag: FeatureFlag) -> Bool { on.contains(flag) }

    private func readAgain() {
        let now = Self.read(defaults)
        if now != on { on = now }
    }

    private static func read(_ defaults: UserDefaults) -> Set<FeatureFlag> {
        Set(FeatureFlag.allCases.filter { $0.isOn(in: defaults) })
    }
}
