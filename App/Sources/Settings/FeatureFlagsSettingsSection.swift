import AgentsKitCore
import SwiftUI

/// Settings ▸ This App ▸ Feature Flags (#6254, epic-lifecycle stage 6, #6253): a switch for
/// each feature in work (`FeatureFlag`, from `Packages/Wire/feature-flags.json`). Each flag is
/// the person's, kept on the Mac's host with their other display choices, so it is the same on
/// every client of theirs. The web page has the same section; the Remote reads the flags but
/// has no section (tier Mac + web).
struct FeatureFlagsSettingsSection: View {
    static let title = "Feature Flags"
    /// The words under the section, as the web page's (`featureFlagsFooter`).
    static let footer = "Try a feature that is not ready yet. A feature can change or go away while it is in work. "
        + "The same on every client of yours."
    /// What the section says when no feature is in work, as the web page's.
    static let none = "No feature is in work now."

    var body: some View {
        Section {
            if FeatureFlag.allCases.isEmpty {
                Text(Self.none).foregroundStyle(.secondary)
            }
            ForEach(FeatureFlag.allCases, id: \.self) { FeatureFlagToggle(flag: $0) }
        } header: {
            Text(Self.title)
        } footer: {
            Text(Self.footer)
                .appText(.fine)
                .foregroundStyle(.secondary)
        }
        .paperListRow()
    }
}

/// One flag's switch: its title, and what it turns on under it.
private struct FeatureFlagToggle: View {
    let flag: FeatureFlag
    @AppStorage private var on: Bool

    init(flag: FeatureFlag) {
        self.flag = flag
        _on = AppStorage(wrappedValue: false, flag.key, store: WindowDefaults.store)
    }

    var body: some View {
        // The switch keeps its title as its own label (a two-line label left the switch unnamed
        // for VoiceOver), with the line under it.
        VStack(alignment: .leading, spacing: 2) {
            Toggle(flag.title, isOn: $on)
            Text(flag.about)
                .appText(.fine)
                .foregroundStyle(.secondary)
        }
    }
}
