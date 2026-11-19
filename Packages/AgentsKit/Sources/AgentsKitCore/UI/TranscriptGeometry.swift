import Foundation

/// What the transcript knows of its own geometry (#2310): every row's height, measured once
/// it was drawn or estimated until then, where each row was last placed, and how the pane is
/// held. A class, read and written as the rows are laid out, so knowing it draws nothing again.
/// Not isolated, as a layout's own methods are not; only the main thread lays out and reads it.
/// In AgentsKitCore, not beside `TranscriptStack`, so a test can lay it out as a pass does (#6189).
public final class TranscriptGeometry: @unchecked Sendable {
    public init() {}

    /// Every row in order, as last laid out.
    public private(set) var ids: [UUID] = []
    /// Where each row starts, as last laid out, in the order of `ids`.
    public private(set) var tops: [Double] = []
    /// How tall each row is, in the order of `ids`.
    public private(set) var heights: [Double] = []
    /// The content's height, as last laid out.
    public private(set) var height: Double = 0
    /// Rows measured at `width`, by id. A row never drawn takes `estimate`.
    private var measured: [UUID: Double] = [:]
    private var width: Double = 0
    /// The empty space above the first row, which takes a change above the reader.
    public var slack: Double = 0
    /// The row a reader's place is held by, and where it starts: nil while following the end.
    public var anchor: (id: UUID, top: Double)?
    /// Told, after the pass, that the slack changed: a page landed above, or a row above
    /// the reader changed height. The pane decides then whether its margin follows, not at the
    /// next scroll, or a reader near the top stops at a top that is no longer there (#4087).
    public var slackChanged: (@MainActor () -> Void)?
    /// How far the content moved down, all at once, because the slack ran out: the pane
    /// moves by as much (`TranscriptPlacement.Placed.shift`). Taken by the transcript.
    public var shift: Double = 0

    /// What a row never drawn is taken to be: the mean of those drawn, or a guess.
    public var estimate: Double {
        measured.isEmpty ? 160 : measured.values.reduce(0, +) / Double(measured.count)
    }

    /// The slack a conversation opens with, so earlier pages land without moving the pane:
    /// enough for many. It costs nothing: the pane is kept out of it by a margin, so neither
    /// the reader nor the scroller sees it.
    public static let reserve: Double = 500_000

    /// A row's height at `width`, measured in this layout pass.
    public func measure(_ id: UUID, _ height: Double, width: Double) {
        if abs(width - self.width) > 0.5 {
            // Another width: every height measured is wrong, though nothing above the
            // reader moves while the anchor holds.
            measured = [:]
            self.width = width
        }
        measured[id] = height
    }

    /// Lays every row out: the drawn ones measured, the rest as known. Bounded: heights are
    /// kept for the rows the transcript has, not for every row it ever had.
    public func place(_ ids: [UUID]) -> TranscriptPlacement.Placed {
        if self.ids != ids {
            let now = Set(ids)
            if measured.count > ids.count * 2 { measured = measured.filter { now.contains($0.key) } }
            // The anchor's row gone (a page joined onto it): the next one still here.
            if let held = anchor, !now.contains(held.id),
               let at = self.ids.firstIndex(of: held.id),
               let next = self.ids[at...].first(where: now.contains),
               let top = self.ids.firstIndex(of: next).map({ tops[$0] }) {
                anchor = (next, top)
            }
        }
        let guess = estimate
        let heights = ids.map { measured[$0] ?? guess }
        let index = anchor.flatMap { held in ids.firstIndex(of: held.id).map { ($0, held.top) } }
        let placed = TranscriptPlacement.place(heights: heights, slack: slack,
                                               anchor: index.map { (index: $0.0, top: $0.1) },
                                               reserve: Self.reserve)
        self.ids = ids
        self.heights = heights
        tops = placed.tops
        height = placed.height
        if abs(placed.slack - slack) > 0.5, let slackChanged {
            DispatchQueue.main.async { slackChanged() }
        }
        slack = placed.slack
        if placed.shift > 0, let held = anchor {
            anchor = (held.id, held.top + placed.shift)
            shift += placed.shift
        }
        return placed
    }

    /// Where a row starts, as last laid out.
    public func top(of id: UUID) -> Double? { ids.firstIndex(of: id).map { tops[$0] } }

    /// Holds the reader's place by the row at the top of `from..<to`: nothing moves, as the
    /// row is where it was laid out.
    public func hold(from: Double, to: Double) {
        guard let at = TranscriptPlacement.anchor(tops: tops, heights: heights, visibleTop: from, visibleBottom: to)
        else { return }
        anchor = (ids[at.index], at.top)
    }

    /// Following the end again: the rows go down from where the first one is now.
    public func follow() {
        anchor = nil
    }

    /// Opened afresh: nothing measured, nothing held.
    public func reset(slack: Double) {
        ids = []
        tops = []
        heights = []
        height = 0
        measured = [:]
        anchor = nil
        shift = 0
        self.slack = slack
    }
}
