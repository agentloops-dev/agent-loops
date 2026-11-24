import AgentsKitCore
import SwiftUI

/// The one machine picker on the Remote (#5622; the Mac's is #5621, the web page's #5287, epic #5281): a
/// search field, then the pinned and recently used machines, then the rest grouped by label, each with a
/// state dot and a pin. The rules are the Mac's, `MachinePickerChoice`; it asks `machines/list` with the
/// typed words (`RemoteModel.machinePickerPage`). Pinned and recent are this device's, in
/// `WindowDefaults.store`.
///
/// `RemoteMachinePickerRow` is a form row that opens it; `RemoteMachinePickerAsk` opens it from a menu item.
struct RemoteMachinePickerRow: View {
    /// The row's label (On, Machine), and the sheet's title.
    let label: String
    /// The machine chosen, or nil for none (or `any`).
    let value: MachineRef?
    /// The row's words for `value`; nil says its name.
    var shown: String?
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    var any: RemoteMachinePickerList.AnyRow?
    var disabled = false
    let onPick: (MachineRef?) -> Void

    @Environment(RemoteModel.self) private var model
    @State private var open = false

    private var words: String {
        shown ?? value.map { model.machineName($0) } ?? any?.title ?? "Choose a machine"
    }

    var body: some View {
        Button { open = true } label: {
            LabeledContent(label) {
                HStack(spacing: 6) {
                    if let row = value.flatMap({ model.machineRow($0) }) { MachinePickerDot(row, size: 8) }
                    Text(words).foregroundStyle(Paper.secondary).lineLimit(1)
                    Image(icon: "chevron.right").imageScale(.small).foregroundStyle(Paper.secondary)
                        .accessibilityHidden(true)
                }
            }
            .appText(.reading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .accessibilityLabel("\(label), \(words)")
        .sheet(isPresented: $open) {
            RemoteMachinePickerSheet(title: label, value: value, keep: keep, any: any) { picked in
                open = false
                onPick(picked)
            }
        }
    }
}

/// What a menu item asks the picker for: its title, the machines it offers and what a pick does.
/// A pick runs once the sheet is down, so it can open a sheet of its own (Add Folder on ‹machine›…).
struct RemoteMachinePickerAsk: Identifiable {
    let id = UUID()
    let title: String
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    let onPick: (MachineRef) -> Void
}

/// The picker in a sheet of its own, with its title and Cancel.
struct RemoteMachinePickerSheet: View {
    let title: String
    var value: MachineRef?
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    var any: RemoteMachinePickerList.AnyRow?
    let onPick: (MachineRef?) -> Void

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            RemoteMachinePickerList(value: value, keep: keep, any: any, onPick: onPick)
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel", role: .cancel) { dismiss() }
                    }
                }
        }
        .presentationDetents([.medium, .large])
        .paperSheet()
    }
}

private struct RemoteMachinePickerAskModifier: ViewModifier {
    @Binding var ask: RemoteMachinePickerAsk?
    @State private var picked: (RemoteMachinePickerAsk, MachineRef)?

    func body(content: Content) -> some View {
        content.sheet(item: $ask, onDismiss: {
            guard let (each, machine) = picked else { return }
            picked = nil
            each.onPick(machine)
        }) { each in
            RemoteMachinePickerSheet(title: each.title, keep: each.keep) { machine in
                if let machine { picked = (each, machine) }
                ask = nil
            }
        }
    }
}

extension View {
    /// The picker sheet for a menu item's ask.
    func machinePickerSheet(_ ask: Binding<RemoteMachinePickerAsk?>) -> some View {
        modifier(RemoteMachinePickerAskModifier(ask: ask))
    }
}

/// The list: the search field (the system's, at the top), Auto when offered, then the sections.
struct RemoteMachinePickerList: View {
    struct AnyRow {
        var title: String
        var disabled = false
        /// Why it is off, under it.
        var help: String?
    }

    let value: MachineRef?
    var keep: (DaemonAPI.ControlMachine) -> Bool = { _ in true }
    var any: AnyRow?
    let onPick: (MachineRef?) -> Void

    @Environment(RemoteModel.self) private var model
    @State private var search = ""
    @State private var page: [DaemonAPI.ControlMachine] = []
    @State private var total = 0
    @State private var problem: String?
    @State private var loaded = false
    @State private var pinned = MachinePickerChoice.pinned(WindowDefaults.store)
    @State private var recent = MachinePickerChoice.recent(WindowDefaults.store)
    /// The latest read wins.
    @State private var serial = 0

    private var shown: [MachinePickerChoice.Section] {
        MachinePickerChoice.sections(page, pinned: pinned, recent: recent, keep: keep)
    }

    var body: some View {
        List {
            if let any, search.isEmpty {
                Section { anyRow(any) }
            }
            ForEach(shown) { section in
                Section(section.title) {
                    ForEach(section.machines) { row(for: $0) }
                }
            }
            if let problem {
                Text(problem).foregroundStyle(StateTint.failure.style(or: .secondary))
            } else if loaded, shown.isEmpty {
                Text(MachinePickerChoice.noneWords).foregroundStyle(Paper.secondary)
            }
            if total > page.count {
                Text(MachinePickerChoice.moreWords(shown: page.count, total: total))
                    .appText(.fine).foregroundStyle(Paper.secondary)
            }
        }
        .appText(.reading)
        .searchable(text: $search, placement: .navigationBarDrawer(displayMode: .always),
                    prompt: MachinePickerChoice.searchWords)
        .onSubmit(of: .search) { if let first = shown.first?.machines.first { pick(first.id) } }
        .machinePickerReads(search, machines: machinesKey) { await read($0) }
    }

    private func anyRow(_ any: AnyRow) -> some View {
        Button { pick(nil) } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(any.title).lineLimit(1)
                    if let help = any.help { Text(help).appText(.fine).foregroundStyle(Paper.secondary) }
                }
                Spacer()
                if value == nil { Image(icon: "checkmark").foregroundStyle(Paper.secondary) }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(any.disabled)
        .accessibilityAddTraits(value == nil ? .isSelected : [])
    }

    private func row(for machine: DaemonAPI.ControlMachine) -> some View {
        let isPinned = pinned.contains(machine.id.rawValue)
        let pinWords = isPinned ? MachinePickerChoice.unpinWords(machine.name) : MachinePickerChoice.pinWords(machine.name)
        return HStack(spacing: 10) {
            Button { pick(machine.id) } label: {
                HStack(spacing: 10) {
                    MachinePickerDot(machine, size: 8)
                    Text(machine.name).lineLimit(1)
                    Spacer()
                    Text(MachineListChoice.stateWords(machine)).appText(.fine).foregroundStyle(Paper.secondary).lineLimit(1)
                    if machine.id == value { Image(icon: "checkmark").foregroundStyle(Paper.secondary) }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(machine.id == value ? .isSelected : [])
            Button {
                MachinePickerChoice.togglePin(WindowDefaults.store, machine.id, pinned: &pinned, problem: &problem)
            } label: {
                MachinePickerPin(isPinned: isPinned)
                    .frame(minWidth: 32, minHeight: 32)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(pinWords)
        }
    }

    private func pick(_ id: MachineRef?) {
        if let id { recent = MachinePickerChoice.remember(WindowDefaults.store, id.rawValue) }
        onPick(id)
    }

    /// Each machine and its state, so a join, a leave or a change of state reads the page again.
    private var machinesKey: String {
        model.controlHosts.map { "\($0.id.rawValue):\($0.state)" }.sorted().joined(separator: ",")
    }

    private func read(_ asked: String) async {
        serial += 1
        let mine = serial
        do {
            let answer = try await model.machinePickerPage(asked)
            guard mine == serial else { return }
            page = answer.machines
            total = answer.total
            problem = nil
        } catch {
            if mine == serial { problem = "The control plane can’t be reached." }
        }
        if mine == serial { loaded = true }
    }
}
