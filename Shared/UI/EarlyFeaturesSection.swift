import AgentsKitCore
import SwiftUI

/// The client this view is drawn on, for a pilot's clients (#6052).
extension MaturityClient {
    #if os(macOS)
    static let here = MaturityClient.mac
    #else
    static let here = MaturityClient.remote
    #endif
}

/// A feature's maturity badge (#6052): **Pilot**, **Alpha** or **Beta** in a pill, with its help
/// line; nothing at GA. It goes where a person goes into the feature (its sidebar row, menu
/// item, Settings section title, page or sheet title). The web page's is `MaturityBadge` in
/// `Web/src/views/MaturityBadge.tsx`.
struct MaturityBadge: View {
    let level: Maturity

    /// The badge of a capability, by the list this client was built with, or as the part it runs
    /// on says it (`said`, that host's `maturity`, #6579), when it says one.
    init(capability: String, said: [String: String]? = nil) {
        level = EarlyFeatures.level(of: capability, said: said)
    }

    init(level: Maturity) { self.level = level }

    var body: some View {
        if let badge = level.badge, let help = level.help {
            Text(badge)
                .appText(.fine)
                .fontWeight(.semibold)
                .foregroundStyle(Paper.secondary)
                .padding(.horizontal, 6)
                .background(Paper.raised, in: RoundedRectangle(cornerRadius: Paper.Radius.pill))
                .overlay(RoundedRectangle(cornerRadius: Paper.Radius.pill).strokeBorder(Paper.rule))
                .help(help)
        }
    }
}

/// Settings ▸ This app ▸ Features (#6052, #6604, epic #6552): first a row for each feature
/// flag in work (`FeatureFlag`, #6254), with its title, its line and a switch, off by default and
/// with no badge; then a row for each feature that is not GA and that this client shows, with its
/// name, its badge, its help line and a switch, on by default. A feature turned off shows nowhere
/// on the person's clients. Each switch is kept on the Mac's host with their other display choices
/// (`flag:<name>`, #6254), so it is the same on every client of theirs. Drawn by the Mac window and
/// the Remote; the web page has the same section (Core).
struct EarlyFeaturesSection: View {
    static let title = "Features"
    /// The words under the section, as the web page's (`earlyFeaturesFooter`).
    static let footer = "Features that are not ready yet. A feature in work is off until you turn it on. "
        + "A pilot, alpha or beta feature is on until you turn it off; then it shows nowhere on your clients. "
        + "The same on every client of yours."
    /// What the section says with no flag and every feature GA, as the web page's.
    static let none = "Every feature is ready."

    /// What the control plane and each host say of their levels (#6579): a client older than its
    /// host shows the host's level, the lowest when they differ. Empty for none, and this client
    /// reads the list it was built with.
    var said: [[String: String]?] = []

    var body: some View {
        let rows = EarlyFeatures.rows(on: .here, said: said)
        Section {
            if FeatureFlag.allCases.isEmpty && rows.isEmpty {
                Text(Self.none).foregroundStyle(.secondary)
            }
            ForEach(FeatureFlag.allCases, id: \.self) { FeatureFlagToggle(flag: $0) }
            ForEach(rows) { EarlyFeatureToggle(feature: $0) }
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

/// One flag's switch (#6254): its title, and what it turns on under it; off by default.
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

/// One feature's switch: its name and badge, and the badge's help line under it.
private struct EarlyFeatureToggle: View {
    let feature: EarlyFeature
    @AppStorage private var on: Bool

    init(feature: EarlyFeature) {
        self.feature = feature
        _on = AppStorage(wrappedValue: EarlyFeatures.toggleDefault, feature.key, store: WindowDefaults.store)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            // A switch whose label is a stack has no name for VoiceOver, so it gets the feature's
            // name; the help line under it says the level. No Text in the stack has a label of
            // its own, and the badge is hidden: stacked labels crash AppKit.
            Toggle(isOn: $on) {
                HStack(spacing: 6) {
                    Text(feature.title)
                    MaturityBadge(level: feature.level).accessibilityHidden(true)
                }
            }
            .accessibilityLabel(feature.title)
            if let help = feature.level.help {
                Text(help)
                    .appText(.fine)
                    .foregroundStyle(.secondary)
            }
        }
    }
}
