import Foundation

/// A secret a machine keeps for itself (#5122): an agent saved it there (`ask_secret`,
/// `capture_secret`, `generate_secret`) or a connector's form set it. Every session's agent on
/// that machine can name it. `secrets/localList` gives them by name, never a value. It is not
/// one of the person's secrets (Settings ▸ You ▸ Secrets, from the control plane) and not a
/// session's own (`SessionSecret`).
public struct LocalSecret: Codable, Sendable, Hashable, Identifiable {
    public var name: String
    public var setAt: Date
    /// Its newest operation (#4158).
    public var lastOp: DaemonAPI.SecretLastOp?
    /// Always false: it is on that machine only.
    public var backedUp: Bool?
    /// Ask before use (#4014); nil is off.
    public var needsApproval: Bool?
    /// What it is for (#4167).
    public var description: String?

    public var id: String { name }
    public var asksBeforeUse: Bool { needsApproval == true }

    public init(name: String, setAt: Date, lastOp: DaemonAPI.SecretLastOp? = nil, backedUp: Bool? = nil,
                needsApproval: Bool? = nil, description: String? = nil) {
        self.name = name
        self.setAt = setAt
        self.lastOp = lastOp
        self.backedUp = backedUp
        self.needsApproval = needsApproval
        self.description = description
    }
}

public struct LocalSecrets: Codable, Sendable, Hashable {
    public var secrets: [LocalSecret]
    /// When the machine's key for its secrets does not read (#6632): what that stops and what to do, in the host's words.
    /// Nil when the key reads. Status ▸ Secrets shows it at the top.
    public var keyProblem: String?

    public init(secrets: [LocalSecret], keyProblem: String? = nil) {
        self.secrets = secrets
        self.keyProblem = keyProblem
    }
}

/// secrets/localRemove's request (#6335): one secret a machine keeps for itself, taken out of its store.
public struct LocalSecretRemoveRequest: Codable, Sendable, Hashable {
    public var name: String

    public init(name: String) {
        self.name = name
    }
}

/// The words of Settings ▸ You ▸ Secrets ▸ On This Machine (#5122); the web page's are the same,
/// in `Web/src/model/localSecrets.ts`.
public enum LocalSecretWords {
    public static let header = "On This Machine"
    /// Under Remove's question (#6335).
    public static let removeMessage = "It is taken out of this machine's store. Agents here can no longer use it."
    /// When the machine's list does not load (#6335): its secrets are not hidden without a word.
    public static func didNotLoad(_ why: String) -> String {
        "The secrets on this machine did not load: \(why)"
    }
}

extension SecretInfoWords {
    /// The line under a secret a machine keeps for itself: "Set 1 h ago · Used 20 min ago".
    public static func line(_ secret: LocalSecret, now: Date = Date(), timeZone: TimeZone = .current) -> [Part] {
        var parts: [Part] = []
        if let set = set(created: nil, updated: secret.setAt, now: now, timeZone: timeZone) {
            parts.append(set)
        }
        parts.append(used(secret.lastOp, now: now, timeZone: timeZone))
        return parts
    }
}
