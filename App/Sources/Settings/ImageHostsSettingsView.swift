import AgentsKitCore
import SwiftUI

/// Settings ▸ Machines ▸ Image Hosts (#7139, epic #7136): the hosts whose images a machine's Work
/// pages load, with Remove. The list belongs to the machine whose daemon reads the images. The
/// kept list first, then the machine's. A host is added only from a blocked image.
struct ImageHostsSettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var chosen: MachineRef?
    @State private var lists: [MachineRef: ImageHostsList] = [:]

    private var machine: MachineRef? {
        let machines = model.imageHostMachines.map(\.id)
        return chosen.flatMap { machines.contains($0) ? $0 : nil } ?? machines.first
    }

    private func name(_ id: MachineRef) -> String {
        model.isOnThisMac(id) ? "This Mac" : model.imageHostMachines.first { $0.id == id }?.name ?? model.hosts.label(id)
    }

    private func online(_ id: MachineRef) -> Bool {
        model.imageHostMachines.first { $0.id == id }?.state != "offline" && !model.hostUnreachable(id)
    }

    var body: some View {
        if let machine {
            let list = lists[machine]
            VStack(spacing: 0) {
                if let failed = list?.notRemoved {
                    PaneBanner(tone: .failure, ImageHostsWords.couldNotRemove(failed.host, machine: name(machine))) {
                        Button(ImageHostsWords.tryAgain) { Task { await list?.remove(failed.host) } }
                            .disabled(!online(machine))
                    }
                    .help(failed.why)
                    .paneBannerInset()
                } else if let problem = list?.problem {
                    PaneBanner(tone: .failure, ImageHostsWords.couldNotRead(machine: name(machine)), detail: problem) {
                        Button(ImageHostsWords.tryAgain) { Task { await list?.load(live: true) } }
                            .disabled(!online(machine))
                    }
                    .paneBannerInset()
                }
                form(machine, list)
            }
            .task(id: Ask(machine: machine, online: online(machine))) { await load(machine) }
        } else {
            Text("No machine keeps image hosts yet. Update Agent Loops on a machine to see them.")
                .foregroundStyle(.secondary)
                .padding(20)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private struct Ask: Equatable {
        let machine: MachineRef
        let online: Bool
    }

    private func load(_ machine: MachineRef) async {
        let list = lists[machine] ?? ImageHostsList(calls(machine))
        lists[machine] = list
        await list.load(live: online(machine))
    }

    private func calls(_ machine: MachineRef) -> ImageHostsList.Calls {
        ImageHostsList.Calls(
            kept: { [model] in
                await model.keptWorkRead(DaemonAPI.ImageHosts.self, .imageHosts, machine.rawValue).map { (hosts: $0.value, at: $0.at) }
            },
            list: { [model] in
                try await model.forge(DaemonAPI.Method.imageHostsList, DaemonAPI.Empty(), returning: DaemonAPI.ImageHosts.self, on: machine)
            },
            remove: { [model] host in
                try await model.forge(DaemonAPI.Method.imageHostsRemove, DaemonAPI.ImageHostRequest(host: host),
                                      returning: DaemonAPI.ImageHosts.self, on: machine)
            },
            keep: { [model] in model.keepWork($0, .imageHosts, machine.rawValue) })
    }

    private func form(_ machine: MachineRef, _ list: ImageHostsList?) -> some View {
        Form {
            Section {
                LabeledContent("Machine") {
                    MachinePicker(label: "Machine", value: machine, title: name(machine),
                                  keep: { $0.has(Capability.imageHosts1) }) { if let id = $0 { chosen = id } }
                }
            } header: {
                HStack(spacing: 6) {
                    Text(ImageHostsWords.title)
                    Spacer()
                    if list?.reading == true { ProgressView().controlSize(.mini) }
                    if let read = list?.readAt { LastUpdateText(read: read) }
                }
            }
            .paperListRow()

            Section {
                if let list, let hosts = list.hosts {
                    if hosts.isEmpty { empty }
                    ForEach(hosts, id: \.host) { row($0, list, machine) }
                } else if online(machine), list?.problem == nil {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text(ImageHostsWords.firstRead).foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 10)
                }
            } footer: {
                VStack(alignment: .leading, spacing: 8) {
                    if !online(machine) { Text(verbatim: ImageHostsWords.offline(machine: name(machine))) }
                    Text(verbatim: ImageHostsWords.footer(machine: name(machine)))
                }
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .paperListRow()
        }
        .paperForm()
    }

    private var empty: some View {
        VStack(spacing: 3) {
            Text(ImageHostsWords.none).fontWeight(.semibold)
            Text(ImageHostsWords.noneDetail).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 14)
    }

    private func row(_ host: DaemonAPI.ImageHost, _ list: ImageHostsList, _ machine: MachineRef) -> some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(verbatim: host.host).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                Text(verbatim: ImageHostsWords.allowedOn(host.addedAt)).appText(.fine).foregroundStyle(.secondary)
                if list.notRemoved?.host == host.host {
                    Text(ImageHostsWords.notRemoved).appText(.fine).foregroundStyle(StateTint.failure.style(or: .primary))
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Button { Task { await list.remove(host.host) } } label: {
                Text(ImageHostsWords.remove).foregroundStyle(StateTint.failure.style(or: .primary))
            }
            .accessibilityLabel("\(ImageHostsWords.remove) \(host.host)")
            .disabled(!online(machine))
        }
    }
}
