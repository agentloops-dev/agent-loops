import AgentsKitCore
import SwiftUI

/// Settings (#846): You first, as on the Mac (#3998): lines that open Status ▸ Secrets (#5257) and Status ▸ Accounts
/// (#5258), which are Core (#4032, #4033); this app alone (Voice; Live Activities, which only a phone or an iPad has
/// (#1657); and Sidebar, kept on the Mac's host and the same on every client: #1033, #2079), and
/// Help. Control plane, machine, user and agent configuration settings are Mac + web (#2796,
/// #2798): the Remote does not have them, but for Agents ▸ Policy (#2957), the daily
/// summary's control (#3351), which are Core, and a machine's Image Hosts (#7140).
struct RemoteSettings: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(RemoteModel.self) private var model

    var body: some View {
        NavigationStack {
            Form {
                // The person's own, on every machine of theirs (#3998), as the Mac's Settings ▸ You.
                Section {
                    // The secrets and the accounts have pages of their own, under Status (#5257, #5258): a line each here.
                    Button {
                        model.sidebarItem = .secretsStatus
                        dismiss()
                    } label: {
                        Label(SecretsPage.openSecrets, icon: "key")
                    }
                    // Environment variables have a page of their own too, Status ▸ Environment (#5260).
                    Button {
                        model.sidebarItem = .environmentStatus
                        dismiss()
                    } label: {
                        Label(EnvironmentPage.open, icon: "terminal")
                    }
                    Button {
                        model.sidebarItem = .accountsStatus
                        dismiss()
                    } label: {
                        Label(AccountsPage.openAccounts, icon: "person.crop.rectangle.stack")
                    }
                } header: {
                    Text(SettingsScope.you.title)
                } footer: {
                    Text(SecretsPage.settingsLine + " " + AccountsPage.settingsLine)
                }
                .paperListRow()
                Section(SettingsScope.thisApp.title) {
                    SettingsPageLink("Voice", systemImage: "waveform") { VoiceSettingsSection() }
                    // The notice limits are rules of Agents ▸ Policy now (#2957, FR-023).
                    SettingsPageLink("Notifications", systemImage: "bell") { LiveActivitySettingsSection() }
                    // Kept on the Mac's host, the same on every client (#1033, #2079). The archive
                    // switch is a rule of Agents ▸ Policy now (FR-023).
                    SettingsPageLink("Sidebar", systemImage: "sidebar.left") { SidebarSettingsSection() }
                    // The feature flags in work, then the features that are not GA, a switch each, on every
                    // client (#6052, #6604, Core).
                    SettingsPageLink(EarlyFeaturesSection.title, systemImage: "sparkles") { EarlyFeaturesSection(said: model.partsMaturity) }
                }
                .paperListRow()
                // Agents ▸ Policy is Core (#2957, FR-027): every rule and where it comes from,
                // changed here as on the Mac (#3344).
                Section(PolicyLayersWords.groupTitle) {
                    NavigationLink {
                        PolicyPane(host: model.policyHost, revision: model.policyRevision)
                            .staleBanner()
                            .navigationTitle(PolicyLayersWords.title)
                            .navigationBarTitleDisplayMode(.inline)
                    } label: {
                        Label(PolicyLayersWords.title, icon: PolicyLayersWords.symbol)
                    }
                }
                .paperListRow()
                // A machine's image hosts (#7140), as the Mac's Settings ▸ Machines ▸ Image Hosts: a person
                // who can allow a host on a page here can remove it here.
                if !model.imageHostMachines.isEmpty {
                    Section("Machines") {
                        NavigationLink {
                            RemoteImageHostsPage()
                                .staleBanner()
                                .navigationTitle(ImageHostsWords.title)
                                .navigationBarTitleDisplayMode(.inline)
                        } label: {
                            Label(ImageHostsWords.title, icon: ImageHostsWords.symbol)
                        }
                    }
                    .paperListRow()
                }
                // The daily checks are Core (#3355, #5853): their one control, as the Mac's and the
                // web's Agent Configuration ▸ Workflows.
                Section("Agent Configuration") {
                    SettingsPageLink("Workflows", systemImage: "clock.arrow.circlepath") { DailyChecksSettingsSection() }
                }
                .paperListRow()
                // The docs site (#1327), as the Mac's Help ▸ Agents Help.
                Section {
                    Link(destination: HelpSite.url) {
                        Label("Help", icon: "questionmark.circle")
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .background(Paper.ground)
            // A sheet over the page's banner, so it says it too (#5147).
            .staleBanner()
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
        }
    }
}

/// Status ▸ Secrets (#5257) and Status ▸ Accounts (#5258), as the window's: a page each, with no Reveal or Copy
/// (tier Mac + web, #5112).
struct RemoteStatusSecretsPage: View {
    let kind: StatusSecretsKind
    @Environment(RemoteModel.self) private var model

    var body: some View {
        @Bindable var model = model
        let names = SecretInfoWords.names(model.controlHosts)
        if kind == .secrets {
            SecretsPageView(control: model.controlPlane, state: model.userScope, source: model,
                            machineNames: names, hostName: "the Mac",
                            selection: $model.secretsSelection, agentTitle: { model.work.agent($0)?.title },
                            openAccount: { account in
                                model.openStatusPage(.accountsStatus, from: \.secretsSelection, select: \.accountsSelection, account.id)
                            })
                .navigationTitle(SecretsPage.title)
                .navigationBarTitleDisplayMode(.inline)
        } else {
            AccountsPageView(control: model.controlPlane, state: model.userScope, machineNames: names,
                             selection: $model.accountsSelection,
                             openSecret: { name in
                                 model.openStatusPage(.secretsStatus, from: \.accountsSelection, select: \.secretsSelection, "yours/\(name)")
                             })
                .navigationTitle(AccountsPage.title)
                .navigationBarTitleDisplayMode(.inline)
        }
    }
}

extension RemoteModel {
    /// From one Status page's pushed detail to an item on the other (#5258): the detail goes back first, and the other
    /// page opens a moment later with its item selected. On iPhone, a page changed under a pushed detail drops back to
    /// the sidebar.
    func openStatusPage(_ page: SidebarItem, from: ReferenceWritableKeyPath<RemoteModel, String?>,
                        select: ReferenceWritableKeyPath<RemoteModel, String?>, _ id: String) {
        self[keyPath: from] = nil
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(400))
            self[keyPath: select] = id
            sidebarItem = page
        }
    }
}

/// Settings ▸ Image Hosts on the Remote (#7140): the shared pane, with a menu of the machines that keep the list.
struct RemoteImageHostsPage: View {
    @Environment(RemoteModel.self) private var model

    var body: some View {
        let machines = model.imageHostMachines
        ImageHostsPane(model: model, machines: machines) { machine, choose in
            Picker("Machine", selection: Binding(get: { machine.id }, set: { choose($0) })) {
                ForEach(machines, id: \.id) { Text(verbatim: $0.name).tag($0.id) }
            }
            .labelsHidden()
            .pickerStyle(.menu)
            .disabled(machines.count < 2)
        }
    }
}
