import AgentsKitCore
import SwiftUI

/// The glyph at the start of a Work row (#4391, parts.md section 1): an issue's or a pull request's
/// state in the forge's colour, with words for a screen reader, so the colour is never the only
/// sign. A release, which has no state of its own, shows its kind's glyph in grey.
struct WorkStateIcon: View {
    let kind: String
    let state: String
    var draft = false
    /// The forge's `not_planned`, where it gives one (#4365): a ring with a slash.
    var stateReason: String?
    /// White, on a fill of the state's colour (the item page's pill, #4400).
    var onFill = false
    /// White on a selected row in a key window, as the row's text.
    @Environment(\.backgroundProminence) private var prominence

    var body: some View {
        if let glyph = WorkLook.glyph(kind: kind, state: state, draft: draft, stateReason: stateReason) {
            symbol(glyph)
                .foregroundStyle(onFill || prominence == .increased ? Color.white : glyph.tone.color)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(glyph.accessibilityLabel)
        } else {
            Image(icon: WorkList.Kind(rawValue: kind)?.sidebarIcon ?? "circle")
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
        }
    }

    @ViewBuilder
    private func symbol(_ glyph: WorkLook.Glyph) -> some View {
        switch glyph {
        case .issueOpen: Image(icon: "smallcircle.filled.circle")
        case .issueDone: Image(icon: "checkmark.circle")
        case .issueNotPlanned: Image(icon: "slash.circle")
        case .prOpen, .prDraft: Image(icon: "arrow.triangle.pull")
        case .prMerged: Image(icon: "arrow.triangle.merge")
        case .prClosed:
            // The branch glyph with a cross: SF Symbols has no one glyph for it.
            Image(icon: "arrow.triangle.pull")
                .overlay(alignment: .bottomTrailing) {
                    Image(icon: "xmark").imageScale(.small).fontWeight(.bold)
                        .padding(1)
                        .background(Circle().fill(Paper.raised))
                        .offset(x: 3, y: 2)
                }
        }
    }
}

extension WorkLook.Tone {
    var color: Color {
        switch self {
        case .green: Paper.forgeOpen
        case .purple: Paper.forgeDone
        case .grey: Paper.forgeDraft
        case .red: Paper.forgeClosed
        }
    }
}

/// A person's avatar (#4391, parts.md section 6): the forge's picture (#4363), read through the
/// machine that read the forge (`workPictures`). With no picture (a forge that gives none, or
/// one that did not load): a circle with the login's first letter on a colour picked from the
/// login, so one person keeps one colour.
struct WorkAvatar: View {
    let login: String
    var url: String?
    var side: CGFloat = 20
    @Environment(\.workPictures) private var pictures
    @State private var picture: PlatformImage?

    var body: some View {
        Group {
            if let picture {
                Image(platformImage: picture).resizable().scaledToFill()
            } else {
                Circle()
                    .fill(Color(hue: WorkLook.avatarHue(login), saturation: 0.45, brightness: 0.78))
                    .overlay {
                        Text(WorkLook.initial(login))
                            // Sized to the circle, not to the text style: a marked decorative glyph.
                            .font(.system(size: side * 0.48, weight: .semibold))
                            .foregroundStyle(.white)
                    }
            }
        }
        .frame(width: side, height: side)
        .clipShape(Circle())
        .accessibilityHidden(true)
        .task(id: WorkPictureAsk(url: url, pictures: pictures)) {
            guard let url, let pictures else { picture = nil; return }
            picture = await WorkPictureCache.shared.picture(url, pictures)
        }
    }
}

/// Who reads a forge's pictures for the views under it (`forge/image` on the machine that read
/// the item, #4363): the list sets it per row, an item's page for the page.
struct WorkPictures: Equatable {
    let model: any WorkModel
    let host: MachineRef

    static func == (a: Self, b: Self) -> Bool { a.model === b.model && a.host == b.host }

    /// The picture, and whether it may be kept: not one from a host the person chose (#7139).
    func read(_ url: String) async -> (image: PlatformImage?, keep: Bool) {
        // From this device's copy when it holds it (#4930): a picture does not change at its URL.
        let (answer, keep) = await model.readForgeImage(url, on: host)
        guard case .image(let data) = answer else { return (nil, true) }
        return (PlatformImage(data: data), keep)
    }
}

extension EnvironmentValues {
    @Entry var workPictures: WorkPictures?
}

private struct WorkPictureAsk: Equatable {
    var url: String?
    var pictures: WorkPictures?
}

/// The pictures read so far, by URL, so a person's avatar is read once for every row and page,
/// and a picture that did not load is not asked for again. At most `most` are kept.
@MainActor
final class WorkPictureCache {
    static let shared = WorkPictureCache()
    static let most = 500

    private var got: [String: PlatformImage] = [:]
    private var failed: Set<String> = []
    private var reading: [String: Task<(image: PlatformImage?, keep: Bool), Never>] = [:]

    func picture(_ url: String, _ pictures: WorkPictures) async -> PlatformImage? {
        if let image = got[url] { return image }
        if failed.contains(url) { return nil }
        if let task = reading[url] { return await task.value.image }
        let task = Task { await pictures.read(url) }
        reading[url] = task
        let (image, keep) = await task.value
        reading[url] = nil
        guard keep else { return image }
        if got.count + failed.count >= Self.most {
            got.removeAll()
            failed.removeAll()
        }
        if let image { got[url] = image } else { failed.insert(url) }
        return image
    }
}

/// Up to three avatars, overlapped, then "+2" (the row's trailing edge).
struct WorkAvatarStack: View {
    let logins: [String]
    /// The pictures the forge gives, by login (the author's, #4363): the rest are initials.
    var avatars: [String: String] = [:]
    var limit = 3

    var body: some View {
        if !logins.isEmpty {
            HStack(spacing: -6) {
                ForEach(logins.prefix(limit), id: \.self) { login in
                    WorkAvatar(login: login, url: avatars[login]).overlay(Circle().strokeBorder(Paper.raised, lineWidth: 1.5))
                }
                if logins.count > limit {
                    Text("+\(logins.count - limit)").appText(.fine).foregroundStyle(.secondary).padding(.leading, 8)
                }
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Assigned to \(logins.joined(separator: ", "))")
        }
    }
}

/// A label in its forge's colour (#2956, #4391 parts.md section 3): the colour at a low opacity
/// as its fill, a stronger border, and text that keeps 4.5:1 to the fill (`WorkLook.chip`). With
/// no colour, the grey chip. A scoped label's scope ("feature:") is in the regular weight.
struct WorkLabelChip: View {
    let label: DaemonAPI.ForgeLabel
    /// Sets the list's Label filter (Mac); nil where a chip is not a button.
    var pick: ((String) -> Void)?
    @Environment(\.colorScheme) private var scheme
    /// On a selected row in a key window the chip goes white, as the row's text does (lists.md section 2).
    @Environment(\.backgroundProminence) private var prominence

    var body: some View {
        if let pick {
            Button { pick(label.name) } label: { chip }
                .buttonStyle(.plain)
                .help("Show only \(label.name)")
        } else {
            chip
        }
    }

    private var chip: some View {
        let white = prominence == .increased
        let colours = white ? nil : WorkLook.chip(label.color, dark: scheme == .dark)
        let text: Color = white ? .white : colours?.text.color ?? .secondary
        let fill: Color = white ? .white.opacity(0.2) : colours?.fill.color ?? Paper.well
        let border: Color = white ? .white.opacity(0.6) : colours?.border.color ?? Paper.rule
        let parts = WorkLook.scoped(label.name)
        var words = Text(parts.rest).fontWeight(.medium)
        if let scope = parts.scope { words = Text("\(Text(scope).fontWeight(.regular))\(words)") }
        return words
            .appText(.fine)
            .lineLimit(1)
            .padding(.horizontal, 6)
            .frame(minHeight: 18)
            .foregroundStyle(text)
            .background(Capsule().fill(fill))
            .overlay(Capsule().strokeBorder(border))
            // One element with one label: a label stacked on a Text's own crashes AppKit's accessibility.
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Label \(label.name)")
    }
}

extension WorkLook.RGB {
    var color: Color { Color(red: r, green: g, blue: b) }
}

/// "+2": the chips a wide row leaves out.
struct WorkMoreChips: View {
    let count: Int

    var body: some View {
        if count > 0 {
            Text("+\(count)").appText(.fine).foregroundStyle(.secondary)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(count) more labels")
        }
    }
}

/// An item's priority (#5690), from `ForgeItem.priority`: the label chip's look, with the state
/// tint in its text and line, never in its fill (design/tokens.json): High the failure's red,
/// Medium the warning's orange, Low the plain chip. None has no chip.
struct WorkPriorityChip: View {
    let words: String
    /// The tooltip and spoken label; "Priority high" when nil. A session's says where its
    /// priority comes from (#5849).
    var said: String?
    /// On a selected row in a key window the chip goes white, as the row's text does.
    @Environment(\.backgroundProminence) private var prominence

    init?(_ item: DaemonAPI.ForgeItem) { self.init(priority: item.priority) }

    init?(priority: String?) {
        guard let words = WorkPlan.priorityWords(priority) else { return nil }
        self.words = words
    }

    /// A session's chip (#5849): high and low only; medium and none show nothing.
    init?(_ agent: Agent) {
        guard let words = SessionPriorityWords.chip(agent) else { return nil }
        self.words = words
        said = SessionPriorityWords.help(agent)
    }

    private var tint: StateTint {
        switch words {
        case "High": .failure
        case "Medium": .warning
        default: .none
        }
    }

    private var line: Color {
        switch tint {
        case .failure: Paper.lineFailure
        case .warning: Paper.lineWarning
        default: Paper.rule
        }
    }

    var body: some View {
        let white = prominence == .increased
        Text(words).fontWeight(.medium)
            .appText(.fine)
            .lineLimit(1)
            .padding(.horizontal, 6)
            .frame(minHeight: 18)
            .foregroundStyle(white ? AnyShapeStyle(.white) : tint.style(or: .secondary))
            .background(Capsule().fill(white ? .white.opacity(0.2) : Paper.well))
            .overlay(Capsule().strokeBorder(white ? .white.opacity(0.6) : line))
            .fixedSize()
            // One element with one label: a label stacked on a Text's own crashes AppKit's accessibility.
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(said ?? "Priority \(words)")
            .help(said ?? "Priority \(words.lowercased())")
    }
}

/// An item that is on hold (#6090), from `ForgeItem.onHold`: the label chip's look with no state
/// tint (on hold is not a failure), a pause glyph before the words. Not on hold has no chip.
struct WorkOnHoldChip: View {
    /// On a selected row in a key window the chip goes white, as the row's text does.
    @Environment(\.backgroundProminence) private var prominence

    init?(_ item: DaemonAPI.ForgeItem) { self.init(onHold: WorkPlan.onHold(item)) }

    init?(onHold: Bool) {
        guard onHold else { return nil }
    }

    var body: some View {
        let white = prominence == .increased
        HStack(spacing: 3) {
            Image(icon: "pause.fill").imageScale(.small)
            Text(WorkPlan.onHoldWords).fontWeight(.medium)
        }
        .appText(.fine)
        .lineLimit(1)
        .padding(.horizontal, 6)
        .frame(minHeight: 18)
        .foregroundStyle(white ? AnyShapeStyle(.white) : AnyShapeStyle(.secondary))
        .background(Capsule().fill(white ? .white.opacity(0.2) : Paper.well))
        .overlay(Capsule().strokeBorder(white ? .white.opacity(0.6) : Paper.rule))
        .fixedSize()
        // One element with one label: a label stacked on a Text's own crashes AppKit's accessibility.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(WorkPlan.onHoldWords)
        .help(WorkPlan.onHoldHelp)
    }
}
