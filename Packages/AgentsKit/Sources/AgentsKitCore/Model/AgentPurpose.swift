import Foundation

/// Why an agent was started, when it was not for a person's work (#858). Such an agent is in no
/// list a client sees, has only the tools of its purpose, and runs in a folder of its own. Write
/// It for Me, which started the only kind, was removed (#6623); an old record may still say it.
public enum AgentPurpose: Codable, Hashable, Sendable {
    /// Writes one draft of a provider's tool map or commands.
    case providerDraft(id: UUID)

    public var providerDraft: UUID? {
        switch self {
        case .providerDraft(let id): id
        }
    }
}
