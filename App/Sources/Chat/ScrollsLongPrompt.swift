import AgentsKitCore
import AppKit
import SwiftUI

/// The mouse wheel or the trackpad over a prompt taller than its field scrolls the
/// prompt, not the transcript above it (#6887).
///
/// The prompt is a vertical `TextField`, and its field editor sits in a clip view with
/// no scroll view of its own, so AppKit gave each scroll to the nearest one above: the
/// chat's. A scroll over the field, in its window, is looked at here first. While the
/// field is being edited and its words can move that way, they move, and the scroll goes
/// no further. At the top or the bottom, or when every line shows, it goes on to the
/// transcript, as before. A trackpad gesture that moved the prompt keeps to the prompt
/// until it and its momentum end, so the transcript does not take over halfway.
///
/// A field that is not being edited has no field editor to scroll: its scroll goes on to
/// the transcript, as before.
struct ScrollsLongPrompt: ViewModifier {
    @State private var watcher: Any?
    @State private var anchor = PromptScrollAnchor()

    func body(content: Content) -> some View {
        content
            .background(PromptScrollAnchorView(anchor: anchor))
            .onAppear(perform: watch)
            .onDisappear(perform: stopWatching)
    }

    private func watch() {
        guard watcher == nil else { return }
        watcher = NSEvent.addLocalMonitorForEvents(matching: .scrollWheel) { [anchor] event in
            MainActor.assumeIsolated { anchor.take(event) } ? nil : event
        }
    }

    private func stopWatching() {
        guard let watcher else { return }
        NSEvent.removeMonitor(watcher)
        self.watcher = nil
    }
}

/// Where the prompt field is, kept by an AppKit view laid behind it, and the gesture
/// it holds.
@MainActor
final class PromptScrollAnchor {
    weak var view: NSView?
    /// A trackpad gesture moved the prompt: the rest of it, momentum too, is the prompt's.
    private var holdsGesture = false

    /// Scrolls the prompt by `event`. Answers whether it took the event.
    func take(_ event: NSEvent) -> Bool {
        let starts = event.phase == .began || event.phase == .mayBegin
        if starts { holdsGesture = false }
        let ends = event.momentumPhase == .ended || event.momentumPhase == .cancelled
            || (event.momentumPhase.isEmpty && (event.phase == .ended || event.phase == .cancelled))
        defer { if ends { holdsGesture = false } }

        guard let clip = clipView(under: event), let editor = clip.documentView else { return holdsGesture }
        let delta = event.hasPreciseScrollingDeltas ? event.scrollingDeltaY : event.scrollingDeltaY * Self.lineHeight(of: editor)
        guard let top = PromptScroll.top(from: clip.bounds.origin.y, delta: delta,
                                         content: editor.frame.height, visible: clip.bounds.height)
        else { return holdsGesture }
        clip.scroll(to: NSPoint(x: clip.bounds.origin.x, y: top))
        if let scroller = clip.enclosingScrollView { scroller.reflectScrolledClipView(clip) }
        if !event.phase.isEmpty || !event.momentumPhase.isEmpty { holdsGesture = !ends }
        return true
    }

    /// The clip view of the field editor that edits the prompt, when `event` is over the
    /// prompt in its own window.
    private func clipView(under event: NSEvent) -> NSClipView? {
        guard let view, let window = view.window, window.windowNumber == event.windowNumber,
              view.bounds.contains(view.convert(event.locationInWindow, from: nil)),
              let content = window.contentView,
              var hit = content.hitTest(content.convert(event.locationInWindow, from: nil))
        else { return nil }
        while true {
            if let field = hit as? NSTextField {
                guard let editor = field.currentEditor() as? NSTextView else { return nil }
                return editor.superview as? NSClipView
            }
            guard let parent = hit.superview else { return nil }
            hit = parent
        }
    }

    /// A mouse wheel's notch is a line: its delta counts lines, not points.
    private static func lineHeight(of editor: NSView) -> CGFloat {
        guard let text = editor as? NSTextView, let font = text.font,
              let layout = text.layoutManager else { return 16 }
        return layout.defaultLineHeight(for: font)
    }
}

private struct PromptScrollAnchorView: NSViewRepresentable {
    let anchor: PromptScrollAnchor

    func makeNSView(context: Context) -> NSView {
        let view = PassThrough()
        anchor.view = view
        return view
    }

    func updateNSView(_ view: NSView, context: Context) {
        anchor.view = view
    }

    /// Where the prompt is, never what a click lands on.
    private final class PassThrough: NSView {
        override func hitTest(_ point: NSPoint) -> NSView? { nil }
    }
}
