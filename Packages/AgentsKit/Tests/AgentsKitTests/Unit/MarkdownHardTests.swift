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
            case .image(let source, let alt): "\(alt) \(source)"
            case .rule: ""
            }
        }.joined(separator: "\n")
    }
}
