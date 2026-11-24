import AgentsKitCore
import Foundation
import Testing

/// The page a Work window is keyed by (#5475).
struct WorkWindowKeyTests {
    private let ref = "github.com/acme/app#12"

    @Test func anItemsPageHasAWindowAndAListHasNone() {
        #expect(WorkWindowKey(WorkPlace(kind: .issue)) == nil)
        #expect(WorkWindowKey(WorkPlace(kind: .timeline, ref: ref)) == nil)
        #expect(WorkWindowKey(WorkPlace(kind: .pr, ref: ref)) == WorkWindowKey(kind: "pr", ref: ref))
    }

    @Test func thePageIsTheSameFromItsListAndFromItsFiles() {
        let key = WorkWindowKey(WorkPlace(kind: .pr, ref: ref, fromList: true))
        #expect(key == WorkWindowKey(WorkPlace(kind: .pr, ref: ref, files: true)))
        #expect(key?.place == WorkPlace(kind: .pr, ref: ref), "no Previous and Next in a window of its own")
    }

    @Test func aKindOfALaterBuildIsNoPageHere() throws {
        let saved = Data(#"{"kind":"doc","ref":"d1"}"#.utf8)
        let key = try JSONDecoder().decode(WorkWindowKey.self, from: saved)
        #expect(key.place == nil)
        #expect(try JSONDecoder().decode(WorkWindowKey.self, from: JSONEncoder().encode(key)) == key)
    }

    @Test func aConversationsWindowIsFoundByItsKeyAndIsNoItemsPage() throws {
        let key = WorkWindowKey(conversation: "slack:C01")
        #expect(key.conversation == "slack:C01")
        #expect(key.place == nil)
        #expect(try JSONDecoder().decode(WorkWindowKey.self, from: JSONEncoder().encode(key)) == key)
        #expect(WorkWindowKey(conversation: "").conversation == nil)
        #expect(WorkWindowKey(kind: "pr", ref: ref).conversation == nil)
    }

    @Test func theTitleIsTheNumberAndTheItemsTitle() {
        #expect(WorkWindowKey.title("Fix the build", ref: ref) == "#12 Fix the build")
        #expect(WorkWindowKey.title("Fix the build #12", ref: ref) == "Fix the build #12")
        #expect(WorkWindowKey.title(nil, ref: ref) == "#12")
    }
}
