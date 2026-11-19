import Foundation
import Testing
@testable import AgentsKitCore

/// A Work page's trail (#5452), as `Web/test/work-trail.test.mjs` has it on the web page.
@Suite("Work trail")
struct WorkTrailTests {
    typealias Node = DaemonAPI.LineageNode
    let ref = "github:alexec/Agents#5291"
    let epic = Node(kind: "issue", ref: "github:alexec/Agents#5281")

    @Test("an issue in a feature's epic: Issues, the epic, itself")
    func featureEpicIssue() {
        let parts = WorkTrail.parts(kind: .issue, ref: ref, feature: "agent loops", parent: epic)
        #expect(parts.map(\.words) == ["Issues", "Epic #5281", "#5291"])
        #expect(parts.map(\.target) == [.list(.issue), .item(.issue, ref: epic.ref), nil])
        #expect(WorkTrail.spoken(parts) == "Issues, Epic #5281, #5291")
    }

    @Test("an epic: Issues, itself")
    func epicItself() {
        let parts = WorkTrail.parts(kind: .issue, ref: epic.ref, feature: "work")
        #expect(parts.map(\.words) == ["Issues", "#5281"])
    }

    @Test("an issue in no feature: its list, a parent with no Epic word, itself")
    func plainIssue() {
        #expect(WorkTrail.parts(kind: .issue, ref: ref).map(\.words) == ["Issues", "#5291"])
        let parts = WorkTrail.parts(kind: .issue, ref: ref, feature: "", parent: epic)
        #expect(parts.map(\.words) == ["Issues", "#5281", "#5291"])
        #expect(parts.first?.target == .list(.issue))
    }

    @Test("a pull request keeps to its list, whatever its labels say")
    func pullRequest() {
        let parts = WorkTrail.parts(kind: .pr, ref: "github:alexec/Agents#45", feature: "work", parent: epic)
        #expect(parts.map(\.words) == ["Pull requests", "#45"])
    }

    @Test("a narrow page shows the page and the part above it")
    func short() {
        let parts = WorkTrail.parts(kind: .issue, ref: ref, feature: "work", parent: epic)
        #expect(WorkTrail.short(parts).map(\.words) == ["Epic #5281", "#5291"])
        #expect(WorkTrail.short(WorkTrail.parts(kind: .issue, ref: ref)).map(\.words) == ["Issues", "#5291"])
    }
}
