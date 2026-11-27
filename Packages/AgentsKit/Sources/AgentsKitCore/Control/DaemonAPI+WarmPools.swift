import Foundation

/// Warm pools (#5363, #5365, #5366, epic #5353): a provider's machines kept made, joined, with
/// Agents, runtimes and the pool's projects, and signed in to nothing. New Machine's Start from
/// takes one in seconds, and the pool makes another.
public extension DaemonAPI.Method {
    /// `Empty` → `WarmPools`: the person's own pools.
    static let poolList = "pool/list"
    /// `WarmPoolSet` → `WarmPool`: makes a pool, or changes the one with `id`.
    static let poolSet = "pool/set"
    /// `WarmPoolRequest` → `Empty`: the pool and its ready machines go.
    static let poolDelete = "pool/delete"
    /// `WarmPoolClaim` → `WarmPoolClaimed`: New Machine's Start from a warm pool.
    static let poolClaim = "pool/claim"
}

public extension DaemonAPI {
    /// Whose pool it is. Only `user` is built.
    enum WarmPoolScope: String, Codable, Sendable, Hashable {
        case user, project, org
    }

    /// `keeping`, `off-hours`, `stopped` (three makes failed in a row) or `empty` (min ready 0).
    enum WarmPoolState: String, Codable, Sendable, Hashable {
        case keeping
        case offHours = "off-hours"
        case stopped
        case empty
    }

    /// When a pool runs: these weekdays (0 Sunday to 6 Saturday), from `from` to `to` as HH:MM
    /// in `timezone`. A pool with no hours runs every day.
    struct WarmPoolHours: Codable, Sendable, Hashable {
        public var days: [Int]
        public var from: String
        public var to: String
        public var timezone: String?

        public init(days: [Int], from: String, to: String, timezone: String? = nil) {
            self.days = days
            self.from = from
            self.to = to
            self.timezone = timezone
        }
    }

    /// One machine a pool holds, for a line on its card (#5366).
    struct WarmPoolMachine: Codable, Sendable, Hashable, Identifiable {
        public enum State: String, Codable, Sendable, Hashable {
            case ready
            case settingUp = "setting-up"
            case failed
        }

        public var name: String
        public var state: State
        public var madeAt: Date
        /// What its set-up is doing now: *installing Claude*.
        public var detail: String?
        public var id: String { name }

        public init(name: String, state: State, madeAt: Date, detail: String? = nil) {
            self.name = name
            self.state = state
            self.madeAt = madeAt
            self.detail = detail
        }
    }

    /// A pool's claims in the last seven days (#5366).
    struct WarmPoolClaims: Codable, Sendable, Hashable {
        public var count: Int
        /// Claims that found no ready machine and made a new one.
        public var empty: Int
        /// The median time from a claim to the machine being ready.
        public var medianSeconds: Double?

        public init(count: Int, empty: Int, medianSeconds: Double? = nil) {
            self.count = count
            self.empty = empty
            self.medianSeconds = medianSeconds
        }
    }

    /// A warm pool, as `pool/list` says it.
    struct WarmPool: Codable, Sendable, Hashable, Identifiable {
        public var id: String
        public var name: String
        public var scope: WarmPoolScope
        public var provider: ProviderID
        public var size: String?
        public var region: String?
        public var runtimes: [String]?
        /// The project folders it sends, at their last commit: paths on the Mac that made it.
        public var projects: [String]?
        public var minReady: Int
        public var maxSize: Int
        public var target: Int
        /// Seconds after which a ready machine is replaced; 24 hours when nil.
        public var replaceAfter: Double?
        public var hours: WarmPoolHours?
        public var state: WarmPoolState
        public var ready: Int?
        public var settingUp: Int?
        public var failedMakes: Int?
        public var stoppedBecause: String?
        /// The script the pool stopped for until the person reviews it (#7157): `root` when
        /// root.sh is not the text they approved for new machines.
        public var review: String?
        /// Why a pool that is keeping makes no machine now (#6890).
        public var waitingBecause: String?
        public var machines: [WarmPoolMachine]?
        public var claims: WarmPoolClaims?

        public init(id: String, name: String, scope: WarmPoolScope = .user, provider: ProviderID, size: String? = nil,
                    region: String? = nil, runtimes: [String]? = nil, projects: [String]? = nil, minReady: Int,
                    maxSize: Int, target: Int? = nil, replaceAfter: Double? = nil, hours: WarmPoolHours? = nil,
                    state: WarmPoolState = .keeping, ready: Int? = nil, settingUp: Int? = nil, failedMakes: Int? = nil,
                    stoppedBecause: String? = nil, review: String? = nil, waitingBecause: String? = nil,
                    machines: [WarmPoolMachine]? = nil, claims: WarmPoolClaims? = nil) {
            self.id = id
            self.name = name
            self.scope = scope
            self.provider = provider
            self.size = size
            self.region = region
            self.runtimes = runtimes
            self.projects = projects
            self.minReady = minReady
            self.maxSize = maxSize
            self.target = target ?? minReady
            self.replaceAfter = replaceAfter
            self.hours = hours
            self.state = state
            self.ready = ready
            self.settingUp = settingUp
            self.failedMakes = failedMakes
            self.stoppedBecause = stoppedBecause
            self.review = review
            self.waitingBecause = waitingBecause
            self.machines = machines
            self.claims = claims
        }
    }

    /// `pool/list`'s answer.
    struct WarmPools: Codable, Sendable, Hashable {
        public var pools: [WarmPool]
        public init(pools: [WarmPool]) { self.pools = pools }
    }

    /// `pool/set`: no `id` makes a new pool.
    struct WarmPoolSet: Codable, Sendable, Hashable {
        public var id: String?
        public var name: String
        public var scope: WarmPoolScope?
        public var provider: ProviderID
        public var size: String?
        public var region: String?
        public var runtimes: [String]?
        public var projects: [String]?
        public var minReady: Int
        public var maxSize: Int
        public var replaceAfter: Double?
        public var hours: WarmPoolHours?
        /// The person approved the pool's changed root.sh (#7157): it makes again, and replaces
        /// its unclaimed machines from before.
        public var reviewed: Bool?

        public init(id: String? = nil, name: String, scope: WarmPoolScope? = nil, provider: ProviderID, size: String? = nil,
                    region: String? = nil, runtimes: [String]? = nil, projects: [String]? = nil, minReady: Int,
                    maxSize: Int, replaceAfter: Double? = nil, hours: WarmPoolHours? = nil, reviewed: Bool? = nil) {
            self.id = id
            self.name = name
            self.scope = scope
            self.provider = provider
            self.size = size
            self.region = region
            self.runtimes = runtimes
            self.projects = projects
            self.minReady = minReady
            self.maxSize = maxSize
            self.replaceAfter = replaceAfter
            self.hours = hours
            self.reviewed = reviewed
        }
    }

    /// `pool/delete`.
    struct WarmPoolRequest: Codable, Sendable, Hashable {
        public var id: String
        public init(id: String) { self.id = id }
    }

    /// `pool/claim`: a machine of this provider and size from a pool, with the person's Stop
    /// when idle (minutes; 0 is never).
    struct WarmPoolClaim: Codable, Sendable, Hashable {
        public var provider: ProviderID
        public var size: String?
        public var region: String?
        public var stopWhenIdle: Int?

        public init(provider: ProviderID, size: String? = nil, region: String? = nil, stopWhenIdle: Int? = nil) {
            self.provider = provider
            self.size = size
            self.region = region
            self.stopWhenIdle = stopWhenIdle
        }
    }

    /// What `pool/claim` gave: a pool's machine, or a new one being made, which `note` says.
    struct WarmPoolClaimed: Codable, Sendable, Hashable {
        /// The machine's host; nil for a new machine, which joins on its own.
        public var host: MachineRef?
        public var workspace: String
        public var name: String
        public var pool: String?
        public var fromPool: Bool
        public var note: String?

        public init(host: MachineRef? = nil, workspace: String, name: String, pool: String? = nil, fromPool: Bool,
                    note: String? = nil) {
            self.host = host
            self.workspace = workspace
            self.name = name
            self.pool = pool
            self.fromPool = fromPool
            self.note = note
        }
    }
}
