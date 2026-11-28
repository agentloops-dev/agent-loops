#if DEBUG
import AppKit
import Foundation

/// A menu button's menu, opened and chosen from in process (#7530): `walk.py ROOT pick Add
/// "Add a Card…"`. A SwiftUI `Menu` is one of two things on the Mac:
///
/// - an AppKit pop-up button (a `.menuStyle(.borderlessButton)` in a row of chips, the sort
///   menu): its menu is read as `menu` reads the menu bar, once its coordinator has filled
///   it, and no menu is shown;
/// - a node that SwiftUI draws itself (the Secrets page's **Add**), with no AppKit view and
///   no menu until it opens one. It is pressed in `open`, before the command, and the menu
///   it tracks is taken from `NSMenu.didBeginTrackingNotification`. The socket waits off the
///   main thread meanwhile, and the main queue still runs while a menu tracks, so `run`
///   then chooses the item and ends the tracking. A window behind the others still opens it.
@MainActor
enum WalkPick {
    /// The menu a SwiftUI-drawn menu button has open, from `open` to the end of `run`.
    private static var tracked: NSMenu?
    /// The socket's wait, left once that menu tracks, or when nothing opens.
    private static var waiting: DispatchGroup?

    /// Before the command, on the main thread: a SwiftUI-drawn menu button in the matches is
    /// pressed, and `group` is left once its menu tracks. A pop-up button needs nothing here.
    static func open(_ found: [WalkCommands.Match], group: DispatchGroup) {
        guard found.allSatisfy({ popUp($0.element) == nil }),
              let button = found.reversed().lazy.compactMap({ menuButton($0.element) }).first else { return }
        end()
        group.enter()
        waiting = group
        NotificationCenter.default.addObserver(Watcher.shared, selector: #selector(Watcher.began(_:)),
                                               name: NSMenu.didBeginTrackingNotification, object: nil)
        // After this returns: the press runs the menu's tracking loop, which does not return
        // until the menu closes.
        DispatchQueue.main.async {
            if !button.showMenu() { leave() }
        }
    }

    private static func leave() {
        waiting?.leave()
        waiting = nil
    }

    @MainActor
    private final class Watcher: NSObject {
        static let shared = Watcher()

        @objc func began(_ note: Notification) {
            guard WalkPick.waiting != nil else { return }
            WalkPick.tracked = note.object as? NSMenu
            WalkPick.leave()
        }
    }

    static func run(_ request: [String: Any], matches found: [WalkCommands.Match],
                    menu choose: ([String], NSMenu) throws -> [String: Any]) throws -> [String: Any] {
        defer { end() }
        let path = request["path"] as? [String] ?? []
        guard !found.isEmpty else { throw WalkError("nothing matched \(request["text"] ?? "")") }
        let opened: NSMenu
        if let (cell, menu) = found.reversed().lazy.compactMap({ popUp($0.element) }).first {
            // SwiftUI's coordinator fills a pop-up button's menu only as it is about to show.
            if let delegate = menu.delegate as? NSObject {
                typealias WillShow = @convention(c) (NSObject, Selector, NSPopUpButtonCell, NSMenu) -> Void
                let selector = NSSelectorFromString("popUpButtonCell:willShowMenu:")
                if delegate.responds(to: selector) {
                    unsafeBitCast(delegate.method(for: selector), to: WillShow.self)(delegate, selector, cell, menu)
                }
            }
            opened = menu
        } else if let menu = tracked {
            opened = menu
        } else {
            throw WalkError("no menu button matched \(request["text"] ?? ""), or its menu did not open: "
                + found.map { $0.element.line(in: $0.window) }.joined(separator: "; "))
        }
        // Closed first, as a click on an item closes it before its action runs.
        let tracking = opened === tracked
        if tracking { opened.cancelTrackingWithoutAnimation() }
        guard path.isEmpty else { return try choose(path, opened) }
        opened.update()
        return ["ok": true, "did": opened.items.filter { !$0.isHidden }.map {
            ($0.isSeparatorItem ? "—" : $0.title) + ($0.isEnabled || $0.isSeparatorItem ? "" : " (off)")
        }.filter { !$0.isEmpty }.joined(separator: "\n")]
    }

    private static func end() {
        NotificationCenter.default.removeObserver(Watcher.shared, name: NSMenu.didBeginTrackingNotification, object: nil)
        leave()
        tracked?.cancelTrackingWithoutAnimation()
        tracked = nil
    }

    /// The pop-up button's cell the element is or is in, and its menu.
    private static func popUp(_ element: WalkAX) -> (NSPopUpButtonCell, NSMenu)? {
        var current: WalkAX? = element
        var hops = 0
        while let now = current, !now.isWindow, hops <= 6 {
            let cell = now.object as? NSPopUpButtonCell ?? (now.object as? NSPopUpButton)?.cell as? NSPopUpButtonCell
            if let cell, let menu = cell.menu { return (cell, menu) }
            current = now.parent
            hops += 1
        }
        return nil
    }

    /// The element, or the nearest above it, that is a menu button.
    private static func menuButton(_ element: WalkAX) -> WalkAX? {
        var current: WalkAX? = element
        var hops = 0
        while let now = current, !now.isWindow, hops <= 6 {
            if ["AXMenuButton", "AXPopUpButton"].contains(now.role) { return now }
            current = now.parent
            hops += 1
        }
        return nil
    }
}
#endif
