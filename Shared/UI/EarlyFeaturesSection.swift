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

    /// The badge of a capability, by the list this client was built with.
    init(capability: String) { level = EarlyFeatures.level(of: capability) }

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

/// Settings ▸ This app ▸ Early Features (#6052, epic #6552): a row for each feature that is not
/// GA and that this client shows, with its name, its badge, its help line and a switch, on by
/// default. A feature turned off shows nowhere on the person's clients: the switch is kept on
/// the Mac's host with their other display choices (`flag:<capability>`, #6254). Drawn by the
/// Mac window and the Remote; the web page has the same section (Core).
struct EarlyFeaturesSection: View {
    static let title = "Early Features"
    /// The words under the section, as the web page's (`earlyFeaturesFooter`).
    static let footer = "Features that are not ready yet. Turn one off, and it shows nowhere on your clients. "
        + "The same on every client of yours."
    /// What the section says when every feature is GA, as the web page's.
    static let none = "Every feature is ready."

    var body: some View {
        let rows = EarlyFeatures.rows(on: .here)
        Section {
            if rows.isEmpty {
                Text(Self.none).foregroundStyle(.secondary)
            }
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
            // VoiceOver reads the switch by the feature's name; the help line under it says the
            // badge's level. No label over the label's own Text: stacked labels crash AppKit.
            Toggle(isOn: $on) {
                HStack(spacing: 6) {
                    Text(feature.title)
                    MaturityBadge(level: feature.level).accessibilityHidden(true)
                }
            }
            if let help = feature.level.help {
                Text(help)
                    .appText(.fine)
                    .foregroundStyle(.secondary)
            }
        }
    }
}
