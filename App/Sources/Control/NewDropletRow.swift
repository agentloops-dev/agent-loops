import AgentsKitCore
import SwiftUI

/// New Droplet on a connected DigitalOcean provider's card (#6577, epic #6070 W3): one click
/// makes a machine with what New Machine would make (the region and size used last, else the
/// nearest region and the recommended size, #6073), and the line beside it says where and what
/// it costs before the click. Change… opens the whole New Machine form. The steps of the make
/// show on its row below (W4, `ProviderSetupSteps`), and Open Machine once it joins (W5). The
/// web page's `NewDropletRow` is the same.
struct NewDropletRow: View {
    @Environment(AppModel.self) private var model
    let control: ControlSettingsModel
    let provider: DaemonAPI.ProviderSummary
    /// Change…: the whole form, at this provider.
    let change: () -> Void
    /// The names made here by this row, to say when each is ready (W5).
    @Binding var made: Set<String>

    @State private var plan: OneClickMake.Plan?
    @State private var problem: String?
    @State private var working = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Button(OneClickMake.button) { press() }
                    .buttonStyle(.paperProminent)
                    .disabled(plan == nil || working || !provider.usable)
                    .help(provider.usable ? (plan?.note ?? "") : GroupWords.mayNotUse(provider, groups: control.groups))
                Button(OneClickMake.change, action: change)
                    .buttonStyle(.paper)
                    .disabled(working || !provider.usable)
                if working { ProgressView().controlSize(.small) }
                if let plan {
                    Text(plan.summary).appText(.supporting).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 0)
            }
            if let plan {
                Text(plan.note).appText(.fine).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let problem {
                Text(problem).appText(.supporting).tinted(.failure).fixedSize(horizontal: false, vertical: true)
            }
            ForEach(readyHosts, id: \.id) { host in
                HStack(spacing: 8) {
                    Image(icon: "checkmark.circle.fill").foregroundStyle(StateTint.vouched.style(or: .secondary))
                    Text(OneClickMake.ready(host.name)).appText(.supporting)
                    Spacer(minLength: 8)
                    Button("Done") { made.remove(host.name) }.buttonStyle(.paper)
                    Button(OneClickMake.openMachine) {
                        made.remove(host.name)
                        model.machinePage = host.id
                    }
                    .buttonStyle(.paperProminent)
                }
            }
        }
        // ControlRow's insets, so the row lines up with the card's others.
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        // Read again when the provider's row changes: a make takes the next name.
        .task(id: provider.workspaces.count) { await load() }
    }

    /// The machines this row made that joined, and whose set-up is done.
    private var readyHosts: [DaemonAPI.ControlMachine] {
        control.hosts.filter { host in
            made.contains(host.name) && (host.setup.map(MachineSetupWords.isReady) ?? true)
        }
    }

    private func load() async {
        switch await control.workspaceOptions(provider.id) {
        case .success(let options):
            plan = OneClickMake.plan(options)
            if plan == nil { problem = "\(provider.name) gives no name for a new machine: use \(OneClickMake.change)" }
        case .failure(let refusal):
            plan = nil
            problem = refusal.message
        }
    }

    private func press() {
        guard let plan else { return }
        working = true
        problem = nil
        made.insert(plan.name)
        Task {
            // The call answers once DigitalOcean made it; its row shows each step meanwhile.
            let result = await control.makeMachine(provider.id, name: plan.name, options: .object(plan.values))
            working = false
            switch result {
            case .success(let created):
                if created.workspace.state == "failed" { made.remove(plan.name) }
            case .failure(let refusal):
                made.remove(plan.name)
                problem = refusal.message
            }
            await load()
        }
    }
}

/// A make's steps on its provider's row (#6577, W4), from the row's `setup` (#6073): a mark,
/// the step and its line, and the time a running step has taken. The machine's page has the
/// whole set-up, with Try Again, once it joins.
struct ProviderSetupSteps: View {
    let setup: DaemonAPI.MachineSetup

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            VStack(alignment: .leading, spacing: 4) {
                ForEach(setup.steps) { step in
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        Image(icon: MachineSetupSection<EmptyView>.symbol(step.state))
                            .foregroundStyle(MachineSetupSection<EmptyView>.tint(step.state).style(or: .secondary))
                            .frame(width: 16)
                            .accessibilityLabel(MachineSetupSection<EmptyView>.stateWords(step.state))
                        Text(MachineSetupWords.name(step)).appText(.supporting)
                        Text(MachineSetupWords.line(step, now: context.date))
                            .appText(.fine).foregroundStyle(.secondary).lineLimit(2)
                        Spacer(minLength: 8)
                        if let time = MachineSetupWords.time(step, now: context.date) {
                            Text(time).appText(.fine).monospacedDigit().foregroundStyle(.secondary)
                        }
                    }
                    .accessibilityElement(children: .combine)
                }
                if let failed = MachineSetupWords.failedStep(setup) {
                    Text(failed.error ?? "\(MachineSetupWords.name(failed)) failed.")
                        .appText(.supporting).tinted(.failure).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .padding(.leading, 34)
        .padding(.trailing, 16)
        .padding(.bottom, 10)
    }
}
