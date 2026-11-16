import AppKit

/// A scratch copy's windows keep no frame (#5636).
///
/// Every copy of the app shares one bundle id, so one defaults domain, and AppKit saves each
/// window's frame there under its autosave name (`NSWindow Frame main-AppWindow-1`), which
/// SwiftUI gives every copy the same. A walk that sized its window (`walk.py size`, the
/// placing at launch) so set the frame the person's window opened at next: too tall, too short.
/// `WindowDefaults` keeps a copy's own keys apart; this keeps AppKit's out as well, by taking
/// the autosave name off each window of a scratch copy before it can be moved or sized. Its
/// windows are not restorable either: the saved state the app opens with is shared too.
@MainActor
enum ScratchWindowFrames {
    /// Nothing for the person's window; for a scratch copy, the names are cleared from now on.
    static func start(scratch: Bool = WindowDefaults.scratchDomain != nil) {
        guard scratch, observer == nil else { return }
        // After each turn of the event loop: SwiftUI names a window when it makes it, and
        // can name it again later, so a name set at any time is gone before the next event.
        observer = NotificationCenter.default.addObserver(
            forName: NSApplication.didUpdateNotification, object: nil, queue: .main
        ) { _ in
            MainActor.assumeIsolated { clear(NSApp.windows) }
        }
    }

    static func clear(_ windows: [NSWindow]) {
        for window in windows {
            if !window.frameAutosaveName.isEmpty { window.setFrameAutosaveName("") }
            if window.isRestorable { window.isRestorable = false }
        }
    }

    private static var observer: NSObjectProtocol?
}
