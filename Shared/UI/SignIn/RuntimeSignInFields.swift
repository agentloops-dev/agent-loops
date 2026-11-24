#if os(macOS)
import AppKit
#else
import UIKit
#endif
import SwiftUI

/// The link of a runtime's sign-in (#5866): a click or a tap opens it in the browser, and the copy
/// icon copies it. The web page's RuntimeSignInDialog draws the same.
struct SignInLinkRow: View {
    let url: URL

    @State private var copied = false
    /// Counts the copies, so a second copy starts the 2 s again.
    @State private var copies = 0

    var body: some View {
        HStack(spacing: 8) {
            Link(destination: url) {
                Text(url.absoluteString)
                    .underline()
                    .foregroundStyle(.tint)
                    .lineLimit(1).truncationMode(.middle)
            }
            .help(url.absoluteString)
            Spacer(minLength: 0)
            Button {
                Clipboard.copy(url.absoluteString)
                copied = true
                copies += 1
            } label: {
                Image(icon: copied ? "checkmark" : "doc.on.doc")
            }
            .help(copied ? RuntimeSignInWords.copied : RuntimeSignInWords.copyLink)
            .accessibilityLabel(copied ? RuntimeSignInWords.copied : RuntimeSignInWords.copyLink)
        }
        // In a form's row on the phone, a tap on one must not press the other.
        .buttonStyle(.borderless)
        .task(id: copies) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(2))
            if !Task.isCancelled { copied = false }
        }
    }
}

/// The code the sign-in page gives (#5866): it cannot be typed, only pasted with the paste icon.
struct PastedCodeField: View {
    @Binding var code: String

    var body: some View {
        HStack(spacing: 8) {
            field
            paste
        }
    }

    private var field: some View {
        Text(code.isEmpty ? RuntimeSignInWords.codeLabel : code)
            .foregroundStyle(code.isEmpty ? AnyShapeStyle(.tertiary) : AnyShapeStyle(.primary))
            .lineLimit(1).truncationMode(.middle)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityLabel(RuntimeSignInWords.codeLabel)
            .accessibilityValue(code)
            #if os(macOS)
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .paperWell(in: RoundedRectangle(cornerRadius: Paper.Radius.control))
            .overlay(RoundedRectangle(cornerRadius: Paper.Radius.control).strokeBorder(Paper.rule, lineWidth: 1))
            #endif
    }

    @ViewBuilder private var paste: some View {
        #if os(macOS)
        Button {
            take(NSPasteboard.general.string(forType: .string))
        } label: {
            Image(icon: "doc.on.clipboard")
        }
        .buttonStyle(.borderless)
        .help(RuntimeSignInWords.pasteCodeLabel)
        .accessibilityLabel(RuntimeSignInWords.pasteCodeLabel)
        #else
        // The system's own button, so iOS does not ask "Allow Paste" each time.
        PasteButton(payloadType: String.self) { strings in
            Task { @MainActor in take(strings.first) }
        }
        .labelStyle(.iconOnly)
        .buttonBorderShape(.capsule)
        .accessibilityLabel(RuntimeSignInWords.pasteCodeLabel)
        #endif
    }

    private func take(_ text: String?) {
        guard let text = text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else { return }
        code = text
    }
}
