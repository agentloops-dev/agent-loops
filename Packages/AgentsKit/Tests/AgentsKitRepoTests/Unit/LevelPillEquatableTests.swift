import Foundation
import Testing

/// #6897: on the Mac, the Model pill's submenus (⋯ <runtime>'s own, Model) opened and closed
/// again and again under the pointer. The pill's menu is an NSMenu that SwiftUI makes again
/// each time the pill's body runs, which closes an open submenu; the pointer on its item
/// opens it again. The pill took closures, so SwiftUI could never tell it was the same, and
/// every change to the prompt bar ran its body again.
///
/// `LevelPill` is now a value, read once when it is made, and `Equatable`; each place that
/// makes one adds `.equatable()`, so SwiftUI runs its body only when what it shows changes.
/// A source scan, for the reason `OneGroupingTests` gives: the line that breaks it does not
/// exist yet. The matcher is proved against samples first.
@Suite("Level pill is equatable")
struct LevelPillEquatableTests {
    private static let root = URL(filePath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()

    /// Each `LevelPill(…)` in the text, with its trailing closure, and whether `.equatable()`
    /// comes next.
    static func pills(in text: String) -> [(line: Int, equatable: Bool)] {
        var found: [(Int, Bool)] = []
        var search = text.startIndex
        while let start = text.range(of: "LevelPill(", range: search..<text.endIndex) {
            search = start.upperBound
            let before = text[..<start.lowerBound].last
            if let before, before.isLetter || before.isNumber || before == "_" { continue }
            var index = close(text, from: text.index(before: start.upperBound), open: "(", shut: ")")
            var rest = text[index...].drop { $0 == " " }
            if rest.first == "{" {
                index = close(text, from: rest.startIndex, open: "{", shut: "}")
                rest = text[index...]
            }
            let next = rest.drop { $0.isWhitespace }
            let line = text[..<start.lowerBound].filter { $0 == "\n" }.count + 1
            found.append((line, next.hasPrefix(".equatable()")))
        }
        return found
    }

    /// The index after the bracket that closes the one at `from`.
    private static func close(_ text: String, from: String.Index, open: Character, shut: Character) -> String.Index {
        var depth = 0
        var index = from
        while index < text.endIndex {
            if text[index] == open { depth += 1 }
            if text[index] == shut {
                depth -= 1
                if depth == 0 { return text.index(after: index) }
            }
            index = text.index(after: index)
        }
        return text.endIndex
    }

    @Test("the matcher finds a pill with and without .equatable()")
    func matcher() {
        let with = """
        LevelPill(field: .model, chosen: nil, cell: { f($0) }) { level in
            choose(level)
        }
        .equatable()
        """
        let without = """
        LevelPill(field: .model, chosen: nil, cell: { f($0) }) {
            choose($0)
        }
        .help("x")
        """
        #expect(Self.pills(in: with).map(\.equatable) == [true])
        #expect(Self.pills(in: without).map(\.equatable) == [false])
        #expect(Self.pills(in: "struct LevelPill: View {}").isEmpty)
    }

    @Test("LevelPill is Equatable, and every one that is made adds .equatable()")
    func everyPillIsEquatable() throws {
        let pill = try String(contentsOf: Self.root.appending(path: "Shared/UI/Chat/LevelPill.swift"), encoding: .utf8)
        #expect(pill.contains("struct LevelPill: View, Equatable"))
        var made = 0
        for directory in ["App/Sources", "Remote/Sources", "Shared/UI"] {
            guard let walker = FileManager.default.enumerator(at: Self.root.appending(path: directory),
                                                              includingPropertiesForKeys: nil) else { continue }
            while let url = walker.nextObject() as? URL {
                guard url.pathExtension == "swift" else { continue }
                let text = try String(contentsOf: url, encoding: .utf8)
                for found in Self.pills(in: text) {
                    made += 1
                    #expect(found.equatable, "\(url.lastPathComponent):\(found.line) makes a LevelPill without .equatable() (#6897)")
                }
            }
        }
        #expect(made > 0)
    }
}
