import AgentsKitCore
import Foundation

/// Which role a connection gets, from the code signature of the process that made it.
///
/// Every process of this account can reach the socket, so a file mode or a secret on
/// disk tells nothing apart: an agent's shell can read whatever the app can. What it
/// cannot do is be the app. The kernel names the process on the other end by audit
/// token, which a caller cannot forge, and the signature on that process says which
/// program it is and whose. The app and the bridge are control; this daemon's own
/// binary, run as a runtime's MCP helper, is an agent; Agents Host may read what its
/// window counts (#168); anything else is a stranger.
public struct RolePolicy: Sendable {
    public let role: @Sendable (_ fd: Int32) -> ConnectionRole
    /// Said once in the log, so which door is in place is never a guess.
    public let summary: String

    public init(summary: String, role: @escaping @Sendable (_ fd: Int32) -> ConnectionRole) {
        self.summary = summary
        self.role = role
    }

    /// Everything to everyone this account runs: what the socket was before roles.
    public static let open = RolePolicy(summary: "every process of this account may call everything") { _ in .control }

    /// The bundle identifiers that are control. The helper is the daemon's own binary.
    public static let controlIdentifiers = ["com.alexecollins.agents", "com.alexecollins.agents.bridge"]
    public static let helperIdentifier = "agentsd"
    /// Agents Host, whose window counts this Mac's host's folders and working agents.
    public static let hostAppIdentifier = "com.alexecollins.agents.host"

    /// Who gets what, each signed by this daemon's team, tried in order. Exact
    /// identifiers only: anything not named here, of any team, is a stranger.
    public static let signedRoles: [(role: ConnectionRole, identifiers: [String])] = [
        (.control, controlIdentifiers),
        (.agent, [helperIdentifier]),
        (.hostApp, [hostAppIdentifier]),
    ]

    /// The role a caller signed by this daemon's team as `identifier` gets: the rule
    /// the Go host's agents-peer checks, by name.
    public static func role(forSignedIdentifier identifier: String?) -> ConnectionRole {
        guard let identifier else { return .stranger }
        return signedRoles.first { $0.identifiers.contains(identifier) }?.role ?? .stranger
    }
}
