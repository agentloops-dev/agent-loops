import AgentsKitCore
import SwiftUI

/// The one machine picker (#5621; the web page's is #5287, epic #5281): a search field, then the pinned and recently
/// used machines, then the rest grouped by label, each with a state dot and a pin. It asks `machines/list` with the typed
/// words (#5283), so a person with many machines never loads them all; a control plane before `machines/2` answers them
/// all, and the picker filters them itself. Pinned and recent are this app's, in `WindowDefaults.store`.
///
/// `MachinePicker` is a button that opens it in a popover; `MachinePickerSheet` opens it from a menu item (a context menu
/// or the menu bar), where no popover can open.
struct MachinePicker: View {
    /// The name for assistive tech, and what the button says when nothing is chosen.
    let label: String
    /// The machine chosen, or nil for none (or `any`).
    let value: MachineRef?
    /// The button's words for `value`; nil says its name, or `placeholder`.
    var title: String?
    var placeholder = "Choose a machine"
    /// The machines that may be picked; the rest are left out.
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    /// A first row that picks no machine, as Auto.
    var any: MachinePickerList.AnyRow?
    let onPick: (MachineRef?) -> Void

    @Environment(AppModel.self) private var model
    @State private var open = false

    var body: some View {
        Button { open.toggle() } label: {
            HStack(spacing: 6) {
                if let row = value.flatMap({ model.hosts.controlRows[$0] }) { MachinePickerDot(row) }
                Text(title ?? value.map { model.hosts.label($0) } ?? any?.title ?? placeholder).appText(.reading).lineLimit(1)
                Image(icon: "chevron.down").glyph(size: 8, weight: .semibold).foregroundStyle(.secondary).accessibilityHidden(true)
            }
        }
        .accessibilityLabel(label)
        .accessibilityValue(value.map { model.hosts.label($0) } ?? any?.title ?? placeholder)
        .popover(isPresented: $open, arrowEdge: .bottom) {
            MachinePickerList(label: label, value: value, keep: keep, any: any) { picked in
                open = false
                onPick(picked)
            }
            .frame(width: 300, height: 360)
        }
    }
}

/// The picker opened from a menu item, in a sheet: its title says what a pick does ("Send to a Machine").
struct MachinePickerSheet: View {
    let title: String
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    let onPick: (MachineRef) -> Void

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(title).appText(.reading).fontWeight(.semibold).padding([.horizontal, .top], 16).padding(.bottom, 4)
            MachinePickerList(label: title, value: nil, keep: keep) { picked in
                if let picked { onPick(picked) }
                dismiss()
            }
            HStack {
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .buttonStyle(.paper)
                    .keyboardShortcut(.cancelAction)
            }
            .padding(12)
        }
        .frame(width: 340, height: 440)
    }
}

/// What a menu item asks the picker sheet for: its title, the machines it offers and what a pick
/// does. A pick runs once the sheet is down, so it can open a sheet of its own (Send to ‹machine›…).
struct MachinePickerAsk: Identifiable {
    let id = UUID()
    let title: String
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    let onPick: (MachineRef) -> Void
}

extension MachinePickerAsk {
    /// A folder's and a worktree's one item for Send to ‹machine›… (#1123) and Bring Back from ‹machine›… (#1473).
    static let sendMenu = "Send to a Machine…"
    static let bringBackMenu = "Bring Back from a Machine…"

    /// The picker over `targets`, titled for sending or bringing back.
    static func send(_ targets: [MachineRef], bringBack: Bool = false, then pick: @escaping (MachineRef) -> Void) -> Self {
        let allowed = Set(targets)
        return MachinePickerAsk(title: bringBack ? "Bring Back from a Machine" : "Send to a Machine",
                                keep: { allowed.contains($0.id) }, onPick: pick)
    }
}

private struct MachinePickerSheetModifier: ViewModifier {
    @Binding var ask: MachinePickerAsk?
    @State private var picked: (MachinePickerAsk, MachineRef)?

    func body(content: Content) -> some View {
        content.sheet(item: $ask, onDismiss: {
            guard let (each, machine) = picked else { return }
            picked = nil
            each.onPick(machine)
        }) { each in
            MachinePickerSheet(title: each.title, keep: each.keep) { picked = (each, $0) }.paperSheet()
        }
    }
}

extension View {
    /// The picker sheet for a menu item's ask.
    func machinePickerSheet(_ ask: Binding<MachinePickerAsk?>) -> some View {
        modifier(MachinePickerSheetModifier(ask: ask))
    }

    /// The picker sheet the window's menus ask for (`WindowRequests.machinePicker`).
    func machinePickerSheet(_ requests: WindowRequests) -> some View {
        machinePickerSheet(Binding(get: { requests.machinePicker }, set: { requests.machinePicker = $0 }))
    }
}

/// The panel: the search field and the list.
struct MachinePickerList: View {
    struct AnyRow {
        var title: String
        var disabled = false
        /// Why it is off, under it.
        var help: String?
    }

    let label: String
    let value: MachineRef?
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    var any: AnyRow?
    let onPick: (MachineRef?) -> Void

    @Environment(AppModel.self) private var model
    @State private var search = ""
    @State private var page: [DaemonAPI.ControlMachine] = []
    @State private var total = 0
    @State private var problem: String?
    @State private var pinned = MachinePickerChoice.pinned(WindowDefaults.store)
    @State private var recent = MachinePickerChoice.recent(WindowDefaults.store)
    /// The latest read wins.
    @State private var serial = 0
    @FocusState private var searching: Bool

    private var shown: [MachinePickerChoice.Section] {
        MachinePickerChoice.sections(page, pinned: pinned, recent: recent, keep: keep)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            TextField(MachinePickerChoice.searchWords, text: $search)
                .textFieldStyle(.roundedBorder)
                .focused($searching)
                .accessibilityLabel(MachinePickerChoice.searchWords)
                .onSubmit { if let first = shown.first?.machines.first { pick(first.id) } }
                .padding(10)
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if let any, search.isEmpty { anyRow(any) }
                    ForEach(shown) { section in
                        Text(section.title).appText(.supporting).fontWeight(.semibold).foregroundStyle(.secondary)
                            .padding(.horizontal, 10).padding(.top, 8).padding(.bottom, 2)
                            .accessibilityAddTraits(.isHeader)
                        ForEach(section.machines) { row(for: $0) }
                    }
                    if let problem {
                        Text(problem).appText(.supporting).foregroundStyle(StateTint.failure.style(or: .secondary)).padding(10)
                    } else if shown.isEmpty {
                        Text(MachinePickerChoice.noneWords).appText(.supporting).foregroundStyle(.secondary).padding(10)
                    }
                    if total > page.count {
                        Text(MachinePickerChoice.moreWords(shown: page.count, total: total))
                            .appText(.fine).foregroundStyle(.secondary).padding(10)
                    }
                }
                .padding(.bottom, 6)
            }
            .accessibilityLabel(label)
        }
        .onAppear { searching = true }
        .machinePickerReads(search, machines: machinesKey) { await read($0) }
    }

    private func anyRow(_ any: AnyRow) -> some View {
        Button { pick(nil) } label: {
            VStack(alignment: .leading, spacing: 1) {
                HStack {
                    Text(any.title).lineLimit(1)
                    Spacer()
                    if value == nil { Image(icon: "checkmark").foregroundStyle(.secondary) }
                }
                if let help = any.help { Text(help).appText(.fine).foregroundStyle(.secondary) }
            }
            .padding(.horizontal, 10).padding(.vertical, 5)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(any.disabled)
        .accessibilityAddTraits(value == nil ? .isSelected : [])
    }

    private func row(for machine: DaemonAPI.ControlMachine) -> some View {
        let name = machine.id == .mac ? model.hosts.label(.mac) : machine.name
        let isPinned = pinned.contains(machine.id.rawValue)
        return HStack(spacing: 8) {
            Button { pick(machine.id) } label: {
                HStack(spacing: 8) {
                    MachinePickerDot(machine)
                    Text(name).lineLimit(1)
                    Spacer()
                    Text(MachineListChoice.stateWords(machine)).appText(.fine).foregroundStyle(.secondary).lineLimit(1)
                    if machine.id == value { Image(icon: "checkmark").foregroundStyle(.secondary) }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(machine.id == value ? .isSelected : [])
            Button {
                MachinePickerChoice.togglePin(WindowDefaults.store, machine.id, pinned: &pinned, problem: &problem)
            } label: {
                MachinePickerPin(isPinned: isPinned)
            }
            .buttonStyle(.plain)
            .help(isPinned ? MachinePickerChoice.unpinWords(name) : MachinePickerChoice.pinWords(name))
            .accessibilityLabel(isPinned ? MachinePickerChoice.unpinWords(name) : MachinePickerChoice.pinWords(name))
        }
        .padding(.horizontal, 10).padding(.vertical, 5)
    }

    private func pick(_ id: MachineRef?) {
        if let id { recent = MachinePickerChoice.remember(WindowDefaults.store, id.rawValue) }
        onPick(id)
    }

    /// Each machine and its state, so a join, a leave or a change of state reads the page again.
    private var machinesKey: String {
        model.hosts.controlRows.values.map { "\($0.id.rawValue):\($0.state)" }.sorted().joined(separator: ",")
    }

    private func read(_ asked: String) async {
        serial += 1
        let mine = serial
        guard let control = model.controlRequests else {
            // No control plane to ask: the machines this app already knows.
            let all = MachinePickerChoice.filtered(Array(model.hosts.controlRows.values), search: asked)
            page = Array(all.prefix(MachinePickerChoice.limit))
            total = all.count
            return
        }
        do {
            let answer = try await control.call(DaemonAPI.Method.hostsList, MachinePickerChoice.query(asked))
            guard mine == serial else { return }
            if answer.arrayValue != nil {
                // A control plane before machines/2: every machine, filtered and cut here.
                let all = MachinePickerChoice.filtered(try answer.decode([DaemonAPI.ControlMachine].self), search: asked)
                page = Array(all.prefix(MachinePickerChoice.limit))
                total = all.count
            } else {
                let list = try answer.decode(DaemonAPI.MachineList.self)
                page = list.machines
                total = list.total
            }
            problem = nil
        } catch let error as JSONRPCError {
            if mine == serial { problem = HostProblem.controlRefusal(error) }
        } catch {
            if mine == serial { problem = "The control plane can’t be reached." }
        }
    }
}
