import Foundation
import Testing
@testable import AgentsKitCore

/// Next ↗ on Work's Issues list (#5691; tier Web + Mac link, #4937, Lead role, #5290).
struct NextLinkTests {
    @Test func itOpensWorkNextForTheProjectsFolder() throws {
        let project = WorkList.Project(id: "alexec/agents", name: "Agents",
                                       folders: [WorkList.folderKey(host: .mac, path: "/Users/a/My Agents/")])
        let place = NextLink.place(project: project)
        #expect(place == "/work/next?folder=%2FUsers%2Fa%2FMy%20Agents")
        let url = try #require(BrowserOpening.pageURL("http://127.0.0.1:8792", place: place))
        #expect(url.fragment(percentEncoded: true) == "/work/next?folder=%2FUsers%2Fa%2FMy%20Agents")
    }

    @Test func withNoProjectThePageChooses() {
        #expect(NextLink.place(project: nil) == "/work/next")
        #expect(NextLink.place(project: WorkList.Project(id: "x", name: "X", folders: [])) == "/work/next")
    }
}
