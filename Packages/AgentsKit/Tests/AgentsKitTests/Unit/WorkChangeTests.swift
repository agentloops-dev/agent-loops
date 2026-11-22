import Foundation
import Testing
@testable import AgentsKitCore

/// A Work change (#6354, epic #6348): what the cache shows at once, how it goes back, and the footer's words.
@Suite("A Work change")
struct WorkChangeTests {
    private let node = DaemonAPI.LineageNode(kind: "issue", ref: "github:acme/api#6311")

    private func item() -> DaemonAPI.ForgeItem {
        var item = DaemonAPI.ForgeItem(node: node, title: "Cache", state: "open", labels: [.init(name: "bug")],
                                       assignees: ["sam"], updatedAt: Date(timeIntervalSince1970: 1_794_000_000))
        item.priority = "medium"
        return item
    }

    @Test func eachChangeShowsAtOnceAndGoesBack() {
        let changes: [WorkChange] = [
            .priority("high"), .hold(true), .label("epic", add: true), .label("bug", add: false), .close(reopen: false),
            .transition(.init(id: "31", name: "Done", category: "done")), .assignee("alexec", add: true),
        ]
        for change in changes {
            let old = item()
            var shown = old
            change.apply(to: &shown)
            #expect(shown != old, "\(change) changes the item")
            change.restore(from: old, into: &shown)
            #expect(shown == old, "\(change) goes back")
        }
    }

    @Test func aChangeGoesBackWithoutUndoingAnother() {
        let old = item()
        var shown = old
        WorkChange.priority("high").apply(to: &shown)
        shown.title = "Renamed on the forge"
        WorkChange.priority("high").restore(from: old, into: &shown)
        #expect(shown.priority == "medium")
        #expect(shown.title == "Renamed on the forge")
    }

    @Test func aChangeAsksTheForgesWrite() throws {
        #expect(WorkChange.priority("high").method == DaemonAPI.Method.forgePriority)
        let label = try #require(WorkChange.label("epic", add: false).request(node) as? DaemonAPI.ForgeLabelRequest)
        #expect(label.remove == ["epic"] && label.add == nil)
        let reopen = try #require(WorkChange.close(reopen: true).request(node) as? DaemonAPI.ForgeCloseRequest)
        #expect(reopen.reopen == true)
    }

    @Test func theFooterSaysWhatAndWhy() {
        #expect(WorkChange.savingWords(node.ref) == "Saving #6311…")
        #expect(WorkChange.priority("high").failedWords(node.ref, said: "GitHub said 403 (no write access).")
            == "Could not set #6311 to High: GitHub said 403 (no write access). Changed back.")
        #expect(WorkChange.close(reopen: false).failedWords(node.ref, said: "")
            == "Could not close #6311. Changed back.")
        #expect(WorkChange.label("epic", add: true).words(node.ref) == "add the label epic to #6311")
    }
}
