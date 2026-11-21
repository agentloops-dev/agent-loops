import Foundation

// Events and waiting (042), kept in a file of its own so the lanes building beside it
// do not collide in DaemonAPI.swift. contracts/daemon-api.md is the reference.

public extension DaemonAPI.Method {
    /// A page of the log and who is waiting, for the Mac's page and the phone's list.
    static let eventsList = "events/list"
    /// The person cancelling an agent's wait, from the Mac, the phone or the web page.
    static let eventsCancelWait = "events/cancelWait"
    /// The window telling the daemon a server went offline or came back. The window
    /// holds the servers' connections, so it is the only one that knows.
    static let eventsServer = "events/server"
}

public extension DaemonAPI.Notification {
    /// A new event, a repeat or consequence added to one, or a change to who is waiting.
    static let eventsChanged = "events/changed"
}

public extension DaemonAPI.Failure {
}

public extension DaemonAPI {

    struct EventsListRequest: Codable, Sendable, Hashable {
        public var before: EventPosition?
        public var limit: Int
        /// One scope, or all when nil.
        public var scope: EventScope?
        /// Some of the page's capsules, or all when nil.
        public var groups: [EventGroup]?

        public init(before: EventPosition? = nil, limit: Int = EventLog.pageLimit,
                    scope: EventScope? = nil, groups: [EventGroup]? = nil) {
            self.before = before
            self.limit = limit
            self.scope = scope
            self.groups = groups
        }

        /// A page narrowed as the filter says, before `before` when given.
        public init(before: EventPosition? = nil, _ filter: EventFilter) {
            self.init(before: before, scope: filter.scope,
                      groups: filter.groups.isEmpty ? nil : EventGroup.allCases.filter(filter.groups.contains))
        }
    }

    struct CancelWaitRequest: Codable, Sendable, Hashable {
        public var agentID: UUID
        public init(agentID: UUID) { self.agentID = agentID }
    }

    /// One agent in the page's Waiting now strip.
    struct WaitingAgent: Codable, Sendable, Hashable, Identifiable {
        public var agentID: UUID
        public var title: String
        public var folder: URL
        public var status: WaitStatus

        public var id: UUID { agentID }
    }

    struct EventsPage: Codable, Sendable, Hashable {
        /// Newest first.
        public var events: [Event]
        public var waiting: [WaitingAgent]
        public var hasMore: Bool
        /// This machine's producer ref (#4732), as its own events say it.
        public var producer: String?

        public init(events: [Event], waiting: [WaitingAgent], hasMore: Bool, producer: String? = nil) {
            self.events = events
            self.waiting = waiting
            self.hasMore = hasMore
            self.producer = producer
        }
    }

    /// What `events/changed` carries: the event that is new or changed, if one is, and
    /// the whole Waiting now strip, which is small.
    struct EventsChange: Codable, Sendable, Hashable {
        public var event: Event?
        public var waiting: [WaitingAgent]
    }

    /// `events/server`: one server, by the name the person gave it, gone or back.
    struct ServerReachabilityChange: Codable, Sendable, Hashable {
        public var server: String
        public var online: Bool

        public init(server: String, online: Bool) {
            self.server = server
            self.online = online
        }
    }
}
