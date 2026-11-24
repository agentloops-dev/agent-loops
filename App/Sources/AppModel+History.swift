import AgentsKitCore
import Foundation

/// Back and Forward along every page of the window (#5784), as Finder's and Safari's: the ‹ ›
/// in the toolbar, Go ▸ Back (⌘[) and Forward (⌘]), the mouse's side buttons and a sideways
/// swipe. Each page a person goes to is noted (`ContentView` notes the detail's place when it
/// changes); Back and Forward open the place again, and a place that is gone is skipped.
extension AppModel {
    /// What the detail column shows now, as a place; nil while it shows nothing a person picked.
    var pagePlace: PagePlace? {
        guard let item = sidebarItem else { return nil }
        return PagePlace(item: item, work: showsWork ? workPlace : nil,
                         docs: showsDocs ? docsHistory.current ?? DocsPlace() : nil)
    }

    /// The detail column came to a place.
    func notePagePlace() {
        guard let place = pagePlace else { return }
        pageHistory.note(place)
    }

    var canGoBack: Bool { pageHistory.canGoBack(isThere: { isThere($0) }) }
    var canGoForward: Bool { pageHistory.canGoForward(isThere: { isThere($0) }) }

    func goBack() {
        if let place = pageHistory.back(isThere: { isThere($0) }) { reopen(place) }
    }

    func goForward() {
        if let place = pageHistory.forward(isThere: { isThere($0) }) { reopen(place) }
    }

    /// Whether a place can still be shown: its session is not archived or deleted, its folder
    /// not removed, its feature not turned off (#6572). Every other page is always there.
    func isThere(_ place: PagePlace) -> Bool {
        switch place.item {
        case .session(let id):
            guard let agent = work.agent(id) else { return false }
            return agent.state != .archived
        case .folder(let key), .workflow(_, let key), .pin(_, let key), .archivedSessions(let key),
             .archivedWorkflows(let key), .folderMCPServers(let key), .review(_, let key):
            return work.folder(key).map { !$0.folder.isArchived } ?? false
        case .work:
            return flags.isOn(.work)
        case .docs:
            return flags.isOn(.docs)
        case .dashboard:
            return flags.isOn(.dashboard)
        default:
            return true
        }
    }

    /// Show a place again, as the person left it.
    private func reopen(_ place: PagePlace) {
        if let work = place.work {
            showWork(work)
        } else if let docs = place.docs {
            showDocs(docs)
        } else {
            sidebarItem = place.item
        }
    }
}
