import SwiftUI

/// The one Line (#5036; `specs/session-cards/spec.md`, family 2): the record of what happened,
/// with no box and no glyph (#5675): a bold head, a quiet detail and at most one action link at the end.
/// When it has more to show, a click on the words opens it in place, under the words, and a
/// small chevron says so. Red only on a failure's words (#5016). Tool lines, thoughts,
/// the plan line, notes, notified, a report, the answered Ask's "You chose", and the sandbox and
/// policy records are all drawn with it. The window and the Remote both draw it; the web page's
/// is `Web/src/views/chat/RecordLine.tsx`.
struct RecordLine<More: View>: View {
    enum Tone { case plain, failure }

    /// Plain words before the head: "You chose".
    var lead: String?
    /// In bold. Nil for a line that is all detail: a tool line, a thought.
    var head: String?
    /// In the quiet colour, after the head.
    var detail: String?
    var tone: Tone = .plain
    /// One line, cut at the end: a tool line, a thought. Anything else wraps.
    var oneLine = false
    /// The one action link at the end of the line: See Policy, Try Again, Steps.
    var action: StripButton?
    /// What a click on the words does instead of opening: a folded run unfolds.
    var onClick: (() -> Void)?
    var clickHelp: String?
    /// What the click says it does, closed and open: "Show the whole thought", "Hide the thought".
    var openHelp: (show: String, hide: String) = ("Show more", "Show less")
    /// Gone to from elsewhere, so opened (#1315). Still closed by a click.
    var isFocused = false
    let more: More
    @State private var isExpanded = false
    @State private var isHovering = false

    init(lead: String? = nil, head: String? = nil, detail: String? = nil, tone: Tone = .plain,
         oneLine: Bool = false, action: StripButton? = nil, onClick: (() -> Void)? = nil, clickHelp: String? = nil,
         openHelp: (show: String, hide: String) = ("Show more", "Show less"), isFocused: Bool = false,
         @ViewBuilder more: () -> More) {
        self.lead = lead
        self.head = head
        self.detail = detail
        self.tone = tone
        self.oneLine = oneLine
        self.action = action
        self.onClick = onClick
        self.clickHelp = clickHelp
        self.openHelp = openHelp
        self.isFocused = isFocused
        self.more = more()
    }

    private var opens: Bool { More.self != EmptyView.self }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                summary
                Spacer(minLength: 0)
                if let action {
                    Button(action.title, action: action.action)
                        .linkStyle()
                        .appText(.reading)
                        .help(action.help ?? "")
                        .disabled(action.disabled)
                        .fixedSize()
                }
            }
            if isExpanded, opens {
                more
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .onChange(of: isFocused, initial: true) { _, now in if now, opens { isExpanded = true } }
    }

    @ViewBuilder
    private var summary: some View {
        if let onClick {
            Button(action: onClick) { words }
                .buttonStyle(.plain)
                .help(clickHelp ?? "")
                .accessibilityHint(clickHelp ?? "")
        } else if opens {
            Button {
                withAnimation(ResponseMotion.response) { isExpanded.toggle() }
            } label: {
                words
            }
            .buttonStyle(.plain)
            .help(isExpanded ? openHelp.hide : openHelp.show)
            .accessibilityHint(isExpanded ? openHelp.hide : openHelp.show)
        } else {
            words
        }
    }

    /// The words, and the chevron when the line opens: brighter under the pointer, as a turn's step is (069).
    private var words: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            text
                .appText(.reading)
                .lineLimit(oneLine ? 1 : nil)
                .truncationMode(.tail)
                .fixedSize(horizontal: false, vertical: !oneLine)
                .textSelection(.enabled)
            if opens || onClick != nil {
                Image(icon: "chevron.right")
                    .appText(.fine)
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(isExpanded ? 90 : 0))
                    .accessibilityHidden(true)
            }
        }
        .contentShape(.rect)
        .onHover { isHovering = (opens || onClick != nil) && $0 }
    }

    /// Lead, bold head, " · ", quiet detail, as one text so it wraps as one sentence. A failure
    /// is red on its main words: the head, or the detail of a line with no head.
    private var text: Text {
        let failure = tone == .failure
        let loud: AnyShapeStyle = failure ? StateTint.failure.style(or: .primary) : AnyShapeStyle(.primary)
        let quiet: AnyShapeStyle = failure && head == nil && lead == nil ? loud
            : isHovering ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary)
        var parts: [Text] = []
        if let lead { parts.append(Text(verbatim: lead).foregroundStyle(head == nil ? quiet : loud)) }
        if let head { parts.append(Text(verbatim: head).bold().foregroundStyle(loud)) }
        if let detail, !detail.isEmpty {
            let said = Text(verbatim: detail).foregroundStyle(quiet)
            parts.append(parts.isEmpty ? said : Text("\(Text(verbatim: "\u{00B7}").foregroundStyle(quiet)) \(said)"))
        }
        guard var out = parts.first else { return Text(verbatim: "") }
        for part in parts.dropFirst() { out = Text("\(out) \(part)") }
        return out
    }
}

extension RecordLine where More == EmptyView {
    init(lead: String? = nil, head: String? = nil, detail: String? = nil, tone: Tone = .plain,
         oneLine: Bool = false, action: StripButton? = nil, onClick: (() -> Void)? = nil, clickHelp: String? = nil) {
        self.init(lead: lead, head: head, detail: detail, tone: tone, oneLine: oneLine, action: action,
                  onClick: onClick, clickHelp: clickHelp) { EmptyView() }
    }
}
