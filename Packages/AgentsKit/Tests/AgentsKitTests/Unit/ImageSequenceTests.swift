import AgentsKitCore
import Foundation
import Testing

@Suite("The image viewer's sequence (#4954)")
struct ImageSequenceTests {
    @Test("an image under 200 points on its longer side does not open large")
    func smallImages() {
        #expect(!ImageSequence.opensLarge(width: 20, height: 20))
        #expect(!ImageSequence.opensLarge(width: 199, height: 120))
        #expect(ImageSequence.opensLarge(width: 200, height: 40))
        #expect(ImageSequence.opensLarge(width: 80, height: 900))
    }

    @Test("a step goes one image on or back, and there is none past either end")
    func steps() {
        #expect(ImageSequence.step(from: 0, count: 3, by: 1) == 1)
        #expect(ImageSequence.step(from: 2, count: 3, by: -1) == 1)
        #expect(ImageSequence.step(from: 0, count: 3, by: -1) == nil)
        #expect(ImageSequence.step(from: 2, count: 3, by: 1) == nil)
        #expect(ImageSequence.step(from: 0, count: 1, by: 1) == nil)
        #expect(ImageSequence.step(from: 5, count: 3, by: -1) == nil)
    }

    @Test("the counter shows only when there is more than one image")
    func counter() {
        #expect(ImageSequence.counter(1, of: 5) == "2 of 5")
        #expect(ImageSequence.counter(0, of: 1) == nil)
    }

    @Test("the sequence is the page's images in order, each once, also in lists and quotes, and no link")
    func sources() {
        let blocks = MarkdownBlock.parse("""
        ![one](https://example.com/1.png)

        [![badge](https://example.com/badge.svg)](https://example.com/ci)

        - an item ![two](https://example.com/2.png)

        > ![three](https://example.com/3.png)

        ![one again](https://example.com/1.png)
        """)
        #expect(ImageSequence.items(in: blocks).map(\.source) == [
            "https://example.com/1.png", "https://example.com/2.png", "https://example.com/3.png",
        ])
    }

    @Test("each item has its alternative text")
    func alts() {
        let items = ImageSequence.items(in: MarkdownBlock.parse("![one](1.png)\n\n![two](2.png)"))
        #expect(items == [.init(source: "1.png", alt: "one"), .init(source: "2.png", alt: "two")])
    }

    @Test("each image is a piece of its own, and the words between are one")
    func pieces() {
        let blocks = MarkdownBlock.parse("# Title\n\nWords.\n\n![one](1.png)\n\n![two](2.png)\n\nMore.")
        #expect(ImageSequence.pieces(of: blocks) == [
            .init(range: 0..<2, isImage: false), .init(range: 2..<3, isImage: true),
            .init(range: 3..<4, isImage: true), .init(range: 4..<5, isImage: false),
        ])
        #expect(ImageSequence.pieces(of: MarkdownBlock.parse("Words only.")).contains { $0.isImage } == false)
    }

    @Test("an image that is a link is the link's words, so its click follows the link")
    func linkedImage() {
        let blocks = MarkdownBlock.parse("[![badge](https://example.com/b.svg)](https://example.com/ci)")
        guard case .paragraph(let text)? = blocks.first, blocks.count == 1 else {
            Issue.record("not one paragraph: \(blocks)"); return
        }
        #expect(text.runs.contains { $0.link == URL(string: "https://example.com/ci") })
    }
}
