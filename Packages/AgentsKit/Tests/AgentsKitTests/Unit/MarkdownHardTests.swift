import Foundation
import Testing
@testable import AgentsKitCore

/// Markdown a renderer may not expect does not stop the chat, and no word of it is lost (#6189).
///
/// The corpus is `Fixtures/markdown-hard` (deep lists and quotes, huge and ragged tables, fences
/// that never close, text with no breaks), written by its `make.mjs`. `MarkdownBlock` is what the Mac
/// window and the Remote draw a message from; `Web/test/markdownHard.test.mjs` feeds the same files to
/// the web page's renderer. Each word of a file is marked `k<n>`, so "drawn" is checked by those marks.
@Suite("Hard Markdown")
struct MarkdownHardTests {
    static let directory = URL(filePath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "Fixtures/markdown-hard")

    static let files: [String] = {
        ((try? FileManager.default.contentsOfDirectory(atPath: directory.path())) ?? [])
            .filter { $0.hasSuffix(".md") }.sorted()
    }()

    @Test func theCorpusIsThere() {
        #expect(Self.files.count >= 4)
    }

    @Test(arguments: files)
    func everyWordIsDrawn(_ name: String) throws {
        let text = try String(contentsOf: Self.directory.appending(path: name), encoding: .utf8)
        let words = Self.marks(in: text)
        #expect(words.count > 10)
        let read = Self.marks(in: Self.text(of: MarkdownBlock.parse(text)))
        #expect(words.subtracting(read).sorted() == [], "read whole")
        // As a view reads it: in parts, each remembered (`MarkdownBlock.parsed`).
        let parted = Self.marks(in: Self.text(of: MarkdownCache().blocks(for: text)))
        #expect(words.subtracting(parted).sorted() == [], "read in parts")
    }

    /// A reply that streams is read again at each chunk, so every cut of it must read too: in a fence,
    /// a table row or a list item, cut part way.
    @Test(arguments: files)
    func everyCutOfAStreamedReplyReads(_ name: String) throws {
        let text = try String(contentsOf: Self.directory.appending(path: name), encoding: .utf8)
        let cache = MarkdownCache()
        let cuts = 8
        for cut in 1...cuts {
            let prefix = String(text.prefix(text.count * cut / cuts))
            let drawn = Self.marks(in: Self.text(of: cache.blocks(for: prefix)))
            // The word at the cut may be cut too ("k12" of "k123"), so only whole words are counted.
            let whole = Set(prefix.matches(of: /k\d+(?=\D)/).map { String($0.output) })
            #expect(whole.subtracting(drawn).sorted() == [], "cut \(cut) of \(cuts)")
        }
    }

    /// A large table that streams is read again at its last rows, not whole at each chunk (#6299): the
    /// 40-column, 300-row table read 0.44 s a time on the main thread.
    @Test func aStreamedTableIsReadAtItsLastRows() throws {
        let text = try String(contentsOf: Self.directory.appending(path: "huge-tables.md"), encoding: .utf8)
        let cache = MarkdownCache()
        let chunk = 400
        var end = text.startIndex
        var most = 0
        while end < text.endIndex {
            end = text.index(end, offsetBy: chunk, limitedBy: text.endIndex) ?? text.endIndex
            let before = cache.charactersRead
            let blocks = cache.blocks(for: String(text[..<end]))
            #expect(!blocks.isEmpty)
            most = max(most, cache.charactersRead - before)
        }
        // A part is about 2,000 characters and the table's head, and a cut reads two parts.
        #expect(most < 8_000, "read \(most) of \(text.utf8.count) characters at one chunk")
        #expect(cache.blocks(for: text) == MarkdownBlock.parse(text))
    }

    /// A long table cut by rows reads as the same table, at every row as it arrives: alignments,
    /// marks and escaped pipes in its cells, short and long rows, and what comes after it.
    @Test func aTableCutByRowsReadsAsOne() {
        let filler = String(repeating: "Words enough to fill a part of the reading. ", count: 60)
        let rows = (0..<120).map { index -> String in
            switch index % 4 {
            case 0: "| row \(index) | *marked* `code` | a \\| pipe |"
            case 1: "| short \(index) |"
            case 2: "| long \(index) | b | c | d | e |"
            default: "| **\(index)** | [link](https://example.com) | ok |"
            }
        }
        let table = "| one | two | three |\n| :-- | :-: | --: |\n" + rows.joined(separator: "\n")
        let shapes = [
            filler + "\n\n" + table + "\n\nAfter the table.",
            table + "\n\n" + filler,
            filler + "\n\n" + table + "\nlazy row after it\n\n" + table,
            filler + "\n\nA paragraph line\n" + table,
        ]
        #expect(MarkdownBlock.parts(shapes[0]).contains { $0.head != nil })
        for shape in shapes {
            #expect(MarkdownCache().blocks(for: shape) == MarkdownBlock.parse(shape), "\(shape.prefix(80))")
            let cache = MarkdownCache()
            let lines = shape.components(separatedBy: "\n")
            for end in lines.indices where end % 3 == 0 || end == lines.count - 1 {
                let prefix = lines[...end].joined(separator: "\n")
                #expect(cache.blocks(for: prefix) == MarkdownBlock.parse(prefix), "\(prefix.suffix(60))")
            }
        }
    }

    static func marks(in text: String) -> Set<String> {
        Set(text.matches(of: /k\d+/).map { String($0.output) })
    }

    /// Every word a block draws, as plain text.
    static func text(of blocks: [MarkdownBlock]) -> String {
        blocks.map { block -> String in
            switch block {
            case .heading(_, let text), .paragraph(let text): String(text.characters)
            case .list(_, _, let items): items.map { text(of: $0.blocks) }.joined(separator: "\n")
            case .quote(let inner): text(of: inner)
            case .code(_, let code): code
            case .table(let table):
                (table.header + table.rows.flatMap { $0 }).map { String($0.characters) }.joined(separator: " ")
            case .image(let source, let alt, _): "\(alt) \(source)"
            case .rule: ""
            }
        }.joined(separator: "\n")
    }
}
