import Testing

@testable import AgentsKitCore

/// A scroll over a long prompt moves the prompt, and goes on to the transcript at its
/// ends (#6887).
struct PromptScrollTests {
    @Test func aPromptThatFitsTakesNoScroll() {
        #expect(PromptScroll.top(from: 0, delta: -10, content: 100, visible: 100) == nil)
        #expect(PromptScroll.top(from: 0, delta: 10, content: 40, visible: 100) == nil)
    }

    @Test func scrollingDownMovesALongPromptDown() {
        #expect(PromptScroll.top(from: 0, delta: -30, content: 300, visible: 100) == 30)
    }

    @Test func scrollingUpMovesItBackUp() {
        #expect(PromptScroll.top(from: 50, delta: 30, content: 300, visible: 100) == 20)
    }

    @Test func itStopsAtItsEnds() {
        #expect(PromptScroll.top(from: 190, delta: -30, content: 300, visible: 100) == 200)
        #expect(PromptScroll.top(from: 10, delta: 30, content: 300, visible: 100) == 0)
    }

    @Test func atAnEndTheScrollGoesOnToTheTranscript() {
        #expect(PromptScroll.top(from: 200, delta: -30, content: 300, visible: 100) == nil)
        #expect(PromptScroll.top(from: 0, delta: 30, content: 300, visible: 100) == nil)
    }

    @Test func noDistanceIsNoScroll() {
        #expect(PromptScroll.top(from: 50, delta: 0, content: 300, visible: 100) == nil)
    }
}
