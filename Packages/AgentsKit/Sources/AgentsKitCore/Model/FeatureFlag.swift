import Foundation

/// Where a person's feature flags are kept, and how a client reads one (#6254, epic-lifecycle
/// stage 6, #6253).
///
/// The list of flags in work is `FeatureFlag`, made from `Packages/Wire/feature-flags.json`.
/// Each flag is one of the person's display settings, `flag:<name>`, a bool, kept on the host
/// in `~/.agents/settings/display.json` with their other choices (`preferences/get`, `/set`,
/// `/changed`), so a flag follows the person to each of their clients, not the machine.
/// Each client mirrors it into its own defaults (`DisplayPreferenceMirror`), so a view reads
/// one flag with `@AppStorage(FeatureFlag.x.key, store: …)` and a model with `isOn(in:)`.
///
/// The store takes any name, not only the list's: a capability's toggle (#6052) is kept the
/// same way, by its name, with its own default.
public enum FeatureFlags {
    /// The display setting a flag or a toggle of that name is kept under: `flag:<name>`, with
    /// each dot of a capability's name a colon. A key has no dot because `@AppStorage` hears a
    /// key's changes by key-value observing, which reads a dot as a key path and hears nothing.
    public static func key(_ name: String) -> String { "flag:" + name.replacingOccurrences(of: ".", with: ":") }

    /// Whether the person has the flag or toggle `name` on; `fallback` when they have not chosen.
    public static func isOn(_ name: String, default fallback: Bool = false,
                            in preferences: DaemonAPI.DisplayPreferences) -> Bool {
        preferences[key(name)]?.boolValue ?? fallback
    }

    /// The change that turns `name` on or off. A choice that is the default takes the key
    /// away, so display.json keeps only what the person changed.
    public static func change(_ name: String, on: Bool, default fallback: Bool = false) -> DaemonAPI.DisplayPreferences {
        [key(name): on == fallback ? .null : .bool(on)]
    }
}

extension FeatureFlag {
    /// The display setting this flag is kept under, `flag:<name>`; off when it is absent.
    public var key: String { FeatureFlags.key(rawValue) }

    /// Whether the person has this flag on, from a client's mirror of their display settings.
    public func isOn(in defaults: UserDefaults) -> Bool { defaults.bool(forKey: key) }

    /// Whether the person has this flag on, from the host's map.
    public func isOn(in preferences: DaemonAPI.DisplayPreferences) -> Bool {
        FeatureFlags.isOn(rawValue, in: preferences)
    }
}
