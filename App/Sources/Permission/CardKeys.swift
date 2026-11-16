import SwiftUI

extension EnvironmentValues {
    /// Whether a question card takes its keys (⌘1…9 and Return). A chat shows one card at a time,
    /// so it does; the Dashboard (#5534) shows many, so only its top card does, and a key never
    /// answers a card the person is not looking at.
    @Entry var cardKeys = true
}

extension View {
    /// `keyboardShortcut`, while the card takes its keys (`cardKeys`).
    func cardKey(_ shortcut: KeyboardShortcut) -> some View {
        modifier(CardKey(shortcut: shortcut))
    }
}

private struct CardKey: ViewModifier {
    @Environment(\.cardKeys) private var on
    let shortcut: KeyboardShortcut

    func body(content: Content) -> some View {
        content.keyboardShortcut(on ? shortcut : nil)
    }
}
