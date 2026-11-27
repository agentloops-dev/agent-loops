import CoreGraphics

/// Where a scroll over a long prompt puts its words (#6887).
///
/// The Mac prompt is a text field, and a text field takes no scroll of its own: the
/// wheel or the trackpad over a prompt taller than its field scrolled the transcript
/// above it. The prompt now takes it while it can move, and gives it to the transcript
/// at its top or bottom, or when all its words show.
public enum PromptScroll {
    /// The new top of what shows, or nil when the prompt cannot move that way.
    ///
    /// - Parameters:
    ///   - top: how far down the words the field shows now, in points.
    ///   - delta: the scroll's distance, as `NSEvent.scrollingDeltaY` gives it: more
    ///     than zero goes up the words, to their start.
    ///   - content: the height of all the words.
    ///   - visible: the height of the field that shows them.
    public static func top(from top: CGFloat, delta: CGFloat, content: CGFloat, visible: CGFloat) -> CGFloat? {
        let bottom = max(content - visible, 0)
        guard bottom > 0.5, delta != 0 else { return nil }
        let moved = min(max(top - delta, 0), bottom)
        return abs(moved - top) < 0.5 ? nil : moved
    }
}
