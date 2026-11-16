import Foundation
import Testing
@testable import AgentsKitCore

/// The Mac window's Back and Forward along every page (#5784).
@Suite("Page history")
struct PageHistoryTests {
    private let chat = PagePlace(item: .session(UUID()))
    private let events = PagePlace(item: .events)
    private let issues = PagePlace(item: .work(.issue), work: WorkPlace(kind: .issue))

    private func issue(_ ref: String, fromList: Bool = true) -> PagePlace {
        PagePlace(item: .work(.issue), work: WorkPlace(kind: .issue, ref: ref, fromList: fromList))
    }

    @Test func backFromAnIssueAChatLinkedToGoesToTheChat() {
        var h = PageHistory()
        h.note(chat)
        h.note(issue("o/r#1", fromList: false))
        #expect(h.back { _ in true } == chat)
        // Back puts the chat back, and noting it does not go on again.
        h.note(chat)
        #expect(h.canGoForward { _ in true })
        #expect(h.forward { _ in true } == issue("o/r#1", fromList: false))
    }

    @Test func everyPageGoesOnAndANewOneDropsTheForwardPlaces() {
        var h = PageHistory()
        h.note(chat); h.note(events); h.note(events)
        #expect(h.places.places == [chat, events])
        _ = h.back { _ in true }
        h.note(issues)
        #expect(h.places.places == [chat, issues])
        #expect(!h.canGoForward { _ in true })
    }

    @Test func previousAndNextReplaceSoBackGoesToTheList() {
        var h = PageHistory()
        h.note(issues); h.note(issue("a")); h.note(issue("b")); h.note(issue("c"))
        #expect(h.places.places == [issues, issue("c")])
        #expect(h.back { _ in true } == issues)
    }

    @Test func aPlaceThatIsGoneIsSkipped() {
        var h = PageHistory()
        h.note(events); h.note(chat); h.note(issues)
        let there: (PagePlace) -> Bool = { $0 != chat }
        #expect(h.back(isThere: there) == events)
        #expect(h.forward(isThere: there) == issues)
        // Nothing before but gone places: no Back, and the place stays.
        var only = PageHistory()
        only.note(chat); only.note(events)
        #expect(!only.canGoBack(isThere: there))
        #expect(only.back(isThere: there) == nil)
        #expect(only.current == events)
    }

    @Test func workAndDocsPlacesAreKeptOnlyOnTheirPages() {
        #expect(PagePlace(item: .events, work: WorkPlace(kind: .issue), docs: DocsPlace()).work == nil)
        #expect(PagePlace(item: .docs, docs: DocsPlace(ref: "d")).docs?.ref == "d")
    }
}
