import AgentsKitCore
import SwiftUI

/// A Work page (#5475) or a doc's page (#5481) in a window of its own on the Mac, beside the main window, whose place is not
/// touched by it. Work's lists, sessions and new sessions open in the main window.
struct WorkWindow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWindow) private var openWindow
    @Binding var key: WorkWindowKey?
    @State private var shown: (any WorkWindowPages)?

    private var showMain: @MainActor () -> Void {
        { [model, openWindow] in model.bringMainWindowForward(opening: openWindow) }
    }

    var body: some View {
        page
            .webPlaceLinks()
            .paperGround()
            .windowFloor(width: 480, height: 360)
            .tint(Paper.accent)
            .focusedSceneValue(\.workWindow, shown.map { FocusedWorkWindow(pages: $0) })
            .environment(\.showMainWindow, ShowMainWindow(run: showMain))
            // A session's link opens it where a notification would (#3353).
            .environment(\.openURL, OpenURLAction { url in
                guard case .agent(let id) = AttentionLink.parse(url) else { return .systemAction }
                showSession(id)
                return .handled
            })
    }

    @ViewBuilder
    private var page: some View {
        if key?.docRef != nil {
            DocWindow(key: $key, opened: { shown = $0 })
        } else if let conversation = key?.conversation {
            ConversationWindow(conversation: conversation, list: model.messagesList).id(conversation)
                .onAppear { shown = WorkWindowModel(base: model, place: nil, showMain: showMain, showSession: showSession) }
                .onDisappear { shown = nil }
        } else {
            WorkWindowPage(base: model, key: $key, showMain: showMain, showSession: showSession, opened: { shown = $0 })
        }
    }

    private func showSession(_ id: UUID) {
        if !model.showInFolderWindow(id) { model.showInMainWindow(id, opening: openWindow) }
    }
}

/// Brings the main window forward, from a view in a window of its own that opens something there.
struct ShowMainWindow {
    let run: @MainActor () -> Void
}

extension EnvironmentValues {
    /// Set in a Work page's own window (#5475); nil in the main window.
    @Entry var showMainWindow: ShowMainWindow? = nil
}

/// The pages of the key window's own window, of whichever kind. Equal by identity, so a new body of the
/// same window is no new value: a value that always changes updates the toolbar again (#5947).
struct FocusedWorkWindow: Equatable {
    let pages: any WorkWindowPages

    static func == (a: Self, b: Self) -> Bool { a.pages === b.pages }
}

extension FocusedValues {
    /// The Work page in the key window, when that is a window of its own (#5475): Go ▸ Back and Forward
    /// are its, and the Session menu has no session.
    @Entry var workWindow: FocusedWorkWindow?
}
