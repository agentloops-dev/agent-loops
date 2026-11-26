import Foundation
import Testing
@testable import AgentsKitCore

/// Each Work page opens with what it had when last left (#5451), as workRemembered.test.mjs has it on the web page.
struct WorkRememberedTests {
    @Test func aListsFiltersAndSortAreTheWebPagesWordsAndComeBack() {
        let f = WorkList.Filter(state: .closed, project: "agents", label: "bug")
        let words = WorkRemembered.words(f, sort: .newest, kind: .issue)
        #expect(words == "project=agents&state=closed&label=bug&sort=newest")
        let back = WorkRemembered.list(words, kind: .issue)
        #expect(back.filter == f)
        #expect(back.sort == .newest)
    }

    @Test func theDefaultsAreNoWords() {
        #expect(WorkRemembered.words(.init(), sort: .priority, kind: .issue) == "")
        // Issues open by Priority (#6087); a person who chose Last updated keeps it.
        #expect(WorkRemembered.list(nil, kind: .issue).sort == .priority)
        #expect(WorkRemembered.words(.init(), sort: .updated, kind: .issue) == "sort=updated")
        #expect(WorkRemembered.list("sort=updated", kind: .issue).sort == .updated)
        #expect(WorkRemembered.words(.init(), sort: .newest, kind: .release) == "")
        #expect(WorkRemembered.words(.init(text: "  "), sort: .updated, kind: .pr) == "")
        let none = WorkRemembered.list(nil, kind: .release)
        #expect(none.filter == .init())
        #expect(none.sort == .newest)
    }

    @Test func aStateSortOrBoardAPageDoesNotHaveIsTheDefault() {
        let got = WorkRemembered.list("state=merged&sort=updated&board=x&label=y", kind: .release)
        #expect(got.filter.state == .open)
        #expect(got.sort == .newest)
        #expect(got.filter.board == "")
        #expect(got.filter.label == "y")
        #expect(WorkRemembered.list("state=merged", kind: .pr).filter.state == .merged)
    }

    @Test func wordsWithSpacesPlusAndAmpersandComeBack() {
        let f = WorkList.Filter(label: "c++ & go", text: "a b=c")
        let words = WorkRemembered.words(f, sort: .updated, kind: .issue)
        #expect(!words.contains("+"))
        #expect(WorkRemembered.list(words, kind: .issue).filter == f)
        // The web page's URLSearchParams writes a space as "+".
        #expect(WorkRemembered.list("q=a+b", kind: .issue).filter.text == "a b")
        #expect(WorkRemembered.list("%zz", kind: .issue).filter == .init())
    }

    @Test func timelineKeepsItsChoices() {
        #expect(WorkRemembered.timeline(range: .twoDays, project: "", board: "") == "")
        let t = WorkRemembered.timeline(WorkRemembered.timeline(range: .week, project: "p1", board: "b"))
        #expect(t.range == .week)
        #expect(t.project == "p1")
        #expect(t.board == "b")
        #expect(WorkRemembered.timeline("range=year").range == .twoDays)
        // The On hold toggle (#7335): off by default, kept as the web page keeps it.
        #expect(t.held == false)
        #expect(WorkRemembered.timeline(range: .twoDays, project: "", board: "", held: true) == "held=on")
        #expect(WorkRemembered.timeline("held=on").held)
    }

    @Test func eachPageHasItsOwnKey() {
        #expect(WorkRemembered.key(.issue) == "agents.work.filters.issue")
        #expect(WorkRemembered.key(.timeline) == "agents.work.filters.timeline")
    }
}
