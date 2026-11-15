import Foundation
import Testing
@testable import AgentsKitCore

/// The Dashboard's board (#5534): a row per work item with its sessions, its worst mark, its
/// section, its pull request and checks, and the epic cards over them.
@Suite("Dashboard board")
struct DashboardTests {
    typealias Node = DaemonAPI.LineageNode
    let now = Date(timeIntervalSince1970: 1_793_000_000)
    let folder = URL(fileURLWithPath: "/tmp/dash-repo")
    var calendar: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    func agent(_ title: String, labels: [String] = [], state: AgentState = .running, ended: EndedReason? = nil,
               at: Date? = nil, archivedAt: Date? = nil, started: Date? = nil) -> Agent {
        Agent(runtimeID: "claude", cwd: folder, title: title,
              labels: labels.map { SessionLabel(value: $0, owner: .agent) }, state: state,
              createdAt: started ?? now.addingTimeInterval(-3600), lastActivityAt: at ?? now, endedReason: ended,
              archivedAt: archivedAt)
    }

    func issue(_ n: Int, _ title: String, parent: Int? = nil, progress: DaemonAPI.ForgeProgress? = nil) -> DaemonAPI.ForgeItem {
        .init(node: Node(kind: "issue", ref: "github:acme/app#\(n)"), title: title, state: "open", folder: folder.path,
              updatedAt: now, parent: parent.map { Node(kind: "issue", ref: "github:acme/app#\($0)") }, progress: progress)
    }

    func pull(_ n: Int, checks: String, review: String? = nil, labels: [String] = [], draft: Bool = false) -> DaemonAPI.ForgeItem {
        .init(node: Node(kind: "pr", ref: "github:acme/app#\(n)"), title: "PR \(n)", state: "open", folder: folder.path,
              labels: labels.map { DaemonAPI.ForgeLabel(name: $0) }, updatedAt: now, draft: draft, checks: checks, review: review)
    }

    func board(_ agents: [Agent], issues: [DaemonAPI.ForgeItem] = [], pulls: [DaemonAPI.ForgeItem] = [],
               epics: [DaemonAPI.ForgeItem] = [], groups: [String: AgentGroup] = [:]) -> Dashboard.Board {
        Dashboard.board(agents: agents, group: { groups[$0.title ?? ""] ?? $0.group() },
                        issues: issues, pulls: pulls, epics: epics, now: now, calendar: calendar)
    }

    @Test func sessionsOnOneIssueAreOneRowWithItsTitlePullRequestAndChecks() {
        let b = board([agent("web", labels: ["#12", "P#20"]), agent("mac", labels: ["#12"])],
                      issues: [issue(12, "Session badge")], pulls: [pull(20, checks: "passing")])
        let rows = b.rows[.working] ?? []
        #expect(rows.count == 1)
        #expect(rows.first?.title == "Session badge")
        #expect(rows.first?.refText == "#12")
        #expect(rows.first?.sessions.count == 2)
        #expect(rows.first?.pullRequest?.number == 20)
        #expect(rows.first?.pullRequest?.checks == "passing")
    }

    @Test func aRowTakesItsWorstSessionsMarkAndWaitingGoesUnderNeedsYou() {
        let b = board([agent("b", labels: ["#1"], state: .waitingOnUser, started: now.addingTimeInterval(-60)),
                       agent("a", labels: ["#1"], started: now.addingTimeInterval(-120))])
        #expect(b.count(.needsYou) == 1)
        #expect(b.count(.working) == 0)
        let row = b.rows[.needsYou]?.first
        #expect(row?.mark == .needsYou)
        #expect(row?.sessions.map(\.agent.title) == ["a", "b"])
    }

    /// Alex, #5695: rows that swapped places while they worked. The order is when each started, and
    /// no state or activity moves it: in a section, or for the sessions on a row.
    @Test func rowsAndSessionsKeepTheirOrderWhileTheyWork() {
        let t0 = now.addingTimeInterval(-7200)
        var a = agent("a", labels: ["#1"], started: t0)
        let b = agent("b", labels: ["#2"], started: t0.addingTimeInterval(60))
        var c = agent("c", labels: ["#3"], started: t0.addingTimeInterval(120))
        var c2 = agent("c2", labels: ["#3"], started: t0.addingTimeInterval(180))
        let order = { (agents: [Agent], groups: [String: AgentGroup]) in
            let w = self.board(agents, groups: groups).rows[.working] ?? []
            return (w.compactMap(\.refText), w.last?.sessions.compactMap(\.agent.title) ?? [])
        }
        let first = order([a, b, c, c2], [:])
        #expect(first.0 == ["#1", "#2", "#3"])
        #expect(first.1 == ["c", "c2"])
        // The newest session does something, then the oldest; one goes idle and another fails.
        c2.lastActivityAt = now.addingTimeInterval(30)
        a.lastActivityAt = now.addingTimeInterval(60)
        c.state = .finished
        c.endedReason = .processDied
        let second = order([c2, b, a, c], ["b": .waiting])
        #expect(second.0 == first.0)
        #expect(second.1 == first.1)
    }

    @Test func finishedPausedAndArchivedSessionsLeaveTheBoard() {
        let b = board([agent("done", labels: ["#4"], state: .finished, ended: .endTurn),
                       agent("working", labels: ["#4"]),
                       agent("paused", labels: ["#5"]),
                       agent("archived", labels: ["#6"], state: .archived, archivedAt: now)],
                      groups: ["paused": .stopped])
        #expect(b.rows[.working]?.map(\.refText) == ["#4"])
        #expect(b.rows[.working]?.first?.sessions.map(\.agent.title) == ["working"])
        #expect(b.count(.needsYou) == 0)
    }

    @Test func aDoneRowStaysWhileItsPullRequestsChecksFail() {
        let b = board([agent("lane", labels: ["#8", "P#30"], state: .finished, ended: .endTurn)],
                      pulls: [pull(30, checks: "failing")])
        let row = b.rows[.working]?.first
        #expect(row?.mark == .failed)
        #expect(row?.sessions.isEmpty == true)
        #expect(board([agent("lane", labels: ["#8", "P#30"], state: .finished, ended: .endTurn)],
                      pulls: [pull(30, checks: "passing")]).isEmpty)
    }

    @Test func aSessionWithNoIssueIsARowOfItsOwnUnderItsTitle() {
        let b = board([agent("Fix flaky Go test")])
        #expect(b.rows[.working]?.first?.subject == .session)
        #expect(b.rows[.working]?.first?.title == "Fix flaky Go test")
        #expect(b.rows[.working]?.first?.refText == nil)
    }

    @Test func aPullRequestAloneIsItsRow() {
        let b = board([agent("review", labels: ["P#20"])], pulls: [pull(20, checks: "failing")])
        let row = b.rows[.working]?.first
        #expect(row?.subject == .pullRequest(number: 20))
        #expect(row?.refText == "PR #20")
        #expect(row?.pullRequest?.checks == "failing")
    }

    @Test func aCrashedSessionIsFailedAndStaysWithTheWorkingRows() {
        let b = board([agent("x", labels: ["#3"], state: .finished, ended: .processDied)])
        #expect(b.rows[.working]?.first?.mark == .failed)
    }

    @Test func anEpicCardForEachEpicOnTheBoardWithItsProgress() {
        let b = board([agent("a", labels: ["#12"]), agent("b", labels: ["#13"], state: .waitingOnUser)],
                      issues: [issue(12, "Badge", parent: 9), issue(13, "Notice", parent: 9)],
                      epics: [issue(9, "Dashboard", progress: .init(done: 1, total: 4))])
        #expect(b.epics.count == 1)
        #expect(b.epics.first?.title == "Dashboard")
        #expect(b.epics.first?.items == 2)
        #expect(b.epics.first?.progress?.total == 4)
        #expect(b.epics.first?.mark == .needsYou)
        #expect(b.rows.values.joined().allSatisfy { $0.epic?.ref == "github:acme/app#9" })
    }

    @Test func withNoForgeReadARowShowsItsLabel() {
        let b = board([agent("a", labels: ["#77"])])
        #expect(b.rows[.working]?.first?.title == "Issue #77")
        #expect(b.rows[.working]?.first?.node == nil)
    }

    @Test func aPullRequestThatWaitsForApprovalPutsItsRowUnderNeedsYou() {
        // The session waits on the review with nothing to ask: the pull request is what needs the person.
        let b = board([agent("lane", labels: ["#12", "P#20"], state: .finished, ended: .endTurn)],
                      pulls: [pull(20, checks: "passing", review: "review_required", labels: ["needs-approval"])])
        let row = b.rows[.needsYou]?.first
        #expect(row?.mark == .needsYou)
        #expect(row?.pullRequest?.waitsForApproval == true)
        // Its session is done, so it is not listed.
        #expect(row?.sessions.isEmpty == true)
    }

    @Test func anApprovedDraftOrUnlistedPullRequestDoesNotWait() {
        #expect(board([agent("a", labels: ["P#20"])], pulls: [pull(20, checks: "passing", review: "approved",
                                                                 labels: ["needs-approval"])]).count(.needsYou) == 0)
        #expect(board([agent("a", labels: ["P#20"])], pulls: [pull(20, checks: "passing", review: "review_required",
                                                                 draft: true)]).count(.needsYou) == 0)
        // Not read from the forge: its labels alone never ask.
        #expect(board([agent("a", labels: ["P#20"])]).count(.needsYou) == 0)
        // A review required by the forge asks without the label.
        #expect(board([agent("a", labels: ["P#20"])], pulls: [pull(20, checks: "running",
                                                                 review: "review_required")]).count(.needsYou) == 1)
    }

    @Test func aSessionWithACardItsStateDoesNotShowNeedsYou() {
        let a = agent("guarded", labels: ["#7"], state: .finished, ended: .endTurn)
        let b = Dashboard.board(agents: [a], group: { $0.group() }, asks: { $0.id == a.id }, now: now, calendar: calendar)
        #expect(b.rows[.needsYou]?.first?.sessions.first?.mark == .needsYou)
    }

    @Test func noSessionsIsAnEmptyBoard() {
        #expect(board([]).isEmpty)
        #expect(board([]).sections.isEmpty)
    }
}
