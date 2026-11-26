import AgentsKitCore
import SwiftUI

/// A machine of Settings ▸ Image Hosts: its name for a person, and whether it takes a Remove now.
struct ImageHostsMachine: Equatable {
    let id: MachineRef
    let name: String
    let online: Bool
}

/// Settings ▸ Image Hosts (#7139, epic #7136): the hosts whose images a machine's Work pages load,
/// with Remove. The list belongs to the machine whose daemon reads the images. The kept list first,
/// then the machine's. A host is added only from a blocked image. Drawn by the Mac's Settings ▸
/// Machines ▸ Image Hosts and by a page of the Remote's Settings (#7140); each gives its own
/// machine menu.
struct ImageHostsPane<Picker: View>: View {
    let model: any WorkModel
    /// The machines that keep the list, the home one first.
    let machines: [ImageHostsMachine]
    @ViewBuilder let picker: (_ machine: ImageHostsMachine, _ choose: @escaping (MachineRef) -> Void) -> Picker

    @State private var chosen: MachineRef?
    @State private var lists: [MachineRef: ImageHostsList] = [:]

    private var machine: ImageHostsMachine? {
        machines.first { $0.id == chosen } ?? machines.first
    }

    var body: some View {
        if let machine {
            let list = lists[machine.id]
            VStack(spacing: 0) {
                if let failed = list?.notRemoved {
                    PaneBanner(tone: .failure, ImageHostsWords.couldNotRemove(failed.host, machine: machine.name)) {
                        Button(ImageHostsWords.tryAgain) { Task { await list?.remove(failed.host) } }
                            .disabled(!machine.online)
                    }
                    .help(failed.why)
                    .paneBannerInset()
                } else if let problem = list?.problem {
                    PaneBanner(tone: .failure, ImageHostsWords.couldNotRead(machine: machine.name), detail: problem) {
                        Button(ImageHostsWords.tryAgain) { Task { await list?.load(live: true) } }
                            .disabled(!machine.online)
                    }
                    .paneBannerInset()
                }
                form(machine, list)
            }
            .task(id: machine) { await load(machine) }
        } else {
            Text("No machine keeps image hosts yet. Update Agent Loops on a machine to see them.")
                .foregroundStyle(.secondary)
                .padding(20)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func load(_ machine: ImageHostsMachine) async {
        let list = lists[machine.id] ?? ImageHostsList(calls(machine.id))
        lists[machine.id] = list
        await list.load(live: machine.online)
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

    private func form(_ machine: ImageHostsMachine, _ list: ImageHostsList?) -> some View {
        Form {
            Section {
                LabeledContent("Machine") { picker(machine) { chosen = $0 } }
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
                } else if machine.online, list?.problem == nil {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text(ImageHostsWords.firstRead).foregroundStyle(.secondary)
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 10)
                }
            } footer: {
                VStack(alignment: .leading, spacing: 8) {
                    if !machine.online { Text(verbatim: ImageHostsWords.offline(machine: machine.name)) }
                    Text(verbatim: ImageHostsWords.footer(machine: machine.name))
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

    private func row(_ host: DaemonAPI.ImageHost, _ list: ImageHostsList, _ machine: ImageHostsMachine) -> some View {
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
            #if os(iOS)
            // In a list a plain button takes the whole row's tap.
            .buttonStyle(.borderless)
            #endif
            .accessibilityLabel("\(ImageHostsWords.remove) \(host.host)")
            .disabled(!machine.online)
        }
    }
}
