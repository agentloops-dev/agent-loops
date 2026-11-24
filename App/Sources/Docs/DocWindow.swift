import AgentsKitCore
import SwiftUI

/// The docs a window of its own goes along (#5481): its Back and Forward never move another window.
@MainActor @Observable
final class DocWindowModel: WorkWindowPages {
    @ObservationIgnored private let bringForward: @MainActor (WorkWindowKey) -> Void
    private(set) var history = LineageHistory<DocsPlace>()

    init(place: DocsPlace, bringForward: @escaping @MainActor (WorkWindowKey) -> Void) {
        self.bringForward = bringForward
        history.go(place)
    }

    var key: WorkWindowKey? { history.current?.ref.map { .doc($0) } }
    var canGoBack: Bool { history.canGoBack }
    var canGoForward: Bool { history.canGoForward }

    func go(_ place: DocsPlace) { take { $0.go(place) } }

    func step(back: Bool) { take { if back { $0.back() } else { $0.forward() } } }

    /// The doc it shows, at a line.
    func show(line: Int) { history.replace(DocsPlace(ref: history.current?.ref, line: line)) }

    /// Another window that shows the doc comes forward: a doc has one window at most.
    private func take(_ move: (inout LineageHistory<DocsPlace>) -> Void) {
        var next = history
        move(&next)
        guard let ref = next.current?.ref else { return }
        let key = WorkWindowKey.doc(ref)
        if key != self.key, OpenWorkWindows.shows(key, besides: self) { return bringForward(key) }
        history = next
    }
}

/// A doc's page in a window of its own (#5481), in the window scene of Work's pages (#5475).
struct DocWindow: View {
    @Environment(AppModel.self) private var app
    @Environment(\.openWindow) private var openWindow
    /// The doc, as the window was opened with it and is restored with it; a link in the window changes it.
    @Binding var key: WorkWindowKey?
    /// The window's model, for the menus of the app; nil once the window closes.
    let opened: ((any WorkWindowPages)?) -> Void
    @State private var model: DocWindowModel?

    var body: some View {
        Group {
            if let model, let place = model.history.current, let ref = place.ref {
                DocPage(ref: ref, line: place.line, window: model).id(place)
                    .toolbar { WorkWindowHistoryButtons(pages: model) }
            } else {
                Color.clear
            }
        }
        .modifier(WorkWindows())
        .task(id: key) { follow() }
        .onChange(of: model?.key) { _, shown in
            if let shown, shown != key { key = shown }
        }
        .onChange(of: key?.docRef.flatMap { app.docsList.windowLine[$0] }) { _, line in
            if let line, let ref = model?.key?.docRef, ref == key?.docRef {
                app.docsList.windowLine[ref] = nil
                model?.show(line: line)
            }
        }
        .onDisappear { close() }
    }

    private func follow() {
        guard let ref = key?.docRef, model?.key != key else { return }
        close()
        let line = app.docsList.windowLine.removeValue(forKey: ref)
        let made = DocWindowModel(place: DocsPlace(ref: ref, line: line)) { [openWindow] in
            openWindow(id: OpenWorkWindow.sceneID, value: $0)
        }
        OpenWorkWindows.opened(made)
        model = made
        opened(made)
    }

    private func close() {
        if let model { OpenWorkWindows.closed(model) }
        model = nil
        opened(nil)
    }
}

extension AppModel {
    /// A doc's row pressed: its page in the main window, or with ⌘ held, in a window of its own.
    func followDoc(_ place: DocsPlace, window: OpenWorkWindow?, main: ShowMainWindow? = nil) {
        if let window, let ref = place.ref, OpenWorkWindow.commandHeld {
            return openDocWindow(ref, line: place.line, window: window)
        }
        showDocs(place)
        main?.run()
    }

    func openDocWindow(_ ref: String, line: Int?, window: OpenWorkWindow) {
        docsList.windowLine[ref] = line
        window(.doc(ref))
    }
}

extension View {
    /// A doc's row has **Open in New Window** in its menu.
    func docWindowMenu(_ ref: String, line: Int? = nil) -> some View { modifier(DocWindowMenu(ref: ref, line: line)) }
}

private struct DocWindowMenu: ViewModifier {
    @Environment(AppModel.self) private var model
    @Environment(\.openWorkWindow) private var openWorkWindow
    let ref: String
    let line: Int?

    func body(content: Content) -> some View {
        if let openWorkWindow {
            content.contextMenu {
                Button(OpenWorkWindow.words) { model.openDocWindow(ref, line: line, window: openWorkWindow) }
            }
        } else {
            content
        }
    }
}
