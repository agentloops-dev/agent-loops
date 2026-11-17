import AgentsKitCore
import Foundation

/// The words for a chat paused by a spent allowance that can carry on by itself (#1859),
/// once for both apps. The web page says the same in `continueWhenBack.ts`.
///
/// Stopped, the line **Its allowance ran out** offers **Continue at 07:00** (or **Continue
/// when it is back** when the provider gave no time) and **Continue in new session** (#5919).
/// Waiting, it says **Continues at 07:00** in the chat and on its row, with **Cancel**, and
/// the line offers nothing. The daemon keeps the wait on `Agent.continueWhenBack`.
enum ContinueWhenBackWords {
    static let symbol = "clock.arrow.circlepath"
    static let cancel = "Cancel"
    static let help = "Sends “continue” in this chat when the allowance is back, or sooner if a turn on this runtime works."
    /// Under the line, beside Continue at (#5919).
    static let inNewSession = "Continue in new session"
    static let inNewSessionHelp = "Starts a new session on Auto that reads this one, then archives this chat."

    /// Whether the chat offers the buttons: paused by a spent allowance, and not waiting yet.
    static func offers(_ agent: Agent) -> Bool {
        agent.state == .stopped && agent.endedReason == .allowanceSpent && agent.continueWhenBack == nil
    }

    /// The last "Its allowance ran out" line in these turns, the one the buttons sit under.
    /// A stored turn is read from its outcome. Nil when that line is not loaded.
    static func allowanceLine(in turns: [ChatTurn]) -> UUID? {
        var found: UUID?
        for turn in turns {
            let items = turn.items.isEmpty ? (turn.storedOutcome ?? []) : turn.items
            for item in items {
                guard case .entry(let entry) = item,
                      case .stateChanged(.stopped, .allowanceSpent) = entry.kind else { continue }
                found = entry.id
            }
        }
        return found
    }

    /// The button: the provider's reset time when this client knows it.
    static func button(resetsAt: Date?, now: Date = Date()) -> String {
        guard let resetsAt, resetsAt > now else { return "Continue when it is back" }
        return "Continue at \(PoolWords.time(resetsAt, now: now))"
    }

    /// "Continues at 07:00", or nil for a chat that is not waiting. Also the row's grey line.
    static func line(_ agent: Agent, now: Date = Date()) -> String? {
        guard let wait = agent.continueWhenBack else { return nil }
        let time = PoolWords.time(wait.at, now: now)
        return wait.resetKnown ? "Continues at \(time)" : "Continues when it is back · tries at \(time)"
    }
}

extension ContinueWhenBackWords {
    /// The workflow page's switch (#1918), writing `continue-when-back:`.
    static let workflowSwitch = "Continue when the allowance is back"

    /// What the switch means for this workflow's runs.
    static func workflowSentence(_ on: Bool) -> String {
        on ? "A run whose allowance is spent waits, and carries on when it is back."
           : "A run whose allowance is spent stops until somebody chooses Continue."
    }
}

extension RuntimeAllowances {
    /// When the provider said this runtime's allowance resets, for the button (#1859).
    func resetsAt(_ runtimeID: String) -> Date? {
        rows.first { $0.runtimeID == runtimeID && $0.state.isOut }?.state.knownReturn
    }
}
