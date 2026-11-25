import AgentsKitCore
import SwiftUI

/// A machine page's Agent Loops sections (#5273, spec 5239 board 6), drawn by the window's
/// `MachinePage` and the Remote's `RemoteMachinePage`, as the web page's `MachineAgents`: the
/// version the machine runs, since when, and the changes since the version before, newest
/// first, with Show all; the person's other machines, each with how far it is behind this one,
/// which opens those changes; and the versions this machine ran. Read from the control plane
/// with `machines/agents` when the page opens, so it says when (#5139). The window also says how the
/// machine updates itself (#6114), read from the machine every few seconds while the page is open.
struct MachineAgentsSections: View {
    let host: MachineRef
    /// `machines/agents` on the control plane; nil when there is none to ask.
    let read: (@MainActor (MachineRef) async throws -> DaemonAPI.MachineAgents)?
    /// `machines/agentsChanges` on the control plane.
    let changes: @MainActor (DaemonAPI.AgentsChangesRequest) async throws -> DaemonAPI.AgentsChanges
    /// The machine's name, for the line when it does not answer `readUpdate`.
    var name = ""
    /// `selfUpdate/status` on the machine (#6114, tier Mac + web); nil when it can't be asked, and on the Remote.
    var readUpdate: (@MainActor () async throws -> DaemonAPI.SelfUpdate)?

    @State private var update: DaemonAPI.SelfUpdate?
    @State private var updateFailed = false
    @State private var agents: DaemonAPI.MachineAgents?
    @State private var readAt: Date?
    @State private var failure: String?
    /// Every change since the version before, once Show all was pressed.
    @State private var all: DaemonAPI.AgentsChanges?
    /// The changes between this machine and another, once its line was pressed.
    @State private var opened: [MachineRef: DaemonAPI.AgentsChanges] = [:]
    @State private var asking: Set<String> = []

    var body: some View {
        Group {
            Section {
                if let agents {
                    versionRows(agents)
                } else if let failure {
                    Text(failure).foregroundStyle(Paper.secondary)
                } else {
                    ProgressView().frame(maxWidth: .infinity, alignment: .leading)
                }
                if readUpdate != nil { updateRows }
            } header: {
                Text(AgentsVersionWords.title)
            } footer: {
                if let readAt { LastUpdateText(read: readAt) }
            }
            if let agents, let shown = all ?? agents.changes, !shown.changes.isEmpty {
                Section(AgentsVersionWords.listHeading(shown)) {
                    ForEach(shown.changes) { ChangeRow(change: $0) }
                    if let more = AgentsVersionWords.showAll(shown) {
                        Button(more) { Task { await showAll(agents) } }
                            .disabled(asking.contains("all"))
                    }
                }
            }
            if let agents, !agents.others.isEmpty {
                Section("Other machines") {
                    ForEach(agents.others) { other in otherRows(other, of: agents) }
                }
            }
            if let agents, agents.history.count > 1 {
                Section("History") {
                    ForEach(agents.history, id: \.self) { past in
                        LabeledContent(AgentsVersionWords.short(past.version)) {
                            if let since = past.since {
                                Text(AgentsVersionWords.when(since)).monospacedDigit().foregroundStyle(Paper.secondary)
                            }
                        }
                    }
                }
            }
        }
        .task(id: host) { await load() }
        .task(id: UpdateRead(host: host, asks: readUpdate != nil)) { await followUpdate() }
    }

    private struct UpdateRead: Hashable {
        let host: MachineRef
        let asks: Bool
    }

    /// Whether the machine updates itself, and where that is (#6114).
    @ViewBuilder
    private var updateRows: some View {
        if let update {
            LabeledContent(SelfUpdateWords.automatic, value: SelfUpdateWords.value(update))
            Text(SelfUpdateWords.line(update))
                .appText(.fine)
                .foregroundStyle((SelfUpdateWords.failed(update) ? StateTint.failure : .none).style(or: Paper.secondary))
                .textSelection(.enabled)
        } else if updateFailed {
            Text(SelfUpdateWords.noAnswer(name)).appText(.fine).foregroundStyle(Paper.secondary)
        }
    }

    /// Reads the machine's own state while the page is open. A machine that runs another version
    /// since the last read was updated, so its version and changes are read again.
    private func followUpdate() async {
        update = nil
        updateFailed = false
        guard let readUpdate else { return }
        var running: String?
        while !Task.isCancelled {
            do {
                let now = try await readUpdate()
                update = now
                updateFailed = false
                if let running, running != now.running { await load() }
                running = now.running
            } catch {
                update = nil
                updateFailed = true
            }
            try? await Task.sleep(for: .seconds(3))
        }
    }

    @ViewBuilder
    private func versionRows(_ agents: DaemonAPI.MachineAgents) -> some View {
        if agents.version.isEmpty {
            Text(AgentsVersionWords.none).foregroundStyle(Paper.secondary)
        } else {
            let parts = AgentsVersionWords.parts(agents.version)
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(parts.name).appText(.reading).monospacedDigit().textSelection(.enabled)
                    if let build = parts.build {
                        Text(build).foregroundStyle(Paper.secondary).monospacedDigit().textSelection(.enabled)
                    }
                }
                let line = AgentsVersionWords.sinceLine(agents)
                if !line.isEmpty {
                    Text(line).foregroundStyle(Paper.secondary).monospacedDigit()
                }
            }
            .accessibilityElement(children: .combine)
        }
    }

    @ViewBuilder
    private func otherRows(_ other: DaemonAPI.MachineAgentsOther, of agents: DaemonAPI.MachineAgents) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(other.name)
            Text(AgentsVersionWords.otherLine(other)).appText(.fine).foregroundStyle(Paper.secondary).monospacedDigit()
            if let behind = AgentsVersionWords.behindLine(other) {
                Button {
                    Task { await open(other, of: agents) }
                } label: {
                    Text(behind).appText(.fine)
                        .foregroundStyle(AgentsVersionWords.isBehind(other)
                            ? StateTint.warning.style(or: Paper.secondary) : AnyShapeStyle(Paper.secondary))
                }
                .buttonStyle(.plain)
                .disabled(asking.contains(other.host.rawValue))
                .accessibilityHint("Shows those changes")
            }
        }
        if let compare = opened[other.host] {
            ForEach(compare.changes) { ChangeRow(change: $0) }
            if let more = AgentsVersionWords.showAll(compare) {
                Button(more) { Task { await open(other, of: agents, all: true) } }
                    .disabled(asking.contains(other.host.rawValue))
            }
        }
    }

    private func load() async {
        agents = nil
        all = nil
        opened = [:]
        failure = nil
        readAt = nil
        guard let read else {
            failure = "No control plane to ask which version runs here."
            return
        }
        do {
            agents = try await read(host)
            readAt = Date()
        } catch {
            failure = "The control plane can’t say which version runs here now."
        }
    }

    private func showAll(_ agents: DaemonAPI.MachineAgents) async {
        guard let previous = agents.previous else { return }
        asking.insert("all")
        defer { asking.remove("all") }
        all = try? await changes(DaemonAPI.AgentsChangesRequest(base: previous, head: agents.version))
    }

    /// Opens or closes another machine's changes: the newest few, then every one with Show all.
    private func open(_ other: DaemonAPI.MachineAgentsOther, of agents: DaemonAPI.MachineAgents, all: Bool = false) async {
        if !all, opened[other.host] != nil {
            opened[other.host] = nil
            return
        }
        asking.insert(other.host.rawValue)
        defer { asking.remove(other.host.rawValue) }
        let request = AgentsVersionWords.compare(from: other, to: agents.version, limit: all ? nil : AgentsVersionWords.shown)
        if let read = try? await changes(request) { opened[other.host] = read }
    }
}

/// One change: its PR's number, which opens it on GitHub, its title and its day.
private struct ChangeRow: View {
    let change: DaemonAPI.AgentsChange

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if let url = AgentsVersionWords.link(change) {
                Link(AgentsVersionWords.label(change), destination: url).monospacedDigit()
            }
            Text(change.title).lineLimit(2)
            Spacer(minLength: 8)
            if let date = change.date {
                Text(AgentsVersionWords.day(date)).appText(.fine).foregroundStyle(Paper.secondary).monospacedDigit()
            }
        }
    }
}
