import AppKit
import SwiftUI

/// The mouse's back and forward buttons, and a three-finger swipe when the trackpad sends one,
/// anywhere in the window (#5784), as Finder and Safari. Two fingers sideways are
/// `swipeBackForward`, over the page only.
struct PageBackForward: ViewModifier {
    let back: () -> Void
    let forward: () -> Void

    @State private var watcher: Any?
    @State private var anchor = SwipeAnchor()

    /// The side buttons, as AppKit numbers them: 0 is the left, 1 the right, 2 the middle.
    static let backButton = 3
    static let forwardButton = 4

    func body(content: Content) -> some View {
        content
            .background(SwipeAnchorView(anchor: anchor))
            .onAppear(perform: watch)
            .onDisappear(perform: stopWatching)
    }

    private func watch() {
        guard watcher == nil else { return }
        let back = back, forward = forward
        watcher = NSEvent.addLocalMonitorForEvents(matching: [.otherMouseDown, .swipe]) { [anchor] event in
            let ours = MainActor.assumeIsolated { anchor.view?.window.map { $0 === event.window } ?? false }
            guard ours, let goesBack = Self.goesBack(event) else { return event }
            MainActor.assumeIsolated { goesBack ? back() : forward() }
            return nil
        }
    }

    private func stopWatching() {
        guard let watcher else { return }
        NSEvent.removeMonitor(watcher)
        self.watcher = nil
    }

    /// True for Back, false for Forward, nil for an event that is neither. A swipe to the left
    /// (`deltaX` 1) goes back, as AppKit's own pages and Safari read it.
    private static func goesBack(_ event: NSEvent) -> Bool? {
        switch event.type {
        case .otherMouseDown where event.buttonNumber == backButton: true
        case .otherMouseDown where event.buttonNumber == forwardButton: false
        case .swipe where event.deltaX > 0: true
        case .swipe where event.deltaX < 0: false
        default: nil
        }
    }
}

extension View {
    /// Back and Forward from the mouse's side buttons and a three-finger swipe (#5784).
    func pageBackForward(back: @escaping () -> Void, forward: @escaping () -> Void) -> some View {
        modifier(PageBackForward(back: back, forward: forward))
    }
}
