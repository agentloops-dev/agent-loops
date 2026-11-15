import SwiftUI

/// The one Ask card (#5031; `specs/session-cards/spec.md`, family 4): every question to the
/// person has the same fixed head (glyph, title, asker), one body, and the same fixed foot (the
/// ways to say no as links on the left, at most two buttons on the right, the primary last).
/// The yellow left edge is the one mark of "needs you" (#5016). The window and the Remote both
/// draw it; the web page's is `Ask.tsx`.
struct AskCard<Content: View, Decline: View, Buttons: View>: View {
    /// An SF Symbol's name, drawn as its Lucide icon (`Image(icon:)`, #4976).
    let icon: String
    let title: String
    /// Who asks, by title and runtime (#121), or a quiet fact such as +12 −3.
    var asker: String?
    /// At the foot, with the other buttons: a form card's Later on the Mac (#4234, #5670).
    var accessory: AnyView?
    /// False for a form that draws its own answers under its fields (a paged form, a secret).
    var hasFoot = true
    let content: Content
    let decline: Decline
    let buttons: Buttons

    init(icon: String, title: String, asker: String? = nil, accessory: AnyView? = nil, hasFoot: Bool = true,
         @ViewBuilder content: () -> Content, @ViewBuilder decline: () -> Decline, @ViewBuilder buttons: () -> Buttons) {
        self.icon = icon
        self.title = title
        self.asker = asker
        self.accessory = accessory
        self.hasFoot = hasFoot
        self.content = content()
        self.decline = decline()
        self.buttons = buttons()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            // The question has the card's whole width (#5670): the asker goes on a line of its
            // own under it, and Later goes to the card's foot.
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(icon: icon)
                    .tinted(.attention)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    // An agent's question may be markdown (**bold**, `code`).
                    Text((try? AttributedString(markdown: title)) ?? AttributedString(title))
                        .appText(.reading).fontWeight(.semibold)
                        .fixedSize(horizontal: false, vertical: true)
                    if let asker {
                        Text(asker)
                            .appText(.fine)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            content
            if hasFoot {
                foot
            } else if let accessory {
                // A card that draws its own answers: Later on a row of its own at the bottom.
                Divider().overlay(Paper.rule)
                accessory
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Paper.raised, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).strokeBorder(Paper.rule))
        .overlay(alignment: .leading) {
            UnevenRoundedRectangle(topLeadingRadius: 16, bottomLeadingRadius: 16)
                .fill(StateTint.attention.color ?? Paper.rule)
                .frame(width: 3)
        }
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(title)
    }

    @ViewBuilder private var foot: some View {
        Divider().overlay(Paper.rule)
        // A narrow column (the phone, a large text size) puts the buttons under the words.
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 10) {
                HStack(spacing: 4) { decline }
                    .appText(.fine)
                Spacer(minLength: 8)
                if let accessory { accessory }
                buttons
            }
            VStack(alignment: .trailing, spacing: 8) {
                HStack(spacing: 4) { decline }
                    .appText(.fine)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if let accessory { accessory }
                HStack(spacing: 10) { buttons }
            }
        }
    }
}

extension AskCard where Decline == EmptyView {
    init(icon: String, title: String, asker: String? = nil,
         @ViewBuilder content: () -> Content, @ViewBuilder buttons: () -> Buttons) {
        self.init(icon: icon, title: title, asker: asker, content: content, decline: { EmptyView() }, buttons: buttons)
    }
}

extension AskCard where Decline == EmptyView, Buttons == EmptyView {
    /// An Ask whose body draws its own answers (a form, a secret, a hand-off): no foot.
    init(icon: String, title: String, asker: String? = nil, accessory: AnyView? = nil,
         @ViewBuilder content: () -> Content) {
        self.init(icon: icon, title: title, asker: asker, accessory: accessory, hasFoot: false,
                  content: content, decline: { EmptyView() }, buttons: { EmptyView() })
    }
}

/// One of the Ask's ways not to answer yes: a link on the foot's left ("No thanks", "Dismiss").
struct AskDecline: View {
    /// Dismiss (#5295; was No, because…, #4039): the card goes with no answer (the wire's
    /// cancel), and the person replies in the prompt. The same word on every client.
    static let dismiss = "Dismiss"

    let title: String
    let action: () -> Void

    init(_ title: String, action: @escaping () -> Void) {
        self.title = title
        self.action = action
    }

    var body: some View {
        Button(title, action: action)
            .buttonStyle(.plain)
            .foregroundStyle(.tint)
    }
}

/// The one label over every Ask, in the yellow of needs you (#5016).
struct WaitingForYouLabel: View {
    static let words = "Waiting for you"

    var body: some View {
        Text(Self.words.uppercased())
            .appText(.fine).fontWeight(.semibold)
            .kerning(0.6)
            .tinted(.attention)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityAddTraits(.isHeader)
            .accessibilityLabel(Self.words)
    }
}
