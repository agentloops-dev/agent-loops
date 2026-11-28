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
    /// The name of its feature, as Settings ▸ This app ▸ Features says it.
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

    /// The level of `capability` as a part said it (#6579): `said` is the `maturity` of a host's
    /// `machine/hello` (its row in `machines/list`) or of `control/status`. A part that says
    /// none is older than that, so this client's own list stands in; a level this client does
    /// not know, too. A capability the part does not name is GA there.
    public static func level(of capability: String, said: [String: String]?,
                             in list: [EarlyFeature] = EarlyFeature.all) -> Maturity {
        guard let said else { return level(of: capability, in: list) }
        guard let raw = said[capability] else { return .ga }
        return Maturity(rawValue: raw) ?? level(of: capability, in: list)
    }

    /// The level of `capability` where no one part runs it, such as Settings: the lowest that
    /// the parts say (the control plane's and each host's), so a person is told the least ready
    /// one. With no part, this client's own list.
    public static func lowest(of capability: String, said parts: [[String: String]?],
                              in list: [EarlyFeature] = EarlyFeature.all) -> Maturity {
        parts.map { level(of: capability, said: $0, in: list) }.min() ?? level(of: capability, in: list)
    }

    /// The rows of Settings ▸ This app ▸ Features on `client`: each capability that is not
    /// GA and that this client shows, at the lowest level the parts say (#6579). One that every
    /// part says is GA leaves the list; one this client's list does not name has no title here, so
    /// it has no row.
    public static func rows(on client: MaturityClient, said parts: [[String: String]?] = [],
                            in list: [EarlyFeature] = EarlyFeature.all) -> [EarlyFeature] {
        list.filter { $0.isOn(client) }.compactMap { feature in
            let level = lowest(of: feature.capability, said: parts, in: list)
            guard level != .ga else { return nil }
            return EarlyFeature(capability: feature.capability, level: level, title: feature.title,
                                issue: feature.issue, clients: feature.clients)
        }
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
