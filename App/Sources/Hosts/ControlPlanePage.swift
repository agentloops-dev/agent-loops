import AgentsKitCore
import SwiftUI

/// The page of a machine that runs only the control plane (#6591, gate 2 of #6593), from its row
/// in Machines: its level and why, load per core, memory and I/O pressure, memory, the process
/// and the store, with "Last update" from the time of the last read (#5139). Read with
/// `control/capacity` when it opens and every 15 s while it is open, as often as the control
/// plane looks at itself; `control/capacityChanged` moves the level between reads. A machine
/// that also runs agents has the same figures as a section of its own page (`MachinePage`).
struct ControlPlanePage: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef

    private var row: DaemonAPI.ControlMachine? { model.controlPlaneHosts?.first { $0.id == host } }
    private var title: String { ControlPlaneMachine.title(row, among: model.controlPlaneHosts ?? []) }

    var body: some View {
        let look = model.controlPlaneLook(host)
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                HStack(alignment: .firstTextBaseline, spacing: 16) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(title).appText(.reading)
                        Text(ControlPlanePage.meta(row, among: model.controlPlaneHosts ?? [])).appText(.supporting).foregroundStyle(.secondary)
                    }
                    Spacer(minLength: 0)
                    ControlCapacityFreshness()
                }
                ControlCapacityFigures(look: look)
            }
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
        .navigationTitle(title)
        .task(id: host) { await ControlCapacityFigures.follow(model) }
    }

    /// "control.agentloops.dev · Linux · Admin view": where the control plane is, and what it runs on.
    /// With several control-only machines (#7362), the window's endpoint is no one machine's
    /// address, so it is left out, and so is a name that the title says already.
    static func meta(_ row: DaemonAPI.ControlMachine?, among rows: [DaemonAPI.ControlMachine]) -> String {
        var parts: [String] = []
        let label = ControlPlaneMachine.label(row, among: rows)
        if label == nil, case .remote(let membership) = ControlConfig.endpoint, let url = membership.url.flatMap(URL.init(string:)),
           let address = url.host() {
            parts.append(address)
        }
        if let row {
            if !row.name.isEmpty, row.name != ControlPlaneMachine.title, row.name != label { parts.append(row.name) }
            if !row.platform.isEmpty { parts.append(row.platform) }
        }
        parts.append("Admin view")
        return parts.joined(separator: " \u{00B7} ")
    }
}

/// "Last update just now", or "Refreshing…" while a read is under way (#5139).
struct ControlCapacityFreshness: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        HStack(spacing: 8) {
            if model.controlCapacity.refreshing {
                ProgressView().controlSize(.small)
                    .accessibilityLabel("Refreshing capacity")
            }
            if let read = model.controlCapacity.readAt { LastUpdateText(read: read) }
        }
    }
}

/// The control plane's figures (gate 2 of #6593): the level and its sentence, four figures across,
/// then Memory, Process and Store. Before the first look, why there is none; after a read that
/// failed, the last look under a line that says how old it is.
struct ControlCapacityFigures: View {
    @Environment(AppModel.self) private var model
    let look: DaemonAPI.ControlCapacityCopy?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let failure = model.controlCapacity.failure {
                LastUpdateTimeline(read: model.controlCapacity.readAt ?? .distantPast) { now in
                    Label(LastUpdate.banner(failure, read: model.controlCapacity.readAt, now: now), icon: "exclamationmark.triangle")
                        .foregroundStyle(StateTint.warning.style(or: .secondary))
                }
                .appText(.supporting)
            }
            if let look {
                levelLine(look)
                HStack(alignment: .top, spacing: 12) {
                    ForEach(ControlPlaneMachine.summary(look), id: \.self) { figure in
                        tile(figure, tint: figure.label == "Capacity" ? MachinePage.tint(look.level) : .none)
                    }
                }
                ForEach(ControlPlaneMachine.sections(look), id: \.title) { section in
                    VStack(alignment: .leading, spacing: 6) {
                        Text(section.title).fontWeight(.semibold)
                        Text(section.figures.map { "\($0.label) \($0.value)" }.joined(separator: "  \u{00B7}  "))
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                    }
                    .accessibilityElement(children: .combine)
                }
                Text(ControlPlaneMachine.foot).appText(.fine).foregroundStyle(.secondary)
            } else if model.controlCapacity.readAt != nil || model.controlCapacity.failure != nil {
                let words = model.controlCapacity.noReadingWords
                VStack(spacing: 6) {
                    Text(words.line).fontWeight(.semibold)
                    Text(words.detail).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 28)
                .accessibilityElement(children: .combine)
            } else {
                ProgressView().frame(maxWidth: .infinity)
            }
        }
    }

    /// "The control plane is near capacity: its disk is busy.", with the level's dot.
    private func levelLine(_ look: DaemonAPI.ControlCapacityCopy) -> some View {
        let tint = MachinePage.tint(look.level)
        return HStack(spacing: 8) {
            Circle().fill((look.level == .ok ? StateTint.vouched : tint).style(or: .secondary)).frame(width: 8, height: 8).accessibilityHidden(true)
            Text(ControlPlaneMachine.sentence(look))
                .foregroundStyle(tint.style(or: .primary))
        }
        .fontWeight(.semibold)
    }

    private func tile(_ figure: ControlPlaneMachine.Figure, tint: StateTint) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(figure.label).appText(.fine).foregroundStyle(.secondary)
            Text(figure.value).appText(.reading).fontWeight(.semibold).monospacedDigit()
                .foregroundStyle(tint.style(or: .primary))
            if let detail = figure.detail {
                Text(detail).appText(.fine).foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .paperWell(in: RoundedRectangle(cornerRadius: Paper.Radius.card))
        .accessibilityElement(children: .combine)
    }

    /// Reads now and every 15 s while the page is open: the figures move between level changes.
    static func follow(_ model: AppModel) async {
        while !Task.isCancelled {
            if let control = model.controlRequests { await model.controlCapacity.read(control) }
            try? await Task.sleep(for: .seconds(ControlPlaneMachine.lookSeconds))
        }
    }
}

/// The control plane on the page of a machine that also runs agents (#6591): the same figures,
/// as a section of `MachinePage`.
struct ControlPlaneSection: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef

    var body: some View {
        Section {
            ControlCapacityFigures(look: model.controlPlaneLook(host))
                .padding(.vertical, 4)
        } header: {
            HStack {
                Text("Control plane")
                Spacer()
                ControlCapacityFreshness()
            }
        }
        .task(id: host) { await ControlCapacityFigures.follow(model) }
    }
}

/// A machine that runs only the control plane, under Machines in the sidebar (#6591): "Control
/// plane", in its level's colour as a host's capacity is (#4748), and under it the level. It has
/// no sessions, start, stop or delete: the control plane refuses them (#6609). Open is its page.
struct ControlPlaneRow: View {
    @Environment(AppModel.self) private var model
    let host: MachineRef
    let open: () -> Void

    var body: some View {
        let look = model.controlPlaneLook(host)
        let line = ControlPlaneMachine.level(look)
        let rows = model.controlPlaneHosts ?? []
        let row = rows.first { $0.id == host }
        let none = model.controlCapacity.noReadingWords
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            MachineIconView(symbol: MachineIcon.symbol(isThisMac: false, row: nil),
                            icon: look == nil ? .stopped : MachineIcon.of(.online, diskLow: false, capacity: look?.level))
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 2) {
                Text(ControlPlaneMachine.title(row, among: rows)).foregroundStyle(.primary).lineLimit(1)
                Text(look == nil ? none.line : line)
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 4)
        }
        .help(look.map { ControlPlaneMachine.sentence($0, on: ControlPlaneMachine.label(row, among: rows)) } ?? none.detail)
        .accessibilityElement(children: .contain)
        .accessibilityValue(look == nil ? none.line : line)
        .contextMenu {
            Button("Open", action: open)
        }
    }
}
