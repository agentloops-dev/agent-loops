import AppKit

/// The prompt field's cursor, read from and put in its field editor, in the character
/// offsets `DictationText` counts in (#6878). The prompt's `TextField` has no `selection:`
/// binding: that binding trails the typing, and a redraw put its old place back.
@MainActor
enum FieldEditorCursor {
    /// The key window's field editor, when it is editing `text`.
    private static func editor(for text: String) -> NSTextView? {
        guard let editor = NSApp.keyWindow?.firstResponder as? NSTextView, editor.string == text else { return nil }
        return editor
    }

    /// Where the cursor is in `text`, or nil when no field editor holds it.
    static func range(in text: String) -> Range<Int>? {
        editor(for: text).flatMap { range(of: $0, in: text) }
    }

    /// Where `editor`'s cursor is in `text`, when it holds `text`.
    static func range(of editor: NSTextView, in text: String) -> Range<Int>? {
        guard editor.string == text, let found = Range(editor.selectedRange(), in: text) else { return nil }
        let lower = text.distance(from: text.startIndex, to: found.lowerBound)
        return lower..<(lower + text.distance(from: found.lowerBound, to: found.upperBound))
    }

    /// Puts the cursor at `range` once the field editor holds `text`, on the next turn of
    /// the main queue, after SwiftUI has given the field its new words.
    static func place(_ range: Range<Int>, in text: String) {
        DispatchQueue.main.async {
            guard let editor = editor(for: text) else { return }
            let lower = text.index(text.startIndex, offsetBy: min(range.lowerBound, text.count))
            let upper = text.index(lower, offsetBy: min(range.count, text.distance(from: lower, to: text.endIndex)))
            editor.setSelectedRange(NSRange(lower..<upper, in: text))
        }
    }
}
