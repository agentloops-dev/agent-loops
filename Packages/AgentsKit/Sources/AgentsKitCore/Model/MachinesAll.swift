import Foundation

// Each machine's answer in one call to the control plane (#5592, epic #5281): Work's lists and
// boards, Reports' forecast and features, Events' newest page and the connections. unreached is
// the machines that did not answer; notAsked counts those past the 50 one call asks.

public extension DaemonAPI {
    /// A machine and the cursor of its next page, for forge/listAll (#5592).
    struct MachineCursor: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var cursor: String

        public init(machine: MachineRef, cursor: String) {
            self.machine = machine
            self.cursor = cursor
        }
    }

    /// forge/listAll (#5592): kind is issues or pulls; pages names the machines of a next page. label and assignee (a
    /// login) narrow each machine's list, as in forge/issues (#6383).
    struct ForgeListAllRequest: Codable, Sendable, Hashable {
        public var kind: String
        public var state: String?
        public var board: String?
        public var label: String?
        public var assignee: String?
        /// The order, as in ForgeListRequest (#6538): `priority`, else the last updated first.
        public var sort: String?
        public var limit: Int?
        public var pages: [MachineCursor]?
        /// A Refresh (#6642): each machine reads its forges, not its kept answer.
        public var fresh: Bool?

        public init(kind: String, state: String? = nil, board: String? = nil, label: String? = nil, assignee: String? = nil,
                    sort: String? = nil, limit: Int? = nil, pages: [MachineCursor]? = nil, fresh: Bool? = nil) {
            self.kind = kind
            self.state = state
            self.board = board
            self.label = label
            self.assignee = assignee
            self.sort = sort
            self.limit = limit
            self.pages = pages
            self.fresh = fresh
        }
    }

    /// events/listAll (#5592): each machine's limit, 50 when not said.
    struct EventsListAllRequest: Codable, Sendable, Hashable {
        public var limit: Int?

        public init(limit: Int? = nil) {
            self.limit = limit
        }
    }

    /// A machine's ForgeList in forge/listAll (#5592).
    struct MachineForgeList: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var machineName: String
        public var list: ForgeList

        public init(machine: MachineRef, machineName: String, list: ForgeList) {
            self.machine = machine
            self.machineName = machineName
            self.list = list
        }
    }

    /// A machine's ForgeBoards in forge/boardsAll (#5592).
    struct MachineForgeBoards: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var machineName: String
        public var boards: ForgeBoards

        public init(machine: MachineRef, machineName: String, boards: ForgeBoards) {
            self.machine = machine
            self.machineName = machineName
            self.boards = boards
        }
    }

    /// A machine's LineageForecast in lineage/forecastAll (#5592).
    struct MachineLineageForecast: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var machineName: String
        public var forecast: LineageForecast

        public init(machine: MachineRef, machineName: String, forecast: LineageForecast) {
            self.machine = machine
            self.machineName = machineName
            self.forecast = forecast
        }
    }

    /// A machine's FeatureUsage in reports/featuresAll (#5592).
    struct MachineFeatureUsage: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var machineName: String
        public var usage: FeatureUsage

        public init(machine: MachineRef, machineName: String, usage: FeatureUsage) {
            self.machine = machine
            self.machineName = machineName
            self.usage = usage
        }
    }

    /// A machine's EventsPage in events/listAll (#5592).
    struct MachineEventsPage: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var machineName: String
        public var page: EventsPage

        public init(machine: MachineRef, machineName: String, page: EventsPage) {
            self.machine = machine
            self.machineName = machineName
            self.page = page
        }
    }

    /// A machine's ConnectionsList in connections/listAll (#5592).
    struct MachineConnections: Codable, Sendable, Hashable {
        public var machine: MachineRef
        public var machineName: String
        public var connections: ConnectionsList

        public init(machine: MachineRef, machineName: String, connections: ConnectionsList) {
            self.machine = machine
            self.machineName = machineName
            self.connections = connections
        }
    }

    /// forge/listAll (#5592): each machine's forge/issues or forge/pulls.
    struct ForgeListAll: Codable, Sendable, Hashable {
        public var lists: [MachineForgeList]
        public var unreached: [UnreachedMachine]
        public var notAsked: Int

        public init(lists: [MachineForgeList] = [], unreached: [UnreachedMachine] = [], notAsked: Int = 0) {
            self.lists = lists
            self.unreached = unreached
            self.notAsked = notAsked
        }
    }

    /// forge/boardsAll (#5592): each machine's forge/boards.
    struct ForgeBoardsAll: Codable, Sendable, Hashable {
        public var boards: [MachineForgeBoards]
        public var unreached: [UnreachedMachine]
        public var notAsked: Int

        public init(boards: [MachineForgeBoards] = [], unreached: [UnreachedMachine] = [], notAsked: Int = 0) {
            self.boards = boards
            self.unreached = unreached
            self.notAsked = notAsked
        }
    }

    /// lineage/forecastAll (#5592): each machine's lineage/forecast.
    struct LineageForecastAll: Codable, Sendable, Hashable {
        public var forecasts: [MachineLineageForecast]
        public var unreached: [UnreachedMachine]
        public var notAsked: Int

        public init(forecasts: [MachineLineageForecast] = [], unreached: [UnreachedMachine] = [], notAsked: Int = 0) {
            self.forecasts = forecasts
            self.unreached = unreached
            self.notAsked = notAsked
        }
    }

    /// reports/featuresAll (#5592): each machine's reports/features.
    struct FeatureUsageAll: Codable, Sendable, Hashable {
        public var usage: [MachineFeatureUsage]
        public var unreached: [UnreachedMachine]
        public var notAsked: Int

        public init(usage: [MachineFeatureUsage] = [], unreached: [UnreachedMachine] = [], notAsked: Int = 0) {
            self.usage = usage
            self.unreached = unreached
            self.notAsked = notAsked
        }
    }

    /// events/listAll (#5592): each machine's newest events/list page.
    struct EventsListAll: Codable, Sendable, Hashable {
        public var pages: [MachineEventsPage]
        public var unreached: [UnreachedMachine]
        public var notAsked: Int

        public init(pages: [MachineEventsPage] = [], unreached: [UnreachedMachine] = [], notAsked: Int = 0) {
            self.pages = pages
            self.unreached = unreached
            self.notAsked = notAsked
        }
    }

    /// connections/listAll (#5592): each machine's connections/list.
    struct ConnectionsListAll: Codable, Sendable, Hashable {
        public var lists: [MachineConnections]
        public var unreached: [UnreachedMachine]
        public var notAsked: Int

        public init(lists: [MachineConnections] = [], unreached: [UnreachedMachine] = [], notAsked: Int = 0) {
            self.lists = lists
            self.unreached = unreached
            self.notAsked = notAsked
        }
    }
}

public extension DaemonAPI.Method {
    /// `ForgeListAllRequest` → `ForgeListAll` (#5592).
    static let forgeListAll = "forge/listAll"
    /// `Empty` → `ForgeBoardsAll` (#5592).
    static let forgeBoardsAll = "forge/boardsAll"
    /// `LineageForecastRequest` → `LineageForecastAll` (#5592).
    static let lineageForecastAll = "lineage/forecastAll"
    /// `Empty` → `FeatureUsageAll` (#5592).
    static let reportsFeaturesAll = "reports/featuresAll"
    /// `EventsListAllRequest` → `EventsListAll` (#5592).
    static let eventsListAll = "events/listAll"
    /// `Empty` → `ConnectionsListAll` (#5592).
    static let connectionsListAll = "connections/listAll"
}
