import AgentsKitCore
import SwiftUI
import UniformTypeIdentifiers

/// What a drop where the pointer is would do, or why it is refused (#7539): the label the
/// sidebar shows while a row is dragged (wireframes 2b–3c).
struct ParentDropTip: Equatable {
    let words: String
    let refused: Bool
}

// MARK: The drag (wireframes 2b–2d, 3a–3c)

extension View {
    /// A session row that drags onto a lead, and a lead row that takes a drop (#7539).
    func sessionParentDrag(_ agent: Agent, tree: SidebarTree.Row?, folds: SidebarFolds?) -> some View {
        modifier(SessionParentDrag(agent: agent, tree: tree, folds: folds))
    }

    /// A tooltip only when there is something to say.
    @ViewBuilder
    func help(ifAny text: String?) -> some View {
        if let text { help(text) } else { self }
    }

    /// A folder's Sessions row: a drop there puts the session at the top. (A List's section
    /// heading takes no drop on the Mac, so the heading is not a target.)
    func sessionTopDrop(folders: Set<FolderKey>, name: String) -> some View {
        modifier(SessionTopDrop(folders: folders, name: name))
    }
}

/// The type a dragged row carries: its id as text. What the drag is comes from
/// `AppModel.parentDrag`, which the drag sets as it starts.
private let sessionRowType = UTType.utf8PlainText

private struct SessionParentDrag: ViewModifier {
    @Environment(AppModel.self) private var model
    let agent: Agent
    let tree: SidebarTree.Row?
    let folds: SidebarFolds?
    @State private var isTargeted = false

    func body(content: Content) -> some View {
        if model.sessionParentsOn {
            let drag = model.parentDrag
            let inDrag = drag?.subtree.contains(agent.id) == true
            content
                // While a row is dragged, only a lead can take it: the others dim, and the
                // dragged rows fade in place.
                .opacity(drag != nil && (inDrag || !SessionParents.isLead(agent)) ? 0.38 : 1)
                .overlay {
                    // Only while the drag lasts: a drop that lands nowhere calls no exit.
                    if isTargeted, drag != nil {
                        let refused = model.parentDropTip?.refused == true
                        RoundedRectangle(cornerRadius: 6)
                            .strokeBorder(refused ? Paper.lineFailure : Paper.accent, lineWidth: 2)
                            .allowsHitTesting(false)
                    }
                }
                .onDrag {
                    guard model.canSetParent(agent) else { return NSItemProvider() }
                    model.beginParentDrag(agent)
                    return NSItemProvider(object: agent.id.uuidString as NSString)
                } preview: {
                    // Drawn as the drag starts, before `parentDrag` is set: counted here.
                    ParentDragGhost(title: SessionParents.title(agent),
                                    carried: model.parentIndex(on: agent.host).descendants(of: agent.id)
                                        .count { $0.state != .archived })
                }
                .onDrop(of: [sessionRowType], delegate: LeadDrop(model: model, lead: agent, tree: tree, folds: folds,
                                                                 isTargeted: $isTargeted))
        } else {
            content
        }
    }
}

/// A lead's row taking a dragged session: the reasons the cache can see, before the drop.
private struct LeadDrop: DropDelegate {
    let model: AppModel
    let lead: Agent
    let tree: SidebarTree.Row?
    let folds: SidebarFolds?
    @Binding var isTargeted: Bool

    private var child: Agent? { model.parentDrag.flatMap { model.work.agent($0.id) } }

    /// Nil when the drop is no move at all (not a lead, or the lead it is under now).
    private var tip: ParentDropTip? {
        guard let child, SessionParents.isLead(lead) else { return nil }
        if let refusal = model.parentRefusal(moving: child, under: lead) {
            return ParentDropTip(words: refusal.words, refused: true)
        }
        guard child.parent?.id != lead.id else { return nil }
        let carried = model.parentDrag?.carried ?? 0
        let words = carried > 0
            ? SessionParents.dropSubtree(SessionParents.title(child), count: carried, under: SessionParents.title(lead))
            : SessionParents.dropUnder(SessionParents.title(lead))
        return ParentDropTip(words: words, refused: false)
    }

    func validateDrop(info: DropInfo) -> Bool { model.parentDrag != nil }

    func dropEntered(info: DropInfo) {
        let tip = tip
        isTargeted = tip != nil
        model.parentDropTip = tip
        // A hover of 1 s on a folded lead opens it.
        if tip?.refused == false, let tree, tree.children > 0, !tree.isUnfolded, let folds {
            let id = lead.id
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(1))
                if isTargeted, model.parentDrag != nil { folds.set(session: id, open: true) }
            }
        }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        DropProposal(operation: tip?.refused == false ? .move : .forbidden)
    }

    func dropExited(info: DropInfo) {
        isTargeted = false
        if model.parentDropTip == tip { model.parentDropTip = nil }
    }

    func performDrop(info: DropInfo) -> Bool {
        defer { end() }
        guard let child, tip?.refused == false else { return false }
        Task { await model.setParent(child.id, .under(lead.id)) }
        return true
    }

    private func end() {
        isTargeted = false
        model.parentDropTip = nil
        model.parentDrag = nil
    }
}

/// The folder's heading taking a dragged session of that folder: at the top (wireframe 2c).
private struct SessionTopDrop: ViewModifier {
    @Environment(AppModel.self) private var model
    let folders: Set<FolderKey>
    let name: String
    @State private var isTargeted = false

    func body(content: Content) -> some View {
        if model.sessionParentsOn {
            content
                .overlay(alignment: .bottom) {
                    if isTargeted, model.parentDrag != nil {
                        Capsule().fill(Paper.accent).frame(height: 2).padding(.horizontal, 8).allowsHitTesting(false)
                    }
                }
                .onDrop(of: [sessionRowType], delegate: TopDrop(model: model, folders: folders, name: name,
                                                                isTargeted: $isTargeted))
        } else {
            content
        }
    }
}

private struct TopDrop: DropDelegate {
    let model: AppModel
    let folders: Set<FolderKey>
    let name: String
    @Binding var isTargeted: Bool

    /// The dragged session, when it is of these folders and not at the top already.
    private var child: Agent? {
        guard let agent = model.parentDrag.flatMap({ model.work.agent($0.id) }),
              folders.contains(FolderKey(host: agent.host, folder: agent.folderPath)),
              SessionParents.effectiveParent(of: agent) != nil else { return nil }
        return agent
    }

    func validateDrop(info: DropInfo) -> Bool { model.parentDrag != nil }

    func dropEntered(info: DropInfo) {
        isTargeted = child != nil
        if child != nil { model.parentDropTip = ParentDropTip(words: SessionParents.dropTop(name), refused: false) }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? { DropProposal(operation: child != nil ? .move : .forbidden) }

    func dropExited(info: DropInfo) {
        isTargeted = false
        if child != nil { model.parentDropTip = nil }
    }

    func performDrop(info: DropInfo) -> Bool {
        defer {
            isTargeted = false
            model.parentDropTip = nil
            model.parentDrag = nil
        }
        guard let child else { return false }
        Task { await model.setParent(child.id, .top) }
        return true
    }
}

/// The dragged row under the pointer: its title, and "+2" for the sessions that go with it.
private struct ParentDragGhost: View {
    let title: String
    let carried: Int

    var body: some View {
        HStack(spacing: 6) {
            Text(title).lineLimit(1)
            if carried > 0 {
                Text("+\(carried)")
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 6)
                    .overlay(Capsule().strokeBorder(Paper.rule))
            }
        }
        .appText(.supporting)
        .padding(.horizontal, 10)
        .padding(.vertical, 5)
        .paperRaised(in: .rect(cornerRadius: 8))
    }
}

/// The label of the drop under the pointer, at the foot of the sidebar while a row is
/// dragged: what the drop does, or why it is refused (wireframes 2b–3c).
struct ParentDropTipView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if let tip = model.parentDropTip {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                if tip.refused {
                    Image(icon: "nosign")
                        .foregroundStyle(StateTint.failure.style(or: .primary))
                        .accessibilityHidden(true)
                }
                Text(tip.words)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .appText(.fine)
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .paperRaised(in: .rect(cornerRadius: 8))
            .overlay(alignment: .leading) {
                if tip.refused {
                    UnevenRoundedRectangle(topLeadingRadius: 8, bottomLeadingRadius: 8)
                        .fill(StateTint.failure.style(or: .primary)).frame(width: 3)
                }
            }
            .padding(8)
            .allowsHitTesting(false)
            .transition(.opacity)
        }
    }
}

// MARK: The list of leads (wireframes 1a and 4b)

/// The Lead chip's list in New Session: No lead, then the leads, with a search past 8.
struct LeadPickerList: View {
    let choices: [SessionParents.Choice]
    let chosen: UUID?
    let heading: String
    let choose: (UUID?) -> Void
    @State private var query = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if choices.count > SessionParents.searchPast {
                TextField(SessionParents.searchPrompt, text: $query)
                    .textFieldStyle(.roundedBorder)
                    .padding(.horizontal, 4)
                    .padding(.bottom, 6)
            }
            SelectChoice(title: SessionParents.noLead, description: SessionParents.noLeadDescription,
                         isChosen: chosen == nil) { choose(nil) }
            Divider().padding(.vertical, 4)
            SelectGroupHeading(title: heading)
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(LeadSearch.filter(choices, query)) { choice in
                        SelectChoice(title: choice.title, description: choice.description,
                                     isChosen: chosen == choice.id) { choose(choice.id) }
                            .disabled(choice.refusal != nil)
                    }
                }
            }
            .frame(maxHeight: 280)
            .fixedSize(horizontal: false, vertical: true)
        }
        .frame(width: 300)
    }
}

enum LeadSearch {
    static func filter(_ choices: [SessionParents.Choice], _ query: String) -> [SessionParents.Choice] {
        let query = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !query.isEmpty else { return choices }
        return choices.filter { $0.title.localizedCaseInsensitiveContains(query) || $0.description.localizedCaseInsensitiveContains(query) }
    }

    /// "Leads on this Mac", or on the machine's name.
    @MainActor
    static func heading(_ host: MachineRef, in model: AppModel) -> String {
        host == .mac ? SessionParents.leadsHeading : "Leads on \(model.hosts.label(host))"
    }
}

/// Move Under Lead… (wireframe 4b): every placement in one list. ↑ and ↓ go through it,
/// Return moves, Escape closes, and typing goes to the search field. A choice the Mac would
/// refuse is dimmed with the reason.
struct MoveUnderLeadSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let agent: Agent
    @State private var query = ""
    @State private var picked: SessionPlacement?
    @FocusState private var searchFocused: Bool

    private struct Option: Identifiable {
        let placement: SessionPlacement
        let title: String
        let description: String
        let refused: Bool
        var id: String { MoveUnderLeadSheet.rowID(placement) }
    }

    static func rowID(_ placement: SessionPlacement) -> String {
        switch placement {
        case .top: "top"
        case .starter: "starter"
        case .under(let lead): lead.uuidString
        }
    }

    private var current: SessionPlacement { SessionPlacement(of: agent) }
    private var folder: String { agent.folderPath.lastPathComponent }
    private var starter: Agent? { agent.startedByAgent.flatMap { model.work.agent($0) } }

    private var fixed: [Option] {
        var options = [Option(placement: .top, title: SessionParents.atTopOf(folder),
                              description: SessionParents.underNoSession, refused: false)]
        // Only with a starter in the list that it is not under now.
        if let starter, agent.parent != nil {
            options.append(Option(placement: .starter, title: SessionParents.underItsStarter,
                                  description: SessionParents.title(starter), refused: false))
        }
        return options
    }

    private var leads: [Option] {
        LeadSearch.filter(model.leadChoices(for: agent, on: agent.host, folder: agent.folderPath), query).map {
            Option(placement: .under($0.id), title: $0.title, description: $0.description, refused: $0.refusal != nil)
        }
    }

    private var choosable: [SessionPlacement] { (fixed + leads).filter { !$0.refused }.map(\.placement) }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            VStack(alignment: .leading, spacing: 2) {
                Text(SessionParents.sheetTitle(SessionParents.title(agent))).appText(.reading).fontWeight(.semibold)
                Text(SessionParents.sheetLine(lead: model.lead(of: agent).map(SessionParents.title),
                                              starter: starter.map(SessionParents.title)))
                    .appText(.fine)
                    .foregroundStyle(.secondary)
            }
            TextField(SessionParents.searchPrompt, text: $query)
                .textFieldStyle(.roundedBorder)
                .focused($searchFocused)
                .onKeyPress(.downArrow) { step(1); return .handled }
                .onKeyPress(.upArrow) { step(-1); return .handled }
            ScrollViewReader { scroller in
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(fixed) { row($0) }
                        Divider().padding(.vertical, 4)
                        SelectGroupHeading(title: LeadSearch.heading(agent.host, in: model))
                        ForEach(leads) { row($0) }
                    }
                }
                .frame(height: 260)
                .onChange(of: picked) { _, picked in
                    if let picked { scroller.scrollTo(Self.rowID(picked)) }
                }
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("Move") { move() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(picked == nil || picked == current)
            }
        }
        .padding(16)
        .frame(width: 420)
        .onAppear {
            picked = current
            searchFocused = true
        }
    }

    private func row(_ option: Option) -> some View {
        Button {
            picked = option.placement
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(icon: "checkmark")
                    .glyph(size: 10, weight: .semibold)
                    .foregroundStyle(Paper.accent)
                    .opacity(option.placement == current ? 1 : 0)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 1) {
                    Text(option.title)
                    Text(option.description).appText(.fine).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 5)
            .contentShape(Rectangle())
            .background(picked == option.placement ? Paper.wash : .clear, in: .rect(cornerRadius: 6))
        }
        .buttonStyle(.plain)
        .disabled(option.refused)
        .opacity(option.refused ? 0.5 : 1)
        .id(option.id)
        .accessibilityAddTraits(picked == option.placement ? .isSelected : [])
        .simultaneousGesture(TapGesture(count: 2).onEnded {
            guard !option.refused else { return }
            picked = option.placement
            move()
        })
    }

    private func step(_ by: Int) {
        let all = choosable
        guard !all.isEmpty else { return }
        let at = picked.flatMap { all.firstIndex(of: $0) } ?? (by > 0 ? -1 : all.count)
        picked = all[max(0, min(all.count - 1, at + by))]
    }

    private func move() {
        guard let picked, picked != current else { return }
        dismiss()
        Task { await model.setParent(agent.id, picked) }
    }
}
