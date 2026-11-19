import SwiftUI

extension View {
    /// The least a window can be sized to, whatever its content holds (#5739).
    ///
    /// A `WindowGroup` keeps its window at least as big as its content's own minimum
    /// (`windowResizability` is `.contentMinSize` by default). So a part of the content
    /// that does not scroll and grows (the sidebar's foot when a line comes in, a page's
    /// stack) made the window grow with it, past the bottom of the screen. With a minimum
    /// and an infinite maximum here, the frame's own minimum is this one, never its
    /// content's: the content gets the window's height and scrolls or clips in it.
    ///
    /// Width stays with the content when none is given: the split view's columns set it.
    func windowFloor(width: CGFloat? = nil, height: CGFloat) -> some View {
        frame(minWidth: width, maxWidth: width == nil ? nil : .infinity,
              minHeight: height, maxHeight: .infinity)
    }

    /// A split view column whose content can never raise the split view's height (#5932).
    ///
    /// AppKit asks a column for its least size at almost no width. Wrapped text that is
    /// `fixedSize` on its height (the sidebar foot's "not answering" banner, an offline
    /// machine's line) then wraps a character to a line and reports thousands of points. The
    /// split view took that as its height, the window's floor kept the window at its own
    /// height, and the split view stood centred in it, taller than the window: the sidebar
    /// blank and the prompt bar cut off. The column's least height is none; the list scrolls
    /// and the foot clips in the window's height.
    func columnFloor() -> some View {
        frame(minHeight: 0, maxHeight: .infinity)
    }
}
