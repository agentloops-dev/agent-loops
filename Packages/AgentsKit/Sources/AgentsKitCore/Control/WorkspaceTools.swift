import Foundation

/// The app's workspace tools for agents (#691 US6, research R11): list, start, stop, make and
/// delete, relayed by the agent's daemon to its control plane on channel 0, which answers
/// them as a client's would. Making and deleting ask the person first, on the daemon.
public extension DaemonAPI.Method {
}

public extension DaemonAPI {

    /// `machine/machines`: a control plane method, for an agent on this host.
    struct MachineWorkspaceCall: Codable, Sendable, Hashable {
        /// Who asked, for the control plane's log: "Claude (fix-login)".
        public var agent: String
        public var method: String
        public var params: JSONValue
        public init(agent: String, method: String, params: JSONValue) {
            self.agent = agent
            self.method = method
            self.params = params
        }
    }

    /// What `list_machines` shows an agent (US6 AS1).
    struct AgentMachines: Codable, Sendable, Hashable {
        public struct Workspace: Codable, Sendable, Hashable {
            public var id: String
            public var name: String
            public var state: String
            /// The host it is linked to, by name, and its state there.
            public var host: String?
            public var hostState: String?
            public var cost: String?
            /// Why it failed, on a make that failed (#4706).
            public var reason: String?
            public init(id: String, name: String, state: String, host: String? = nil, hostState: String? = nil,
                        cost: String? = nil, reason: String? = nil) {
                self.id = id
                self.name = name
                self.state = state
                self.host = host
                self.hostState = hostState
                self.cost = cost
                self.reason = reason
            }
        }

        public struct Provider: Codable, Sendable, Hashable {
            public var id: ProviderID
            public var name: String
            public var canMake: Bool
            public var canDelete: Bool
            /// The New Machine choices, when it can make one.
            public var options: JSONValue?
            public var workspaces: [Workspace]
            public init(id: ProviderID, name: String, canMake: Bool, canDelete: Bool, options: JSONValue? = nil,
                        workspaces: [Workspace]) {
                self.id = id
                self.name = name
                self.canMake = canMake
                self.canDelete = canDelete
                self.options = options
                self.workspaces = workspaces
            }
        }

        public var providers: [Provider]
        public init(providers: [Provider]) { self.providers = providers }
    }
}
