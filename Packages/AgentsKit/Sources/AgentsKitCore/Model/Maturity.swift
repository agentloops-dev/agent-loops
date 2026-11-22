import Foundation

/// How ready a feature is (#6052, epic #6552): the level of its capability. A capability that
/// `Packages/Wire/maturity.json` does not list is `ga`, and shows no badge.
public enum Maturity: String, CaseIterable, Sendable, Comparable {
    /// A trial with a few people. It can change or go away, and can be on some clients only.
    case pilot
    /// Works, but parts are missing and it can change much.
    case alpha
    /// Complete, and we still look for faults.
    case beta
    /// Ready. The default.
    case ga

    public static func < (lhs: Maturity, rhs: Maturity) -> Bool { lhs.order < rhs.order }

    private var order: Int {
        switch self {
        case .pilot: 0
        case .alpha: 1
        case .beta: 2
        case .ga: 3
        }
    }

    /// The badge's word; none at GA. The same on the three clients (`Web/src/model/maturity.ts`).
    public var badge: String? {
        switch self {
        case .pilot: "Pilot"
        case .alpha: "Alpha"
        case .beta: "Beta"
        case .ga: nil
        }
    }

    /// The badge's help line; none at GA.
    public var help: String? {
        switch self {
        case .pilot: "Pilot: this feature is a trial with a few people. It can change or go away."
        case .alpha: "Alpha: this feature works, but parts are missing and it can change much."
        case .beta: "Beta: this feature is complete, and we still look for faults."
        case .ga: nil
        }
    }
}

/// A client that draws the product (AGENTS.md, "Keeping the three clients in step").
public enum MaturityClient: String, CaseIterable, Sendable {
    case mac, remote, web
}

/// A capability that is not GA, as `Packages/Wire/maturity.json` lists it (#6052). The list is
/// `EarlyFeature.all`, made by `scripts/wire.sh gen`.
public struct EarlyFeature: Sendable, Hashable, Identifiable {
    public let capability: String
    public let level: Maturity
    /// The name of its feature, as Settings ▸ This app ▸ Early Features says it.
    public let title: String
    public let issue: Int
    /// The clients that show it during a pilot; nil for every client of its tier.
    public let clients: Set<MaturityClient>?

    public var id: String { capability }

    public init(capability: String, level: Maturity, title: String, issue: Int, clients: Set<MaturityClient>?) {
        self.capability = capability
        self.level = level
        self.title = title
        self.issue = issue
        self.clients = clients
    }

    /// The display setting its toggle is kept under: `flag:<capability>`, with dots as colons,
    /// in the store of the feature flags (#6254). On when absent.
    public var key: String { FeatureFlags.key(capability) }

    /// Whether this client shows it at all: during a pilot, only the clients it names.
    public func isOn(_ client: MaturityClient) -> Bool { clients?.contains(client) ?? true }
}

/// How a client reads the maturity of a capability, and whether it shows the feature (#6052).
public enum EarlyFeatures {
    /// A toggle is on until the person turns it off.
    public static let toggleDefault = true

    /// The level of `capability`, from the list this client was built with; GA when it is not listed.
    public static func level(of capability: String, in list: [EarlyFeature] = EarlyFeature.all) -> Maturity {
        list.first { $0.capability == capability }?.level ?? .ga
    }

    /// The rows of Settings ▸ This app ▸ Early Features on `client`: each capability that is not
    /// GA and that this client shows.
    public static func rows(on client: MaturityClient, in list: [EarlyFeature] = EarlyFeature.all) -> [EarlyFeature] {
        list.filter { $0.isOn(client) }
    }

    /// Whether `client` shows the feature of `capability`: a GA one always; one that is not GA
    /// when this client is one of its clients and the person has not turned it off. A feature
    /// that this says no to shows nowhere, as if the host did not have it.
    public static func shows(_ capability: String, on client: MaturityClient, in defaults: UserDefaults,
                             list: [EarlyFeature] = EarlyFeature.all) -> Bool {
        guard let feature = list.first(where: { $0.capability == capability }) else { return true }
        guard feature.isOn(client) else { return false }
        return defaults.object(forKey: feature.key) as? Bool ?? toggleDefault
    }

    /// The change that turns a feature on or off, for `preferences/set`: only an off is kept.
    public static func change(_ capability: String, on: Bool) -> DaemonAPI.DisplayPreferences {
        FeatureFlags.change(capability, on: on, default: toggleDefault)
    }
}
