import Foundation

/// One Work change a person makes (#6354, epic #6348): what Work's cache shows at once, what the forge is asked, how
/// the change goes back when the forge refuses it, and the footer's words.
///
/// Milestone and parent are not here yet: no Mac control sets them, and no forge write sets a milestone.
public enum WorkChange: Sendable, Hashable {
    /// high, medium or low; nil for none (`forge/priority`).
    case priority(String?)
    /// On hold or not (`forge/hold`).
    case hold(Bool)
    /// A label added or taken off (`forge/label`).
    case label(String, add: Bool)
    /// Closed, or open again (`forge/close`).
    case close(reopen: Bool)
    /// A move of a tracker's workflow, as Jira's (`forge/close` with a transition).
    case transition(DaemonAPI.ForgeTransition)
    /// A person assigned or taken off (`forge/assign`).
    case assignee(String, add: Bool)

    /// The forge's write for it.
    public var method: String {
        switch self {
        case .priority: DaemonAPI.Method.forgePriority
        case .hold: DaemonAPI.Method.forgeHold
        case .label: DaemonAPI.Method.forgeLabel
        case .close, .transition: DaemonAPI.Method.forgeClose
        case .assignee: DaemonAPI.Method.forgeAssign
        }
    }

    /// The write's request for `node`.
    public func request(_ node: DaemonAPI.LineageNode) -> any Encodable & Sendable {
        switch self {
        case .priority(let priority): DaemonAPI.ForgePriorityRequest(node: node, priority: priority)
        case .hold(let on): DaemonAPI.ForgeHoldRequest(node: node, on: on)
        case let .label(name, add): DaemonAPI.ForgeLabelRequest(node: node, add: add ? [name] : nil, remove: add ? nil : [name])
        case .close(let reopen): DaemonAPI.ForgeCloseRequest(node: node, reopen: reopen ? true : nil)
        case .transition(let move): DaemonAPI.ForgeCloseRequest(node: node, transition: move.id)
        case let .assignee(login, add): DaemonAPI.ForgeAssignRequest(node: node, add: add ? [login] : nil, remove: add ? nil : [login])
        }
    }

    /// The item as the change makes it, before the forge answers.
    public func apply(to item: inout DaemonAPI.ForgeItem) {
        switch self {
        case .priority(let priority):
            item.priority = priority
        case .hold(let on):
            item.onHold = on ? true : nil
        case let .label(name, add):
            item.labels.removeAll { $0.name.caseInsensitiveCompare(name) == .orderedSame }
            if add { item.labels.append(DaemonAPI.ForgeLabel(name: name)) }
        case .close(let reopen):
            item.state = reopen ? "open" : "closed"
            item.stateReason = reopen ? nil : "completed"
        case .transition(let move):
            item.status = move.name
            if move.category == "done" { item.state = "closed" } else if item.state == "closed" { item.state = "open" }
        case let .assignee(login, add):
            item.assignees.removeAll { $0 == login }
            if add { item.assignees.append(login) }
        }
    }

    /// The fields the change touched, back as they were in `old`: a change of another field since stays.
    public func restore(from old: DaemonAPI.ForgeItem, into item: inout DaemonAPI.ForgeItem) {
        switch self {
        case .priority: item.priority = old.priority
        case .hold: item.onHold = old.onHold
        case .label: item.labels = old.labels
        case .close, .transition:
            item.state = old.state
            item.stateReason = old.stateReason
            item.status = old.status
            item.closedAt = old.closedAt
        case .assignee: item.assignees = old.assignees
        }
    }

    /// "set #6311 to High", "close #12", "add the label bug to #12": what the footer says the change was.
    public func words(_ ref: String) -> String {
        let short = WorkList.shortRef(ref)
        switch self {
        case .priority(let priority): return "set \(short) to \(WorkPlan.priorityWords(priority) ?? "no priority")"
        case .hold(let on): return on ? "put \(short) on hold" : "take \(short) off hold"
        case let .label(name, add): return add ? "add the label \(name) to \(short)" : "take the label \(name) off \(short)"
        case .close(let reopen): return reopen ? "reopen \(short)" : "close \(short)"
        case .transition(let move): return "set \(short) to \(move.name)"
        case let .assignee(login, add): return add ? "assign \(login) to \(short)" : "take \(login) off \(short)"
        }
    }

    /// "Saving #6311…": the footer while the write runs.
    public static func savingWords(_ ref: String) -> String { "Saving \(WorkList.shortRef(ref))…" }

    /// "Could not set #6311 to High: GitHub said 403 (no write access). Changed back.": the footer when the forge
    /// refused the change and it went back.
    public func failedWords(_ ref: String, said: String) -> String {
        let why = said.trimmingCharacters(in: .whitespacesAndNewlines)
        let reason = why.isEmpty ? "" : ": " + (why.hasSuffix(".") ? String(why.dropLast()) : why)
        let line = words(ref)
        return "Could not \(line)\(reason). Changed back."
    }
}
