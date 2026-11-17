import Foundation

/// The shared work of a session search on every host, for the window and the Remote (#5572).
/// Every host is asked at once, and each answer is written as it comes: a slow or far
/// machine does not hold back the others' matches.
@MainActor
public enum SessionSearch {
    /// Ask every host for its page of matches (`page`), the sessions on the issue the words
    /// name, and the sessions whose prompts or replies hold the words, all at once.
    /// - Parameters:
    ///   - has: whether a host has a capability, so a host too old to say is not asked.
    ///   - client: the host's client, or nil when it cannot be reached.
    ///   - page: asks one host for its page of matches and keeps it.
    public static func askEveryHost(
        _ hosts: [MachineRef],
        words: String,
        into work: AgentsModel,
        has: @escaping @MainActor @Sendable (MachineRef, String) -> Bool,
        client: @escaping @MainActor @Sendable (MachineRef) -> DaemonClient?,
        page: @escaping @MainActor @Sendable (MachineRef) async -> Void
    ) async {
        let issue = SessionLabelQuery.issue(in: words)
        await withTaskGroup(of: Void.self) { group in
            for host in hosts {
                group.addTask { await page(host) }
                if let issue {
                    group.addTask { await askIssue(issue, on: host, into: work, has: has, client: client) }
                }
                group.addTask { await askSaid(words, on: host, into: work, has: has, client: client) }
            }
        }
    }

    /// The next page from each host that has more, every host at once.
    public static func askMore(
        _ next: [MachineRef: DaemonAPI.ListRequest],
        page: @escaping @MainActor @Sendable (DaemonAPI.ListRequest, MachineRef) async -> Void
    ) async {
        await withTaskGroup(of: Void.self) { group in
            for (host, request) in next {
                group.addTask { await page(request, host) }
            }
        }
    }

    /// The sessions on the issue, its sub-issues and their PRs (#3899). A host too old to say
    /// matches the #<n> label only.
    static func askIssue(
        _ issue: Int, on host: MachineRef, into work: AgentsModel,
        has: @MainActor (MachineRef, String) -> Bool, client: @MainActor (MachineRef) -> DaemonClient?
    ) async {
        guard has(host, Capability.lineageSessions1), let target = client(host),
              let on = try? await target.call(DaemonAPI.Method.lineageSessions,
                                              DaemonAPI.LineageSessionsRequest(number: issue),
                                              returning: DaemonAPI.LineageSessions.self),
              !Task.isCancelled else { return }
        work.noteIssueSessions(on)
    }

    /// What was said (#1585): the sessions whose prompts or replies hold the words. Not asked of
    /// a host too old to say (#1748).
    static func askSaid(
        _ words: String, on host: MachineRef, into work: AgentsModel,
        has: @MainActor (MachineRef, String) -> Bool, client: @MainActor (MachineRef) -> DaemonClient?
    ) async {
        guard has(host, Capability.agentsSaid1), let target = client(host),
              let said = try? await target.call(DaemonAPI.Method.agentsSaid, DaemonAPI.SaidSearchRequest(text: words),
                                                returning: DaemonAPI.SaidSearchResult.self),
              !Task.isCancelled else { return }
        for match in said.matches { work.saidLines[match.agentID] = match.line }
    }
}
