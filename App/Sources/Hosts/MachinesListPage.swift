import AgentsKitCore
import SwiftUI

/// The Machines page (#5567; the web page's is #5285, epic #5281): for a person with many machines.
/// A search, filter chips with counts (state, provider, OS, version, label), a sort, and a table that
/// loads 50 at a time from `machines/list` (#5283). Select rows (⌘-click, ⇧-click or ⌘A), then
/// Start, Stop or Delete… the selected ones; a double-click or Open opens the machine's page.
/// "Last update" says when this list was read (#5139). A control plane before `machines/2`
/// answers every machine at once, and the page filters them itself. From the Machines header in
/// the sidebar.
struct MachinesListPage: View {
    @Environment(AppModel.self) private var model

    @State private var choice = MachineListChoice()
    @State private var rows: [DaemonAPI.ControlMachine] = []
    @State private var next: String?
    @State private var total = 0
    @State private var counts: DaemonAPI.MachineCounts?
    @State private var readAt: Date?
    @State private var problem: String?
    @State private var loading = false
    @State private var picked: Set<MachineRef> = []
    @State private var working = false
    /// The latest read wins: an answer to an older choice is let go.
    @State private var serial = 0
    /// Stop, asked first while sessions work on some of them: the machines and who works there.
    @State private var stopping: (hosts: [MachineRef], names: [String])?
    /// Delete…, asked once the machines said what work on them is not pushed.
    @State private var deleting: (hosts: [MachineRef], question: String)?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            tools
            let chips = choice.chips(counts)
            if !chips.isEmpty { chipRow(chips) }
            if !selected.isEmpty { bulkBar }
            if let problem {
                Text(problem).appText(.supporting).foregroundStyle(StateTint.failure.style(or: .secondary))
            }
            table
            footer
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        // Typing waits a moment; a chip or a sort reads at once. A machine joining, leaving or
        // changing state reads again.
        .task(id: ReadKey(choice: choice, machines: machinesKey)) {
            if !choice.search.isEmpty { try? await Task.sleep(for: .milliseconds(250)) }
            guard !Task.isCancelled else { return }
            await read(choice)
        }
        // The control plane's level, read again as the page opens (#6591).
        .task(id: model.showsControlPlaneMachine) {
            if model.showsControlPlaneMachine, let control = model.controlRequests { await model.controlCapacity.read(control) }
        }
        // A selected machine that is no longer in the list is let go.
        .onChange(of: rows.map(\.id)) { _, ids in picked.formIntersection(ids) }
        .confirmationDialog(stopTitle, isPresented: Binding(get: { stopping != nil }, set: { if !$0 { stopping = nil } }),
                            titleVisibility: .visible) {
            Button("Stop") { if let hosts = stopping?.hosts { Task { await stopConfirmed(hosts) } } }
            Button("Cancel", role: .cancel) {}
        } message: {
            if let names = stopping?.names {
                Text("\(MachineRow.list(names)) \(names.count == 1 ? "is" : "are") working there. Stopping the machines stops them.")
            }
        }
        .confirmationDialog(MachineBulkActions.deleteTitle(deleting?.hosts.count ?? 0),
                            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            titleVisibility: .visible) {
            Button("Delete", role: .destructive) { if let hosts = deleting?.hosts { Task { await deleteConfirmed(hosts) } } }
            Button("Cancel", role: .cancel) {}
        } message: {
            if let question = deleting?.question { Text(question) }
        }
    }

    // MARK: Parts

    private var header: some View {
        HStack(alignment: .firstTextBaseline) {
            Text("Machines").appText(.reading)
            Spacer(minLength: 16)
            if model.showsControlPlaneMachine, model.controlCapacity.refreshing {
                ProgressView().controlSize(.small).accessibilityLabel("Refreshing capacity")
            }
            if let readAt { LastUpdateText(read: readAt) }
        }
    }

    private var tools: some View {
        HStack(spacing: 12) {
            TextField("Search by name, id or label", text: $choice.search)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 320)
                .accessibilityLabel("Search machines")
            Picker("Sort", selection: $choice.sort) {
                ForEach(MachineListChoice.Sort.allCases, id: \.self) { sort in
                    Text(MachineListChoice.title(sort)).tag(sort)
                }
            }
            .fixedSize()
            Spacer()
        }
    }

    private func chipRow(_ chips: [MachineListChoice.Chip]) -> some View {
        // One line that scrolls sideways: a wrapping row asked at no width stood each chip's words a letter a line, and
        // the window grew to hold them.
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
            ForEach(chips) { chip in
                PageChip(title: chip.filter == .labels ? "# \(chip.value)" : chip.value, count: chip.count, warns: false, on: chip.on) {
                    choice = choice.toggled(chip.filter, chip.value)
                }
                .fixedSize()
                .help(chip.filter.title)
            }
            if choice.narrowed {
                Button("Clear") { choice = choice.cleared }
                    .buttonStyle(.link)
                    .appText(.fine)
            }
            }
            .padding(.vertical, 2)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Filters")
    }

    private var bulkBar: some View {
        let actions = MachineBulkActions(selected)
        return HStack(spacing: 8) {
            Text(MachineBulkActions.selectedWords(selected.count)).fontWeight(.semibold)
            Button(MachineBulkActions.label("Start", reached: actions.start.count, selected: selected.count)) {
                Task { await start(actions.start.map(\.id)) }
            }
            .disabled(working || actions.start.isEmpty)
            Button(MachineBulkActions.label("Stop", reached: actions.stop.count, selected: selected.count)) {
                Task { await stop(actions.stop.map(\.id)) }
            }
            .disabled(working || actions.stop.isEmpty)
            Button(MachineBulkActions.label("Delete…", reached: actions.delete.count, selected: selected.count)) {
                Task { await askDelete(actions.delete) }
            }
            .disabled(working || actions.delete.isEmpty)
            Button("Clear Selection") { picked = [] }
                .buttonStyle(.link)
            Spacer()
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Actions on the selected machines")
    }

    private var table: some View {
        Table(shown, selection: $picked) {
            TableColumn("Machine") { row in
                VStack(alignment: .leading, spacing: 2) {
                    Text(name(row)).fontWeight(.semibold).lineLimit(1)
                    if model.runsControlPlane(row.id) {
                        controlLines(row)
                    } else {
                        Text(MachineListChoice.stateWords(row)).appText(.fine).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
            TableColumn("OS") { row in Text(row.platform).lineLimit(1) }
                .width(min: 60, ideal: 90)
            TableColumn("Version") { row in Text(MachineListChoice.versionWords(row)).lineLimit(1) }
                .width(min: 100, ideal: 150)
            TableColumn("Working") { row in
                let running = model.work.sessionCounts(on: row.id).working
                Text(running == 0 ? "—" : "\(running)").monospacedDigit().lineLimit(1).frame(maxWidth: .infinity, alignment: .trailing)
            }
            .width(min: 60, ideal: 70)
        }
        .contextMenu(forSelectionType: MachineRef.self) { ids in
            menu(for: ids)
        } primaryAction: { ids in
            if let id = ids.first, ids.count == 1 { model.machinePage = id }
        }
        .overlay {
            if rows.isEmpty {
                Text(readAt == nil ? "Loading…" : MachineListChoice.emptyWords(narrowed: choice.narrowed))
                    .appText(.supporting)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityLabel("Machines")
        .frame(minHeight: 160, maxHeight: .infinity)
    }

    @ViewBuilder
    private func menu(for ids: Set<MachineRef>) -> some View {
        let chosen = rows.filter { ids.contains($0.id) }
        let actions = MachineBulkActions(chosen)
        if chosen.count == 1, let only = chosen.first {
            Button("Open") { model.machinePage = only.id }
            Divider()
        }
        Button(MachineBulkActions.label("Start", reached: actions.start.count, selected: chosen.count)) {
            Task { await start(actions.start.map(\.id)) }
        }
        .disabled(working || actions.start.isEmpty)
        Button(MachineBulkActions.label("Stop", reached: actions.stop.count, selected: chosen.count)) {
            Task { await stop(actions.stop.map(\.id)) }
        }
        .disabled(working || actions.stop.isEmpty)
        Divider()
        Button(MachineBulkActions.label("Delete…", reached: actions.delete.count, selected: chosen.count)) {
            Task { await askDelete(actions.delete) }
        }
        .disabled(working || actions.delete.isEmpty)
    }

    private var footer: some View {
        HStack(spacing: 6) {
            Text(MachineListChoice.countWords(shown: rows.count, total: total))
            if let next {
                Text("·")
                Button(MachineListChoice.showMoreWords(shown: rows.count, total: total)) {
                    Task { await read(choice, cursor: next) }
                }
                .buttonStyle(.link)
                .disabled(loading)
            }
        }
        .appText(.fine)
        .foregroundStyle(.secondary)
    }

    // MARK: State

    private struct ReadKey: Equatable {
        var choice: MachineListChoice
        var machines: String
    }

    /// Each machine and its state, so a join, a leave or a change of state reads the list again.
    private var machinesKey: String {
        model.hosts.controlRows.values.map { "\($0.id.rawValue):\($0.state)" }.sorted().joined(separator: ",")
    }

    private var selected: [DaemonAPI.ControlMachine] { rows.filter { picked.contains($0.id) } }

    /// The rows as drawn: the control plane's machines first, for an admin with its flag (#6591).
    private var shown: [DaemonAPI.ControlMachine] {
        model.showsControlPlaneMachine ? ControlPlaneMachine.first(rows) : rows
    }

    /// Under a machine that runs the control plane (#6591): where it is, then its level in the
    /// level's tint, as a host's capacity (#4748).
    @ViewBuilder
    private func controlLines(_ row: DaemonAPI.ControlMachine) -> some View {
        let look = model.controlPlaneLook(row.id)
        let none = model.controlCapacity.noReadingWords
        Text(ControlPlaneMachine.controlOnly(row) ? ControlPlanePage.meta(row, among: model.controlPlaneHosts ?? rows) : "Runs the control plane \u{00B7} \(MachineListChoice.stateWords(row))")
            .appText(.fine).foregroundStyle(.secondary).lineLimit(1)
        HStack(spacing: 5) {
            Circle().fill((look?.level == .ok ? StateTint.vouched : MachinePage.tint(look?.level)).style(or: .secondary))
                .frame(width: 7, height: 7)
                .accessibilityHidden(true)
            // The whole line when it fits, else the level and its main figure (#7366).
            ViewThatFits(in: .horizontal) {
                Text(look == nil ? none.line : ControlPlaneMachine.rowLine(look))
                Text(look == nil ? none.line : ControlPlaneMachine.shortRowLine(look))
            }
            .foregroundStyle(look.map { MachinePage.tint($0.level) }?.style(or: .secondary) ?? AnyShapeStyle(.secondary))
        }
        .appText(.fine)
        .monospacedDigit()
        .lineLimit(1)
        .help(look == nil ? none.detail : ControlPlaneMachine.rowHelp(look))
    }

    private var stopTitle: String {
        let count = stopping?.hosts.count ?? 0
        return count == 1 ? "Stop 1 machine?" : "Stop \(count) machines?"
    }

    private func name(_ row: DaemonAPI.ControlMachine) -> String {
        // A control-only machine that runs no copy is not the control plane yet (#7393).
        if model.runsControlPlane(row.id), ControlPlaneMachine.controlOnly(row) {
            return ControlPlaneMachine.title(row, among: model.controlPlaneHosts ?? rows)
        }
        return row.id == .mac ? "This Mac" : row.name
    }

    // MARK: Reading

    private func read(_ asked: MachineListChoice, cursor: String? = nil) async {
        guard let control = model.controlRequests else {
            problem = "The control plane can’t be reached."
            return
        }
        serial += 1
        let mine = serial
        loading = true
        defer { if mine == serial { loading = false } }
        do {
            let answer = try await control.call(DaemonAPI.Method.hostsList, asked.query(cursor: cursor))
            guard mine == serial else { return }
            if answer.arrayValue != nil {
                // A control plane before machines/2: every machine, filtered and cut here.
                let all = try answer.decode([DaemonAPI.ControlMachine].self)
                let shown = asked.filtered(all)
                let from = cursor.flatMap(Int.init) ?? 0
                let upTo = min(shown.count, from + MachineListChoice.pageSize)
                rows = Array(shown.prefix(upTo))
                next = shown.count > upTo ? String(upTo) : nil
                total = shown.count
                counts = MachineListChoice.counts(of: all)
            } else {
                let page = try answer.decode(DaemonAPI.MachineList.self)
                rows = cursor == nil ? page.machines : rows + page.machines
                next = page.next
                total = page.total
                counts = page.counts
            }
            readAt = Date()
            problem = nil
        } catch let error as JSONRPCError {
            if mine == serial { problem = HostProblem.controlRefusal(error) }
        } catch {
            if mine == serial { problem = "The control plane can’t be reached." }
        }
    }

    private func afterwards() async {
        picked = []
        working = false
        await read(choice)
    }

    // MARK: Actions

    private func start(_ hosts: [MachineRef]) async {
        working = true
        await withTaskGroup(of: Void.self) { group in
            for host in hosts { group.addTask { await model.startHost(host) } }
        }
        await afterwards()
    }

    /// Stop asks once, for all of them, when sessions work on some: the control plane says who.
    private func stop(_ hosts: [MachineRef]) async {
        working = true
        var asked: [MachineRef] = []
        var names: [String] = []
        for host in hosts {
            if let working = await model.stopHost(host) {
                asked.append(host)
                names += working
            }
        }
        if !asked.isEmpty { stopping = (asked, names) }
        await afterwards()
    }

    private func stopConfirmed(_ hosts: [MachineRef]) async {
        working = true
        for host in hosts { _ = await model.stopHost(host, confirmed: true) }
        await afterwards()
    }

    /// Delete lists the unpushed work of every machine first, as Delete on one machine does (#3979).
    private func askDelete(_ machines: [DaemonAPI.ControlMachine]) async {
        working = true
        var previews: [(name: String, preview: DaemonAPI.StartedMachinePreview?)] = []
        for machine in machines { previews.append((name(machine), await model.deletePreview(machine.id))) }
        working = false
        deleting = (machines.map(\.id), MachineBulkActions.deleteQuestion(machines.map(name), unpushed: MachineBulkActions.unpushed(previews)))
    }

    private func deleteConfirmed(_ hosts: [MachineRef]) async {
        working = true
        for host in hosts { await model.deleteMachine(host) }
        await afterwards()
    }
}
