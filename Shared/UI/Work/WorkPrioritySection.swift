import AgentsKitCore
import SwiftUI

/// The side panel's Priority section (#6092, epic #6086): an issue's priority as a menu (High,
/// Medium, None, Low) and its On hold as a toggle, each where the forge sets it (the item's
/// `actions` have `priority` or `hold`, #6091) and a machine of the person's writes; else the chip
/// and, when it is on hold, the words. A choice writes at once (`forge/priority`, `forge/hold`),
/// with a spinner while it runs; a failed write puts the old value back and says why under the
/// section. The page reads the item again after a write (`wrote`), so the chip follows.
struct WorkPrioritySection: View {
    let model: any WorkModel
    let host: MachineRef
    let detail: DaemonAPI.ForgeItemDetail
    let writes: Bool
    /// With Work's cache on (#6354): a choice goes through the cache, shown at once everywhere, with no spinner; nil to
    /// wait for the forge.
    var save: ((WorkChange) -> Void)?
    /// Reads the item again; the section shows the value a write set until it is done.
    let wrote: () async -> Void

    /// The value a write in flight sets, shown until the read after it; nil when none runs.
    @State private var settingPriority: String??
    @State private var settingHold: Bool?
    @State private var problem: String?

    private var item: DaemonAPI.ForgeItem { detail.item }
    private var sets: (priority: Bool, hold: Bool) { WorkList.setsPriority(detail, writes: writes) }
    private var priority: String? { settingPriority ?? item.priority?.lowercased() }
    private var onHold: Bool { settingHold ?? (item.onHold == true) }
    private var busy: Bool { settingPriority != nil || settingHold != nil }

    var body: some View {
        WorkSideSection(title: "Priority") {
            if sets.priority {
                menu
            } else if let chip = WorkPriorityChip(priority: priority) {
                chip
            } else {
                WorkSideNone(words: "None")
            }
            if sets.hold {
                hold
            } else if onHold {
                Text("On hold").help("Not picked up next")
            }
            if let problem {
                Text(problem).appText(.fine).tinted(.failure)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private var menu: some View {
        Menu {
            ForEach(WorkList.priorityChoices, id: \.self) { choice in
                Button {
                    set(priority: choice)
                } label: {
                    if choice == priority {
                        Label(Self.words(choice), icon: "checkmark")
                    } else {
                        Text(Self.words(choice))
                    }
                }
            }
        } label: {
            HStack(spacing: 6) {
                if let chip = WorkPriorityChip(priority: priority) { chip } else { Text("None").foregroundStyle(.secondary) }
                Spacer(minLength: 0)
                if settingPriority != nil {
                    ProgressView().controlSize(.mini)
                } else {
                    Image(icon: "chevron.up.chevron.down").appText(.fine).foregroundStyle(.secondary)
                }
            }
            .padding(.horizontal, 8)
            .frame(minHeight: 26)
            .background(RoundedRectangle(cornerRadius: 6).fill(Paper.well))
            .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Paper.rule))
            .contentShape(Rectangle())
        }
        .menuStyle(.button)
        .buttonStyle(.plain)
        .menuIndicator(.hidden)
        .disabled(busy)
        .help("Set the priority")
        .accessibilityLabel("Priority")
        .accessibilityValue(Self.words(priority))
    }

    private var hold: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Toggle("On hold", isOn: Binding(get: { onHold }, set: { set(hold: $0) }))
                    #if os(macOS)
                    .toggleStyle(.checkbox)
                    #endif
                    .disabled(busy)
                if settingHold != nil { ProgressView().controlSize(.mini) }
            }
            if onHold {
                Text("Not picked up next").appText(.fine).foregroundStyle(.secondary)
                    #if os(macOS)
                    .padding(.leading, 20)
                    #endif
            }
        }
        .help("An item on hold is not picked up next: it waits for a person")
    }

    /// *High*, *Medium*, *Low*, or *None*.
    static func words(_ priority: String?) -> String { WorkPlan.priorityWords(priority) ?? "None" }

    private func set(priority choice: String?) {
        guard choice != priority, !busy else { return }
        if let save { return save(.priority(choice)) }
        settingPriority = .some(choice)
        write(DaemonAPI.Method.forgePriority, DaemonAPI.ForgePriorityRequest(node: item.node, priority: choice))
    }

    private func set(hold on: Bool) {
        guard on != onHold, !busy else { return }
        if let save { return save(.hold(on)) }
        settingHold = on
        write(DaemonAPI.Method.forgeHold, DaemonAPI.ForgeHoldRequest(node: item.node, on: on))
    }

    private func write(_ method: String, _ params: some Encodable & Sendable) {
        problem = nil
        Task {
            do {
                _ = try await model.forge(method, params, returning: DaemonAPI.ForgeDone.self, on: host)
                await wrote()
                // The read after the write has the forge's own value now.
                settingPriority = nil
                settingHold = nil
            } catch {
                // The old value back, and why under the section.
                settingPriority = nil
                settingHold = nil
                problem = WorkList.failureWords(error)
            }
        }
    }
}

/// A Work ▸ Issues row's right-click menu (a long press on iPhone and iPad; epic #6086): a Priority
/// submenu (High, Medium, None, Low, a check on the row's own) and an On hold toggle, each where the
/// row's forge sets it and its machine writes. A choice writes at once, as the side panel's do (#6092).
struct WorkRowPriorityMenu: View {
    let row: WorkList.Row
    let sets: (priority: Bool, hold: Bool)
    let setPriority: (String?) -> Void
    let setHold: (Bool) -> Void

    var body: some View {
        if sets.priority {
            Menu("Priority") {
                ForEach(WorkList.priorityChoices, id: \.self) { choice in
                    Button {
                        setPriority(choice)
                    } label: {
                        if choice == row.priority?.lowercased() {
                            Label(WorkPrioritySection.words(choice), icon: "checkmark")
                        } else {
                            Text(WorkPrioritySection.words(choice))
                        }
                    }
                }
            }
        }
        if sets.hold {
            Toggle("On hold", isOn: Binding(get: { row.onHold }, set: setHold))
        }
    }
}
