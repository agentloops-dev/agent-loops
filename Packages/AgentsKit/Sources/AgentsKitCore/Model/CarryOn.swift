import Foundation

/// "Carry on" in the prompt bar of a chat that an error stopped.
///
/// The bar gets the words, not a send: the person reads why it stopped, then presses
/// Send or types over them. Each stop gets the words one time. When the person clears
/// them, the bar stays clear until the chat stops again.
///
/// The page has its own copy (`Web/src/model/carryOn.ts`).
public enum CarryOn {
    /// The words the bar gets.
    public static let words = "Carry on"

    /// Whether this ending is an error that another turn can get past.
    ///
    /// Not a stop that a person or an agent chose (`cancelled`, `stoppedByAgent`,
    /// `stoppedByAdmin`), nor a limit the reader set (`costLimit`). Not an ending that has
    /// its own way on (`allowanceSpent`: Continue when back; `sandboxFailed`: its card;
    /// `daemonGone`: the daemon picks it up). Not one that another turn cannot fix
    /// (`signInRefused`, `refusal`, `imported`).
    public static func follows(_ reason: EndedReason?) -> Bool {
        switch reason {
        case .maxTokens, .maxTurnRequests, .processDied, .unrecognised, .runtimeError, .rateLimited:
            return true
        default:
            return false
        }
    }

    /// Whether the agent is stopped by an error that "Carry on" can get past.
    public static func follows(_ agent: Agent) -> Bool {
        agent.state == .stopped && follows(agent.endedReason)
    }

    /// The name of one stop: the same agent stopped again is a new stop.
    public static func stop(of agent: Agent) -> String {
        "\(agent.id.uuidString)|\(agent.lastActivityAt.timeIntervalSince1970)"
    }
}

/// The stops whose prompt bar has had "Carry on", for as long as the app runs.
@MainActor
public final class CarryOnOffers {
    public static let shared = CarryOnOffers()

    private var given: Set<String> = []

    public init() {}

    /// "Carry on" for a bar that holds `typed`, when the agent's stop is an error and its
    /// bar has not had the words for this stop; else nil. Never over what is typed.
    public func words(for agent: Agent?, typed: String) -> String? {
        guard let agent, CarryOn.follows(agent),
              typed.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        let stop = CarryOn.stop(of: agent)
        guard given.insert(stop).inserted else { return nil }
        return CarryOn.words
    }
}
