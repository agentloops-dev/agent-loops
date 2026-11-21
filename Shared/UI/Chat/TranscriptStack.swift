import AgentsKitCore
import SwiftUI

/// The id a row of the transcript is laid out by.
struct TranscriptRowID: LayoutValueKey {
    static let defaultValue: UUID? = nil
}

/// The transcript's rows, laid out by their own measured heights (#2310). Only the rows
/// drawn are its subviews; the others are known by their heights. Each drawn row is
/// measured in the same pass that places it, so a row that changed height moves the rows
/// around it in that pass, and the slack above takes it before anything is drawn.
struct TranscriptStack: Layout {
    /// Every row, top first, drawn or not.
    let ids: [UUID]
    /// The rows' own places, as the geometry has them: a line in a turn says where it is in
    /// these (#2860).
    static let space = "transcriptRows"
    let geometry: TranscriptGeometry
    /// Changed to lay the rows out again when what holds them changed outside a pass.
    var pass = 0

    /// The width the drawn rows were last measured at, in this layout of them: the pass that
    /// sizes the stack measures them, and the one that places them uses what it measured
    /// (P20261022-18). Nil once the rows or the stack change.
    struct Cache {
        var measuredWidth: Double?
    }

    func makeCache(subviews: Subviews) -> Cache { Cache() }

    func updateCache(_ cache: inout Cache, subviews: Subviews) {
        cache.measuredWidth = nil
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout Cache) -> CGSize {
        let width = proposal.width ?? subviews.map { $0.sizeThatFits(.unspecified).width }.max() ?? 0
        measure(subviews, width: width)
        cache.measuredWidth = width
        return CGSize(width: width, height: geometry.place(ids).height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout Cache) {
        if cache.measuredWidth.map({ abs($0 - bounds.width) > 0.5 }) ?? true {
            measure(subviews, width: bounds.width)
            cache.measuredWidth = bounds.width
        }
        _ = geometry.place(ids)
        for subview in subviews {
            guard let id = subview[TranscriptRowID.self], let top = geometry.top(of: id) else { continue }
            subview.place(at: CGPoint(x: bounds.minX, y: bounds.minY + top), anchor: .topLeading,
                          proposal: ProposedViewSize(width: bounds.width, height: nil))
        }
    }

    private func measure(_ subviews: Subviews, width: Double) {
        guard width > 0 else { return }
        for subview in subviews {
            guard let id = subview[TranscriptRowID.self] else { continue }
            let size = subview.sizeThatFits(ProposedViewSize(width: width, height: nil))
            geometry.measure(id, size.height, width: width)
        }
    }
}
