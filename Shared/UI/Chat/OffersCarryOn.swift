import AgentsKitCore
import SwiftUI

/// Puts "Carry on" in an empty prompt bar when its chat stops on an error (`CarryOn`).
///
/// When the bar appears on such a chat, and when its chat stops so. On the next turn of
/// the main queue, so the draft that the bar puts back as it appears is in the field
/// first: what is typed is never written over. Shared by the window's bar and the
/// Remote's, so both give the same words for the same stop.
struct OffersCarryOn: ViewModifier {
    @Binding var text: String
    let agent: Agent?

    /// One value for each stop that offers the words; empty for every other state.
    private var stop: String {
        guard let agent, CarryOn.follows(agent) else { return "" }
        return CarryOn.stop(of: agent)
    }

    func body(content: Content) -> some View {
        content.onChange(of: stop, initial: true) {
            guard !stop.isEmpty else { return }
            DispatchQueue.main.async {
                if let words = CarryOnOffers.shared.words(for: agent, typed: text) { text = words }
            }
        }
    }
}
