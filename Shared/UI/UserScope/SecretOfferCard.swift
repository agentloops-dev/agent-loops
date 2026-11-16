import AgentsKitCore
import SwiftUI

/// A secret an agent saved on one of the person's machines (#5308), at the top of the sidebar
/// beside the machines that ask to join. A name the person has already waits for Keep Yours or
/// Use the Agent's; any other is taken by the Mac window at once, so it shows here only until
/// then. The Remote does not seal (`choose` is nil): its card says where to choose. The web
/// page's is `Web/src/views/SecretOffers.tsx`.
struct SecretOfferCard: View {
    let offer: DaemonAPI.SecretOffer
    let isAnswering: Bool
    let failure: String?
    /// Keep Yours (false) or Use the Agent's (true); nil on a client that does not seal.
    let choose: ((_ useTheAgents: Bool) -> Void)?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(icon: "key")
                // Decorative: the card says what it is in words.
                .glyph(size: 13)
                .foregroundStyle(StateTint.attention.style(or: .primary))
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                Text(offer.name).monospaced().fontWeight(.semibold)
                Text(SecretOfferWords.line(offer))
                    .fixedSize(horizontal: false, vertical: true)
                if let failure {
                    Text(failure).appText(.fine).tinted(.warning)
                }
                if offer.waitsForYou {
                    if let choose {
                        HStack(spacing: 8) {
                            Button(SecretOfferWords.useTheAgents) { choose(true) }
                                .buttonStyle(.paperProminent)
                                .disabled(offer.mine == nil)
                            Button(SecretOfferWords.keepYours) { choose(false) }
                                .buttonStyle(.bordered)
                            if isAnswering { ProgressView().controlSize(.small) }
                        }
                        .cardButtons()
                        .disabled(isAnswering)
                        .padding(.top, 2)
                    } else {
                        Text(SecretOfferWords.chooseElsewhere)
                            .appText(.fine)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            // The sidebar's rows are one line; a card's words wrap, as the join cards' do.
            .lineLimit(nil)
            .frame(maxWidth: .infinity, alignment: .leading)
            Spacer(minLength: 0)
        }
        .appText(.supporting)
        .padding(12)
        .background(Paper.raised, in: RoundedRectangle(cornerRadius: Paper.Radius.card))
        .overlay { RoundedRectangle(cornerRadius: Paper.Radius.card).strokeBorder(Paper.rule) }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(offer.name): \(SecretOfferWords.line(offer))")
    }
}
