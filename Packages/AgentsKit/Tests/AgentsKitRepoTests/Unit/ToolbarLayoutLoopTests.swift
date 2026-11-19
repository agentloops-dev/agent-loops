import Foundation
import Testing

/// #5947: the Mac window quit on AppKit's Update Constraints loop. The crash's backtrace
/// ran from `NSHostingView.layout()` through `ToolbarBridge.preferencesDidChange` and
/// `Toolbar.LocationStorage.updatedVendedItems(newFocusedValues:)` to
/// `setNeedsUpdateConstraints`: during the layout pass, the window's toolbar changed, and
/// that asked for one more pass, again and again.
///
/// Two things can change a toolbar during layout, and this scan keeps both out:
/// - a toolbar built from a width that the layout measures (`windowWidth`, `paneWidth`,
///   a geometry proxy's size): each new width built the toolbar again;
/// - a focused value that is a new value at each body, because it is not `Equatable`:
///   the toolbar takes the focused values too.
///
/// A source scan, for the reason `OneGroupingTests` gives: the line that breaks it does
/// not exist yet. The matchers are proved against samples first.
@Suite("Toolbar layout loop")
struct ToolbarLayoutLoopTests {
    private static let root = URL(filePath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()

    private struct Source {
        let path: String
        let text: String
    }

    private static func sources(under directories: [String]) throws -> [Source] {
        var found: [Source] = []
        for directory in directories {
            let base = root.appending(path: directory)
            guard let walker = FileManager.default.enumerator(at: base, includingPropertiesForKeys: nil) else {
                continue
            }
            while let url = walker.nextObject() as? URL {
                guard url.pathExtension == "swift" else { continue }
                found.append(Source(path: String(url.path.dropFirst(root.path.count + 1)),
                                    text: try String(contentsOf: url, encoding: .utf8)))
            }
        }
        return found.sorted { $0.path < $1.path }
    }

    /// The text with each line's `//` comment taken out.
    private static func code(_ text: String) -> String {
        text.components(separatedBy: "\n").map { line in
            line.range(of: "//").map { String(line[..<$0.lowerBound]) } ?? line
        }.joined(separator: "\n")
    }

    /// The body of each toolbar: `.toolbar { … }` and each `@ToolbarContentBuilder`
    /// declaration, from its first `{` to the brace that closes it.
    static func toolbarBodies(in text: String) -> [String] {
        let text = code(text)
        var bodies: [String] = []
        for marker in [".toolbar {", "@ToolbarContentBuilder"] {
            var search = text.startIndex
            while let found = text.range(of: marker, range: search..<text.endIndex) {
                search = found.upperBound
                guard let open = text[found.lowerBound...].firstIndex(of: "{") else { break }
                var depth = 0
                var index = open
                while index < text.endIndex {
                    if text[index] == "{" { depth += 1 }
                    if text[index] == "}" {
                        depth -= 1
                        if depth == 0 { break }
                    }
                    index = text.index(after: index)
                }
                bodies.append(String(text[open..<min(index, text.endIndex)]))
            }
        }
        return bodies
    }

    /// The widths and sizes that a layout pass measures.
    private static let measured = ["windowWidth", "paneWidth", ".size.width", ".size.height"]

    static func readsAMeasuredSize(_ body: String) -> Bool {
        measured.contains { body.contains($0) }
    }

    /// The type of each `@Entry` in an `extension FocusedValues`.
    static func focusedValueTypes(in text: String) -> [String] {
        let text = code(text)
        var types: [String] = []
        var search = text.startIndex
        while let found = text.range(of: "extension FocusedValues", range: search..<text.endIndex) {
            search = found.upperBound
            guard let close = text[found.upperBound...].firstIndex(of: "}") else { break }
            for line in text[found.upperBound..<close].components(separatedBy: "\n") {
                guard line.contains("@Entry var"), let colon = line.firstIndex(of: ":") else { continue }
                let type = line[line.index(after: colon)...]
                    .trimmingCharacters(in: .whitespaces)
                    .components(separatedBy: CharacterSet(charactersIn: "?= ")).first ?? ""
                types.append(type)
            }
        }
        return types
    }

    /// Whether `type` is declared as a class (one identity, so never a new value) or as
    /// an `Equatable` struct or enum in `text`.
    static func isStable(_ type: String, declaredIn text: String) -> Bool {
        let text = code(text)
        for kind in ["class", "struct", "enum"] {
            let pattern = #"(final\s+)?"# + kind + #"\s+"# + type + #"\b[^{]*\{"#
            guard let range = text.range(of: pattern, options: .regularExpression) else { continue }
            if kind == "class" { return true }
            let header = text[range]
            return header.contains("Equatable") || header.contains("Hashable")
        }
        return false
    }

    @Test func theScansCatchAMeasuredToolbarAndANewFocusedValue() throws {
        let toolbar = """
            .toolbar {
                ToolbarItem {
                    SidebarToggle(windowWidth: frame.windowWidth)
                }
            }
            """
        let toolbarBodies = Self.toolbarBodies(in: toolbar)
        #expect(toolbarBodies.count == 1)
        #expect(Self.readsAMeasuredSize(try #require(toolbarBodies.first)))
        let builder = """
            @ToolbarContentBuilder
            private var bar: some ToolbarContent {
                if showsChat { ToolbarItem { SidebarToggle() } }
            }
            // a comment that says windowWidth
            let width = frame.windowWidth
            """
        let builderBodies = Self.toolbarBodies(in: builder)
        #expect(builderBodies.count == 1)
        #expect(!Self.readsAMeasuredSize(try #require(builderBodies.first)))

        let focused = """
            struct Zoom {
                let level: Binding<Double>
            }
            struct Same: Equatable { let level: Double }
            final class Window {}
            extension FocusedValues {
                @Entry var zoom: Zoom?
                @Entry var same: Same?
                @Entry var window: Window?
            }
            """
        #expect(Self.focusedValueTypes(in: focused) == ["Zoom", "Same", "Window"])
        #expect(!Self.isStable("Zoom", declaredIn: focused))
        #expect(Self.isStable("Same", declaredIn: focused))
        #expect(Self.isStable("Window", declaredIn: focused))
    }

    @Test func noToolbarReadsAWidthTheLayoutMeasures() throws {
        let sources = try Self.sources(under: ["App/Sources", "Shared/UI"])
        #expect(sources.count > 60, "too few sources were read; the repository root is wrong")
        var readers: [String] = []
        var toolbars = 0
        for source in sources {
            for body in Self.toolbarBodies(in: source.text) {
                toolbars += 1
                if Self.readsAMeasuredSize(body) { readers.append(source.path) }
            }
        }
        #expect(toolbars > 10, "too few toolbars were found; the scan is wrong")
        #expect(readers.isEmpty, """
            A toolbar reads a width or size that the layout measures. Each new width builds \
            the toolbar again in the layout pass, and that asks AppKit for another pass, \
            until it throws (#5947). Read the width in the toolbar item's own view instead, \
            as SidebarToggle does.
            \(readers.joined(separator: "\n"))
            """)
    }

    @Test func everyFocusedValueIsAClassOrEquatable() throws {
        let sources = try Self.sources(under: ["App/Sources", "Shared/UI"])
        let all = sources.map(\.text).joined(separator: "\n")
        var types: [String] = []
        for source in sources { types += Self.focusedValueTypes(in: source.text) }
        #expect(!types.isEmpty, "no focused values were found; the scan is wrong")
        let unstable = types.filter { !Self.isStable($0, declaredIn: all) }
        #expect(unstable.isEmpty, """
            These focused values are neither a class nor Equatable, so each new body makes a \
            new value, and the window's toolbar is updated again, also in a layout pass \
            (#5947): \(unstable.joined(separator: ", "))
            """)
    }
}
