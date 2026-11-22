import Foundation
import Testing
@testable import AgentsKitCore

/// Work's lists (#3119), as `Web/test/work.test.mjs` has them on the web page: one row per ref
/// from every machine, lineage on top, the filters and the sorts.
@Suite("Work list")
struct WorkListTests {
    typealias Node = DaemonAPI.LineageNode
    let day = Date(timeIntervalSince1970: 1_793_000_000)

    func item(_ n: Int, title: String? = nil, state: String = "open", labels: [DaemonAPI.ForgeLabel] = [],
              assignees: [String] = [], repo: String = "alexec/Agents", kind: String = "issue", at: Date? = nil) -> DaemonAPI.ForgeItem {
        .init(node: Node(kind: kind, ref: "github:\(repo)#\(n)"), title: title ?? "Issue \(n)", state: state, labels: labels,
              assignees: assignees, updatedAt: at ?? day)
    }

    static func cost(_ usd: Decimal) -> DaemonAPI.LineageCost { .init(cost: usd == 0 ? [:] : ["USD": usd], turns: 1) }

    func node(_ n: Int, _ usd: Decimal, waste: Decimal = 0, lead: Double? = nil) -> DaemonAPI.ReportNode {
        .init(node: Node(kind: "issue", ref: "github:alexec/Agents#\(n)"),
              cost: .init(own: Self.cost(usd), total: Self.cost(usd), waste: Self.cost(waste)), sessions: 2,
              leadTimeSeconds: lead, lastAt: day)
    }

    @Test func aJiraIssuesRowHasItsStatusAndTheTrackerJira() {
        let row = WorkList.Row(.init(node: Node(kind: "issue", ref: "jira:PROJ-7"), title: "Export", state: "open", updatedAt: day,
                                     status: "In Review"), on: .mac)
        #expect(row.status == "In Review")
        #expect(row.state == "open")
        #expect(row.repo == "jira")
        #expect(WorkList.repoName(row.repo) == "Jira")
        #expect(WorkList.repoName("github:alexec/Agents") == "alexec/Agents")
    }

    @Test func anAzureBoardsWorkItemsRowHasItsStatesNameItsProjectAndOpenOnAzureBoards() {
        let row = WorkList.Row(.init(node: Node(kind: "issue", ref: "azureboards:acme/web app#12"), title: "Export", state: "open",
                                     updatedAt: day, status: "Active"), on: .mac)
        #expect(row.status == "Active")
        #expect(row.repo == "azureboards:acme/web app")
        #expect(WorkList.repoName(row.repo) == "Azure Boards · acme/web app")
        #expect(WorkList.shortRef(row.node.ref) == "#12")
        #expect(WorkList.openOnWords(row.node.ref) == "Open on Azure Boards")
    }

    @Test func aForgejoPullRequestsRowHasItsRepoAndOpenOnForgejo() {
        let row = WorkList.Row(.init(node: Node(kind: "pr", ref: "forgejo:walkalex/app#8"), title: "Add hello", state: "open",
                                     updatedAt: day), on: .mac)
        #expect(row.repo == "forgejo:walkalex/app")
        #expect(WorkList.repoName(row.repo) == "walkalex/app")
        #expect(WorkList.shortRef(row.node.ref) == "#8")
        #expect(WorkList.openOnWords(row.node.ref) == "Open on Forgejo")
        #expect(WorkList.forgeName("pagure:a/b#1") == "the forge")
        #expect(WorkList.failureWords(JSONRPCError(code: -32602, message: "forge/rerun: open the check on Forgejo")) ==
            "forge/rerun: open the check on Forgejo")
        #expect(WorkList.errorWords(.init(forge: "forgejo", message: "not connected"), machine: "Studio") == "Forgejo on Studio: not connected")
        // agentsd's message does not name the forge, so the line names it once (#6536).
        #expect(WorkList.errorWords(.init(forge: "github", message: "could not be reached (the connection was refused)"), machine: nil) ==
            "GitHub: could not be reached (the connection was refused)")
    }

    @Test func oneMachinesAnswerForAForgeIsEnough() {
        typealias E = DaemonAPI.ForgeListError
        let down = E(forge: "github", message: "rate limited")
        let linear = E(forge: "linear", message: "not connected")
        // Studio read GitHub; the Mac could not, and its error goes. The Linear error stays: no machine read Linear.
        #expect(WorkList.shownErrors([([], [down, linear]), (["github:issue:alexec/agents#1"], [])]) == [[linear], []])
        // A machine that did not answer shows only when no machine answered.
        let offline = E(forge: "", message: "offline")
        #expect(WorkList.shownErrors([([], [offline]), ([], [])]) == [[], []])
        #expect(WorkList.shownErrors([([], [offline]), ([], [offline])]) == [[offline], [offline]])
        // One machine alone keeps its errors, also for a forge it read some of.
        #expect(WorkList.shownErrors([(["github:alexec/Agents#1"], [down])]) == [[down]])
        #expect(WorkList.refForge("github:pr:alexec/agents#1") == "github")
    }

    @Test func aBitbucketIssuesRowHasItsRepoAndOpenOnBitbucket() {
        let row = WorkList.Row(.init(node: Node(kind: "issue", ref: "bitbucket:walkalex/app#3"), title: "Export", state: "open",
                                     updatedAt: day), on: .mac)
        #expect(row.repo == "bitbucket:walkalex/app")
        #expect(WorkList.repoName(row.repo) == "walkalex/app")
        #expect(WorkList.shortRef(row.node.ref) == "#3")
        #expect(WorkList.openOnWords(row.node.ref) == "Open on Bitbucket")
        #expect(WorkList.errorWords(.init(forge: "bitbucket", message: "not connected"), machine: "Studio") == "Bitbucket on Studio: not connected")
    }

    @Test func anAzureDevOpsPullRequestsRowHasItsRepoOpenOnAzureDevOpsAndLabels() {
        let row = WorkList.Row(.init(node: Node(kind: "pr", ref: "azuredevops:acme/web app/app#3"), title: "Export", state: "open",
                                     updatedAt: day), on: .mac)
        #expect(row.repo == "azuredevops:acme/web app/app")
        #expect(WorkList.repoName(row.repo) == "acme/web app/app")
        #expect(WorkList.shortRef(row.node.ref) == "#3")
        #expect(WorkList.openOnWords(row.node.ref) == "Open on Azure DevOps")
        #expect(WorkList.errorWords(.init(forge: "azuredevops", message: "not connected"), machine: "Studio") ==
            "Azure DevOps on Studio: not connected")
        #expect(WorkList.takesLabels(row.node))
    }

    @Test func aBitbucketPullRequestsPageHasNoLabels() {
        #expect(!WorkList.takesLabels(Node(kind: "pr", ref: "bitbucket:walkalex/app#3")))
        #expect(WorkList.takesLabels(Node(kind: "issue", ref: "bitbucket:walkalex/app#3")))
        #expect(WorkList.takesLabels(Node(kind: "pr", ref: "github:alexec/Agents#12")))
        #expect(WorkList.takesLabels(Node(kind: "pr", ref: "forgejo:walkalex/app#2")))
    }

    @Test func oneRowPerRefTheLastUpdatedCopyWithItsLineage() {
        let rows = WorkList.merge([
            (host: .mac, items: [item(1), item(2, title: "Old")]),
            (host: MachineRef(rawValue: "box"), items: [item(2, title: "New", at: day.addingTimeInterval(3600))]),
        ], lineage: [node(2, 4.1)])
        #expect(rows.count == 2)
        let two = rows.first { $0.node.ref.hasSuffix("#2") }
        #expect(two?.title == "New")
        #expect(two?.host?.rawValue == "box")
        #expect(two?.lineage?.sessions == 2)
        #expect(rows.first { $0.node.ref.hasSuffix("#1") }?.lineage == nil)
    }

    /// The server's order (#6538): each machine's items in turn, a ref once, where it was first.
    @Test func theServersOrderKeepsEachRefWhereItWasFirst() throws {
        let box = MachineRef(rawValue: "box")
        let rows = WorkList.ordered([
            (host: .mac, items: [item(3), item(1)]),
            (host: box, items: [item(1, title: "Again"), item(2)]),
        ], lineage: [node(2, 4.1)])
        try #require(rows.map { WorkList.number($0.node.ref) } == [3, 1, 2])
        #expect(rows[1].host == .mac)
        #expect(rows[2].lineage?.sessions == 2)
    }

    /// A list reads each ref's repo once (#6633): merge and the server's order ask `repo` for a row's, not the ref.
    @Test func mergeTakesEachRowsRepoFromTheListsLookUp() {
        var asked: [String] = []
        let lookUp: (String) -> String = { ref in
            asked.append(ref)
            return "kept:" + WorkList.repo(of: ref)
        }
        let lists: [(host: MachineRef, items: [DaemonAPI.ForgeItem])] = [(host: .mac, items: [item(1), item(2, repo: "alexec/Web")])]
        let merged = WorkList.merge(lists, lineage: [], repo: lookUp)
        #expect(Set(merged.map(\.repo)) == ["kept:github:alexec/Agents", "kept:github:alexec/Web"])
        #expect(WorkList.ordered(lists, lineage: [], repo: lookUp).map(\.repo) == ["kept:github:alexec/Agents", "kept:github:alexec/Web"])
        #expect(asked.count == 4)
        // An older copy of a ref makes no row, so its repo is not asked.
        asked = []
        _ = WorkList.merge([(host: .mac, items: [item(1, at: day.addingTimeInterval(60))]), (host: .mac, items: [item(1)])],
                           lineage: [], repo: lookUp)
        #expect(asked == ["github:alexec/Agents#1"])
    }

    @Test func aRowsLineageInWordsNoneWithout() {
        #expect(WorkList.lineageWords(nil) == nil)
        let words = WorkList.lineageWords(node(2, 4.1, waste: 0.6, lead: 86_400 + 4 * 3600))
        #expect(words?.hasPrefix("2 sessions · ") == true)
        #expect(words?.contains("waste ") == true)
        #expect(words?.hasSuffix("lead time 1d 4h") == true)
        var quiet = node(3, 0)
        quiet.sessions = 0
        #expect(WorkList.lineageWords(quiet) == nil)
    }

    /// #3282: each machine's boards once by url, the first name kept; a board's name in the menu.
    @Test func boardsAreMergedByURL() {
        let web = DaemonAPI.ProjectBoard(name: "Web", url: "https://github.com/orgs/acme/projects/7")
        let renamed = DaemonAPI.ProjectBoard(name: "Web team", url: web.url)
        let plat = DaemonAPI.ProjectBoard(name: "Platform", url: "https://linear.app/acme/team/PLAT")
        let merged = WorkList.mergeBoards([[web], [renamed, plat]])
        #expect(merged.map(\.name) == ["Web", "Platform"])
        #expect(WorkList.boardName(plat.url, in: merged) == "Platform")
        #expect(WorkList.boardName("https://elsewhere", in: merged) == "https://elsewhere")
        #expect(WorkList.Filter().board.isEmpty)
    }

    /// forge/boardsAll (#5592): only the machines asked, in their order, so the home one names a board.
    @Test func everyMachinesBoardsComeInOneAnswerTheHomeOneFirst() {
        let web = DaemonAPI.ProjectBoard(name: "Web", url: "https://github.com/orgs/acme/projects/7")
        let renamed = DaemonAPI.ProjectBoard(name: "Web team", url: web.url)
        let other = DaemonAPI.ProjectBoard(name: "Other", url: "https://linear.app/acme/team/OTHER")
        let mac = MachineRef(rawValue: "mac"), box = MachineRef(rawValue: "box"), lost = MachineRef(rawValue: "lost")
        let all = DaemonAPI.ForgeBoardsAll(boards: [.init(machine: box, machineName: "Box", boards: .init(boards: [renamed])),
                                                    .init(machine: mac, machineName: "Mac", boards: .init(boards: [web])),
                                                    .init(machine: lost, machineName: "Lost", boards: .init(boards: [other]))])
        #expect(WorkList.mergeBoards(all, hosts: [mac, box]).map(\.name) == ["Web"])
    }

    /// forge/listAll (#5592): each machine asked, in its order; one the plane could not reach fails all its forges,
    /// so another machine's answer for them is enough (#5449); one not asked is left out.
    @Test func everyMachinesListComesInOneAnswerInTheirOrder() throws {
        let mac = MachineRef(rawValue: "mac"), box = MachineRef(rawValue: "box"), lost = MachineRef(rawValue: "lost")
        let all = DaemonAPI.ForgeListAll(
            lists: [.init(machine: box, machineName: "Box", list: .init(items: [item(2)], cursor: "b2")),
                    .init(machine: MachineRef(rawValue: "new"), machineName: "New", list: .init(items: [item(9)])),
                    .init(machine: mac, machineName: "Mac", list: .init(items: [item(1)]))],
            unreached: [.init(machine: lost, machineName: "Lost")])
        let lists = WorkList.hostLists(all, hosts: [mac, box, lost])
        try #require(lists.count == 3)
        #expect(lists.map(\.host) == [mac, box, lost])
        #expect(lists[1].list.cursor == "b2")
        #expect(lists[2].list.errors == [.init(forge: "", message: WorkList.didNotAnswer)])
        let shown = WorkList.shownErrors(lists.map { ($0.list.items.map(\.node.ref), $0.list.errors ?? []) })
        try #require(shown.count == 3)
        #expect(shown[2].isEmpty)
    }

    /// With one machine, the window asks it directly, not through the control plane (#6141).
    @Test func oneMachineIsAskedDirectly() {
        #expect(!WorkList.asksControlPlane(machines: 0))
        #expect(!WorkList.asksControlPlane(machines: 1))
        #expect(WorkList.asksControlPlane(machines: 2))
    }

    /// Each repo is asked once (#6641): a machine that reads only what others asked read is left out; one that reads
    /// the most covers the others, and one whose scopes are not known is always asked.
    @Test func eachRepoIsAskedOfOneMachine() {
        let a = MachineRef(rawValue: "a"), b = MachineRef(rawValue: "b"), c = MachineRef(rawValue: "c")
        let shop = ["github:example/shop"], both = ["github:example/shop", "github:example/web"]
        #expect(WorkList.oneEach([a, b], scopes: [:]) == ([a, b], []))
        #expect(WorkList.oneEach([a, b], scopes: [a: shop, b: shop]) == ([a], [b]))
        #expect(WorkList.oneEach([a, b], scopes: [a: shop, b: both]) == ([b], [a]))
        #expect(WorkList.oneEach([a, b, c], scopes: [a: shop, b: ["github:example/web"], c: both]) == ([c], [a, b]))
        #expect(WorkList.oneEach([a, b], scopes: [a: shop, b: ["github:example/web"]]) == ([a, b], []))
        #expect(WorkList.oneEach([a, b], scopes: [b: shop]) == ([a, b], []))
        #expect(WorkList.oneEach([a, b], scopes: [a: shop, b: []]) == ([a], [b]))
    }

    /// forge/listAll's next pages (#5592): each machine with a cursor gets its page; one that could not read it keeps its
    /// rows and cursor, and the page says why.
    @Test func theNextPagesComeInOneAnswerAndAFailedOneKeepsItsCursor() throws {
        let mac = MachineRef(rawValue: "mac"), box = MachineRef(rawValue: "box"), lost = MachineRef(rawValue: "lost")
        let had: [(host: MachineRef, list: DaemonAPI.ForgeList)] = [
            (mac, .init(items: [item(1)], cursor: "m2")),
            (box, .init(items: [item(2)], cursor: "b2")),
            (lost, .init(items: [item(3)], cursor: "l2")),
            (MachineRef(rawValue: "done"), .init(items: [item(4)])),
        ]
        let all = DaemonAPI.ForgeListAll(
            lists: [.init(machine: mac, machineName: "Mac", list: .init(items: [item(5)])),
                    .init(machine: box, machineName: "Box",
                          list: .init(errors: [.init(forge: "github", message: "Not connected.")]))],
            unreached: [.init(machine: lost, machineName: "Lost")])
        let (next, problem) = WorkList.addPages(all, to: had)
        try #require(next.count == 4)
        #expect(next.map { $0.list.items.count } == [2, 1, 1, 1])
        #expect(next[0].list.cursor == nil)
        #expect(next[1].list.cursor == "b2")
        #expect(next[2].list.cursor == "l2")
        #expect(problem == "Lost: \(WorkList.didNotAnswer); Not connected.")
        #expect(WorkList.addPages(.init(lists: [.init(machine: mac, machineName: "Mac", list: .init())]), to: had).problem == nil)
    }

    @Test func aProjectHasTheReposItsFoldersReadOnEveryMachine() throws {
        let box = MachineRef(rawValue: "box")
        func read(_ n: Int, _ repo: String, _ folder: String) -> DaemonAPI.ForgeItem {
            var i = item(n, repo: repo)
            i.folder = folder
            return i
        }
        let agents = WorkList.Project(id: "p1", name: "Agents", folders: [
            WorkList.folderKey(host: .mac, path: "/Users/a/Agents/"), WorkList.folderKey(host: box, path: "/home/a/Agents"),
        ])
        let www = WorkList.Project(id: "p2", name: "www", folders: [WorkList.folderKey(host: .mac, path: "/Users/a/www")])
        let quiet = WorkList.Project(id: "p3", name: "quiet", folders: [WorkList.folderKey(host: .mac, path: "/Users/a/quiet")])
        let lists: [(host: MachineRef, items: [DaemonAPI.ForgeItem])] = [
            (host: .mac, items: [read(1, "alexec/Agents", "/Users/a/Agents"), read(2, "alexec/www", "/Users/a/www")]),
            (host: box, items: [read(3, "alexec/docs", "/home/a/Agents")]),
        ]
        let projects = WorkList.projectRepos([agents, www, quiet], lists: lists)
        // A project whose folders read nothing is not offered.
        #expect(projects.map(\.project.name) == ["Agents", "www"])
        #expect(try #require(projects.first).repos == ["github:alexec/Agents", "github:alexec/docs"])

        let rows = WorkList.merge(lists, lineage: [])
        #expect(Set(WorkList.filter(rows, .init(project: "p1"), projects: projects).map(\.node.ref))
            == ["github:alexec/Agents#1", "github:alexec/docs#3"])
        #expect(WorkList.filter(rows, .init(project: "p2"), projects: projects).map(\.node.ref) == ["github:alexec/www#2"])
        #expect(WorkList.filter(rows, .init(project: "p3"), projects: projects).isEmpty)
        #expect(WorkList.filter(rows, .init(), projects: projects).count == 3)

        #expect(WorkList.project(ofFolder: "/home/a/Agents", on: box, in: [agents, www])?.name == "Agents")
        #expect(WorkList.project(ofFolder: "/home/a/Agents", on: .mac, in: [agents, www]) == nil)
        #expect(WorkList.project(ofFolder: nil, on: .mac, in: [agents]) == nil)
    }

    @Test func filtersStateRepoLabelAssigneeAndWords() {
        let rows = WorkList.merge([(host: .mac, items: [
            item(1, labels: [.init(name: "bug", color: "d73a4a")], assignees: ["alexec"]),
            item(2, title: "Flaky test", state: "closed"),
            item(3, repo: "alexec/Other"),
        ])], lineage: [])
        func f(_ filter: WorkList.Filter) -> [Int] {
            WorkList.sort(WorkList.filter(rows, filter), by: .newest).map { WorkList.number($0.node.ref) }
        }
        #expect(f(.init()) == [3, 1])
        #expect(f(.init(state: .closed)) == [2])
        #expect(f(.init(state: .all, repo: "github:alexec/Other")) == [3])
        #expect(f(.init(label: "bug")) == [1])
        #expect(f(.init(assignee: "alexec")) == [1])
        #expect(f(.init(state: .all, text: "flaky")) == [2])
        #expect(WorkList.choices(rows).repos == ["github:alexec/Agents", "github:alexec/Other"])
    }

    @Test func aMergedPullRequestIsMergedNotClosed() {
        let pr = WorkList.Row(item(5, state: "merged", kind: "pr"), on: .mac)
        #expect(WorkList.matches(pr, state: .merged))
        #expect(!WorkList.matches(pr, state: .closed))
    }

    @Test func sortsUpdatedNewestAndCostWithNoLineageLast() {
        let rows = WorkList.merge([(host: .mac, items: [
            item(1, at: day.addingTimeInterval(86_400)), item(9), item(4, at: day.addingTimeInterval(-86_400)),
        ])], lineage: [node(4, 5), node(9, 1)])
        func order(_ s: WorkList.Sort) -> [Int] { WorkList.sort(rows, by: s).map { WorkList.number($0.node.ref) } }
        #expect(order(.updated) == [1, 9, 4])
        #expect(order(.newest) == [9, 4, 1])
        #expect(order(.cost) == [4, 9, 1])
    }

    /// Priority (#6087): not on hold first, then High, Medium, none, Low; in a priority, the last updated first.
    @Test func prioritySortsHighFirstNoneBeforeLowAndOnHoldLast() {
        func with(_ n: Int, _ priority: String?, hold: Bool = false, ago: Double = 0) -> DaemonAPI.ForgeItem {
            var i = item(n, at: day.addingTimeInterval(-ago))
            i.priority = priority
            i.onHold = hold ? true : nil
            return i
        }
        let rows = WorkList.merge([(host: .mac, items: [
            with(1, "low"), with(2, nil), with(3, "high", ago: 3600), with(4, "medium"), with(5, "high"),
            with(6, "high", hold: true), with(7, nil, hold: true, ago: 60),
        ])], lineage: [])
        #expect(WorkList.sort(rows, by: .priority).map { WorkList.number($0.node.ref) } == [5, 3, 4, 2, 1, 6, 7])
        #expect(WorkList.Kind.issue.sorts.first == .priority)
        #expect(WorkList.Sort.priority.title == "Priority")
    }

    @Test func refsRepoShortNameAndForge() {
        #expect(WorkList.repo(of: "github:alexec/Agents#12") == "github:alexec/Agents")
        #expect(WorkList.repo(of: "gitlab:group/repo!45") == "gitlab:group/repo")
        // #3311: a Linear issue's repo is its team, a choice of the Repo filter of its own.
        #expect(WorkList.repo(of: "linear:ENG-4") == "linear:ENG")
        #expect(WorkList.repoName("linear:ENG") == "Linear · ENG")
        var linear = node(0, 0)
        linear.node = Node(kind: "issue", ref: "linear:OPS-2")
        linear.repo = "linear"
        #expect(WorkList.Row(linear).repo == "linear:OPS")
        #expect(WorkList.repo(of: "github:alexec/Agents/release/v0.14.0") == "github:alexec/Agents")
        #expect(WorkList.shortRef("github:alexec/Agents#12") == "#12")
        #expect(WorkList.shortRef("linear:ENG-4") == "ENG-4")
        #expect(WorkList.openOnWords("gitlab:g/r!4") == "Open on GitLab")
    }

    @Test func aReleaseLineageAloneKnows() {
        var n = node(0, 2)
        n.node = Node(kind: "release", ref: "github:alexec/Agents/release/v0.14.0")
        let row = WorkList.Row(n)
        #expect(row.title == "v0.14.0")
        #expect(WorkList.titleSaysRef(row.title, row.node.ref))
        #expect(row.url?.absoluteString == "https://github.com/alexec/Agents/releases/tag/v0.14.0")
    }

    @Test func aLabelsInkReadsOnItsColour() {
        #expect(WorkList.isLight("ffffff"))
        #expect(!WorkList.isLight("#0e1f8a"))
        #expect(WorkList.rgb(nil) == nil)
    }
}

/// The page of one item (#3119), as `Web/test/work.test.mjs` has it.
@Suite("Work page")
struct WorkPageTests {
    typealias Node = DaemonAPI.LineageNode
    let pr = Node(kind: "pr", ref: "github:alexec/Agents#34")
    let release = Node(kind: "release", ref: "github:alexec/Agents/release/v0.14.0")
    let session = Node(kind: "session", ref: "session:S1")
    let at = Date(timeIntervalSince1970: 0)

    @Test func aNodesPageIsInItsKindsList() {
        #expect(WorkList.kind(of: release) == .release)
        // #3878: Work has no Milestones list.
        #expect(WorkList.kind(of: Node(kind: "milestone", ref: "github:alexec/Agents/milestone/15")) == nil)
        #expect(WorkList.kind(of: session) == nil)
    }

    @Test func peopleInWords() {
        let item = DaemonAPI.ForgeItem(node: pr, title: "T", state: "open", author: "alexec", assignees: ["bob"],
                                       milestone: "0.14", updatedAt: at)
        // The forge's milestone is not shown (#3878).
        #expect(WorkList.peopleWords(item) == "by alexec · assigned to bob")
    }

    /// #3130: the page's actions say what each does, as the web page's do.
    @Test func theActionsSayWhatTheyDo() {
        #expect(WorkList.mergeButton(.squash, waits: false) == "Squash and merge")
        #expect(WorkList.mergeButton(nil, waits: true) == "Merge when checks pass")
        let item = DaemonAPI.ForgeItem(node: pr, title: "T", state: "open", updatedAt: at, checks: "passing")
        let running = DaemonAPI.ForgeItemDetail(item: item, checks: [.init(name: "a", state: "running"), .init(name: "b", state: "failing")])
        #expect(WorkList.mergeWaits(running))
        #expect(!WorkList.mergeWaits(DaemonAPI.ForgeItemDetail(item: item, checks: [.init(name: "a", state: "passing")])))
        #expect(WorkList.failedChecks(running).map(\.name) == ["b"])
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeMerge, .init(auto: true)) == "Auto-merge is on: it merges when its checks pass.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeRerun, .init(reran: 2)) == "2 runs started again.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeRequestChanges, .init()) == "Changes requested.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeClose, .init(), reopened: true) == "Reopened.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeClose, .init(), moved: true) == "Status changed.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgePriority, .init()) == "Priority changed.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeHold, .init(), held: true) == "On hold: not picked up next.")
        #expect(WorkList.doneWords(DaemonAPI.Method.forgeHold, .init()) == "Off hold.")
    }

    /// #6092: the Priority section shows an issue's priority and on hold, so Labels leaves out their labels.
    @Test func theLabelsLeaveOutPriorityAndHold() {
        let labels = ["bug", "Priority:High", "priority:low", "priority:urgent", "on hold", "On-Hold", "story"].map { DaemonAPI.ForgeLabel(name: $0) }
        #expect(WorkList.sideLabels(labels).map(\.name) == ["bug", "priority:urgent", "story"])
        #expect(WorkList.priorityChoices == ["high", "medium", nil, "low"])
    }

    /// #6092: the menu and the toggle show only where the item's actions have them and a machine writes.
    @Test func thePriorityControlsFollowTheActions() {
        let item = DaemonAPI.ForgeItem(node: .init(kind: "issue", ref: "github:o/r#1"), title: "T", state: "open", updatedAt: at)
        let both = DaemonAPI.ForgeItemDetail(item: item, actions: ["label", "priority", "hold"])
        #expect(WorkList.setsPriority(both, writes: true) == (true, true))
        #expect(WorkList.setsPriority(both, writes: false) == (false, false))
        #expect(WorkList.setsPriority(DaemonAPI.ForgeItemDetail(item: item, actions: ["priority"]), writes: true) == (true, false))
        #expect(WorkList.setsPriority(DaemonAPI.ForgeItemDetail(item: item), writes: true) == (false, false))
    }

    /// Epic #6086: a row's right-click menu offers Priority and On hold on an issue whose forge sets
    /// them (its capabilities), and its machine writes; never on a pull request or another forge's row.
    @Test func aRowsPriorityMenuFollowsItsForge() {
        let github = DaemonAPI.ForgeCapabilities(forge: "github", issues: true, priority: true, hold: true)
        let jira = DaemonAPI.ForgeCapabilities(forge: "jira", issues: true, priority: true)
        func row(_ kind: String, _ ref: String) -> WorkList.Row {
            WorkList.Row(.init(node: .init(kind: kind, ref: ref), title: "T", state: "open", updatedAt: at), on: .mac)
        }
        let issue = row("issue", "github:alexec/Agents#1")
        #expect(WorkList.rowSetsPriority(issue, forges: [jira, github], writes: true) == (true, true))
        #expect(WorkList.rowSetsPriority(issue, forges: [github], writes: false) == (false, false))
        #expect(WorkList.rowSetsPriority(issue, forges: [jira], writes: true) == (false, false))
        #expect(WorkList.rowSetsPriority(issue, forges: nil, writes: true) == (false, false))
        #expect(WorkList.rowSetsPriority(row("pr", "github:alexec/Agents#2"), forges: [github], writes: true) == (false, false))
        let ticket = row("issue", "jira:PROJ-7")
        #expect(WorkList.rowSetsPriority(ticket, forges: [github, jira], writes: true) == (true, false))
    }

    /// Epic #6086: a list's next page keeps each forge's capabilities, and adds a forge the first page did not name.
    @Test func aNextPageKeepsTheForgesCapabilities() {
        let github = DaemonAPI.ForgeCapabilities(forge: "github", priority: true)
        let gitlab = DaemonAPI.ForgeCapabilities(forge: "gitlab", hold: true)
        #expect(WorkList.joinForges(nil, nil) == nil)
        #expect(WorkList.joinForges([github], nil) == [github])
        #expect(WorkList.joinForges(nil, [gitlab]) == [gitlab])
        #expect(WorkList.joinForges([github], [DaemonAPI.ForgeCapabilities(forge: "github"), gitlab]) == [github, gitlab])
    }

    /// #3130: a pull request's diff from the forge is the diff a view gives the app's diff page.
    @Test func aForgeDiffIsTheViewsDiff() throws {
        let diff = DaemonAPI.ForgeDiff(url: "https://github.com/o/r/pull/1/files", files: [
            .init(path: "a.go", additions: 1, hunks: [.init(oldStart: 0, newStart: 3, lines: [DiffLine(kind: .added, text: "x")])]),
            .init(path: "b.png", note: "binary"),
        ], cut: true)
        let page = diff.appViewDiff(title: "#1 T")
        #expect(page.title == "#1 T" && page.cut && page.url?.host == "github.com")
        try #require(page.files.map(\.path) == ["a.go", "b.png"])
        #expect(page.files[0].hunks.first?.oldStart == 1 && page.files[0].hunks.first?.lines.first?.kind == .added)
        #expect(page.files[1].note == "binary")
        #expect(page.site == "GitHub")
        #expect(diff.appViewDiff(title: "!1 T", site: "GitLab").site == "GitLab")
    }

    /// #3837: a view's diff names the forge it is on (the gitlab server's says GitLab), GitHub when it names none.
    @Test func aViewsDiffNamesItsSite() throws {
        let files: JSONValue = .array([.object(["path": .string("a.go")])])
        let none = try AppViewDiff.read(.object(["files": files])).get()
        #expect(none.site == "GitHub")
        let gitlab = try AppViewDiff.read(.object(["files": files, "site": .string(" GitLab ")])).get()
        #expect(gitlab.site == "GitLab")
        let long = try AppViewDiff.read(.object(["files": files, "site": .string(String(repeating: "x", count: 99))])).get()
        #expect(long.site.count == AppViewDiff.siteLimit)
    }

    @Test func aPlaceIsAListOrAPage() {
        #expect(WorkPlace(kind: .pr).node == nil)
        #expect(WorkPlace(kind: .pr, ref: pr.ref).node == pr)
    }

    @Test func previousAndNextAreTheRowsAboveAndBelowInTheListShown() throws {
        let items: [DaemonAPI.ForgeItem] = (1 ... 3).map { n in
            .init(node: Node(kind: "issue", ref: "github:alexec/Agents#\(n)"), title: "Issue \(n)", state: "open", labels: [],
                  assignees: [], updatedAt: at)
        }
        let rows: [WorkList.Row] = WorkList.sort(WorkList.merge([(host: MachineRef.mac, items: items)], lineage: []), by: .newest)
        let refs: [String] = rows.map { $0.node.ref }
        try #require(refs.count == 3)
        let first = WorkList.neighbours(of: refs[0], in: rows)
        #expect(first?.previous == nil && first?.next == refs[1] && first?.position == 1 && first?.count == 3)
        let middle = WorkList.neighbours(of: refs[1], in: rows)
        #expect(middle?.previous == refs[0] && middle?.next == refs[2])
        let last = WorkList.neighbours(of: refs[2], in: rows)
        #expect(last?.next == nil && last?.position == 3)
        #expect(last.map { WorkList.positionWords($0, more: true) } == "3 of 3+")
        #expect(middle.map { WorkList.positionWords($0, more: false) } == "2 of 3")
        // A row the filters leave out has no place in the list.
        #expect(WorkList.neighbours(of: refs[1], in: WorkList.filter(rows, .init(text: "Issue 3"))) == nil)
    }

    @Test func onlyAPageOpenedFromItsListGoesThroughIt() {
        #expect(WorkPlace(kind: .pr, ref: pr.ref, fromList: true).fromList)
        #expect(!WorkPlace(kind: .pr, ref: pr.ref).fromList)
        #expect(!WorkPlace(kind: .pr, fromList: true).fromList)
    }

    @Test func onlyAPullRequestHasAFilesChangedPageAndBackGoesToIt() {
        let files = WorkPlace(kind: .pr, ref: pr.ref, files: true)
        #expect(files.files)
        #expect(files != WorkPlace(kind: .pr, ref: pr.ref))
        #expect(!WorkPlace(kind: .issue, ref: pr.ref, files: true).files)
        #expect(!WorkPlace(kind: .pr, files: true).files)
        var h = LineageHistory<WorkPlace>()
        h.go(WorkPlace(kind: .pr, ref: pr.ref))
        h.go(files)
        h.back()
        #expect(h.current == WorkPlace(kind: .pr, ref: pr.ref))
    }

    @Test func previousAndNextReplaceThePlaceSoBackGoesToTheList() {
        var h = LineageHistory<WorkPlace>()
        h.go(WorkPlace(kind: .issue))
        h.go(WorkPlace(kind: .issue, ref: "a", fromList: true))
        h.replace(WorkPlace(kind: .issue, ref: "b", fromList: true))
        h.replace(WorkPlace(kind: .issue, ref: "c", fromList: true))
        #expect(h.places.count == 2 && h.current?.ref == "c")
        h.back()
        #expect(h.current == WorkPlace(kind: .issue))
        #expect(h.canGoForward)
    }
}

/// A list's ask joins the next for the same query only within one open (#6640, #6358 finding 3).
@Suite("Work list ask join")
struct WorkListAskJoinTests {
    @Test func theRestoreAndTheClickAskOnce() {
        #expect(WorkList.joinsAsk(same: true, reading: false, since: .milliseconds(200)))
        #expect(WorkList.joinsAsk(same: true, reading: true, since: .seconds(30)))
    }

    @Test func anOpenLaterInTheRunAsksAgain() {
        #expect(!WorkList.joinsAsk(same: true, reading: false, since: .seconds(5)))
        #expect(!WorkList.joinsAsk(same: true, reading: false, since: WorkList.joinWindow))
    }

    @Test func anotherQueryOrTheFirstAskAsks() {
        #expect(!WorkList.joinsAsk(same: false, reading: true, since: .milliseconds(10)))
        #expect(!WorkList.joinsAsk(same: true, reading: false, since: nil))
    }
}
