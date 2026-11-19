import Foundation

/// The sessions that work on Work's items (#5453), and the lead of an epic (#3265), of those
/// this app knows, as the web page's `model/workSessions.ts`.
public enum WorkSessions {
    /// The session that leads an epic, of those this app knows: labelled lead and the epic's short
    /// ref (#3265, or ENG-5 on Linear), as Start lead labels it; not archived; a live one first,
    /// then the newest.
    public static func lead(of epic: DaemonAPI.LineageNode, in agents: [Agent]) -> Agent? {
        let ref = WorkList.shortRef(epic.ref)
        return agents.filter { a in
            a.state != .archived && a.labels.contains { $0.value == "lead" } && a.labels.contains { $0.value == ref }
        }.max { a, b in
            a.state.holdsRuntime != b.state.holdsRuntime ? b.state.holdsRuntime : a.createdAt < b.createdAt
        }
    }

    /// The label a session that works on an item has (#5453): "#12" for an issue (ENG-5 on
    /// Linear), "P#12" for a pull request, as set_session_labels and Start Agent label it.
    public static func itemLabel(_ node: DaemonAPI.LineageNode) -> String {
        (node.kind == "pr" ? "P" : "") + WorkList.shortRef(node.ref)
    }

    /// The session that works on a row's item now (#5453), of those this app knows: labelled
    /// with the item's label and starting, running or waiting on a person. One that needs you
    /// first, then the newest; nil with none.
    public static func workingSession(on node: DaemonAPI.LineageNode, in agents: [Agent]) -> Agent? {
        let label = itemLabel(node)
        return agents.filter { a in a.state.holdsRuntime && a.labels.contains { $0.value == label } }
            .max { a, b in
                let asks = (a.state == .waitingOnUser, b.state == .waitingOnUser)
                return asks.0 != asks.1 ? asks.1 : a.createdAt < b.createdAt
            }
    }

    /// A row's session in words (#5453): "Working", or "Needs you" when it waits on a person.
    public static func workingWords(_ state: AgentState) -> String {
        state == .waitingOnUser ? "Needs you" : "Working"
    }
}
