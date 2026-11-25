import Foundation

/// The images of one message or page that the image viewer goes through (#4954): in their order,
/// without the small ones and the ones that are links. `pageImage.ts` has the same rules for the
/// web page.
public enum ImageSequence {
    /// The least an image's longer side is, in points, to open large and be in the sequence. A
    /// logo, a badge or an icon is smaller, and stays as it is in the text.
    public static let least = 200.0

    /// Whether an image of this size opens large and is in the sequence.
    public static func opensLarge(width: Double, height: Double) -> Bool {
        max(width, height) >= least
    }

    /// The image after (`by` 1) or before (`by` -1) the one at `index`, or nil at an end.
    public static func step(from index: Int, count: Int, by: Int) -> Int? {
        let next = index + by
        return (0..<count).contains(next) && (0..<count).contains(index) ? next : nil
    }

    /// "2 of 5", or nil when there is only the one image.
    public static func counter(_ index: Int, of count: Int) -> String? {
        count > 1 ? "\(index + 1) of \(count)" : nil
    }

    /// One image of a message or page: its source and its alternative text.
    public struct Item: Equatable, Sendable {
        public let source: String
        public let alt: String

        public init(source: String, alt: String) {
            self.source = source
            self.alt = alt
        }
    }

    /// The images in `blocks`, in their order, each source once. An image that is a link
    /// is not an image block at all: Foundation reads it as the link's words, whose click follows it.
    public static func items(in blocks: [MarkdownBlock]) -> [Item] {
        var seen = Set<String>()
        var found: [Item] = []
        func walk(_ blocks: [MarkdownBlock]) {
            for block in blocks {
                switch block {
                case .image(let source, let alt):
                    if seen.insert(source).inserted { found.append(Item(source: source, alt: alt)) }
                case .list(_, _, let items):
                    for item in items { walk(item.blocks) }
                case .quote(let inner):
                    walk(inner)
                default:
                    break
                }
            }
        }
        walk(blocks)
        return found
    }

    /// A run of blocks that are drawn together on a live page: one image, or the words between.
    public struct Piece: Hashable, Sendable {
        public let range: Range<Int>
        public let isImage: Bool

        public init(range: Range<Int>, isImage: Bool) {
            self.range = range
            self.isImage = isImage
        }
    }

    /// `blocks` with each image on its own (#6624), so it can open in the viewer beside words
    /// whose click does something else. An image in a list or a quote stays in its piece.
    public static func pieces(of blocks: [MarkdownBlock]) -> [Piece] {
        var pieces: [Piece] = []
        for (index, block) in blocks.enumerated() {
            if case .image = block {
                pieces.append(Piece(range: index..<(index + 1), isImage: true))
            } else if let last = pieces.last, !last.isImage {
                pieces[pieces.count - 1] = Piece(range: last.range.lowerBound..<(index + 1), isImage: false)
            } else {
                pieces.append(Piece(range: index..<(index + 1), isImage: false))
            }
        }
        return pieces
    }
}
