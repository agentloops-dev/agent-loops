import AgentsKitCore
import SwiftUI

/// Settings (#846): You first, as on the Mac (#3998): a line that opens Status ▸ Secrets (#5257), and Accounts,
/// which are Core (#4032, #4033); this app alone (Voice; Live Activities, which only a phone or an iPad has
/// (#1657); and Sidebar, kept on the Mac's host and the same on every client: #1033, #2079), and
/// Help. Control plane, machine, user and agent configuration settings are Mac + web (#2796,
/// #2798): the Remote does not have them, but for Agents ▸ Policy (#2957) and the daily
/// summary's control (#3351), which are Core.
struct RemoteSettings: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(RemoteModel.self) private var model

    var body: some View {
        NavigationStack {
            Form {
                // The person's own, on every machine of theirs (#3998), as the Mac's Settings ▸ You.
                Section {
                    // The secrets have a page of their own, Status ▸ Secrets (#5257): one line here.
                    Button {
                        model.sidebarItem = .secretsStatus
                        dismiss()
                    } label: {
                        Label(SecretsPage.openSecrets, icon: "key")
                    }
                    NavigationLink {
                        RemoteAccountsPage()
                    } label: {
                        Label(AccountsWords.title, icon: "person.crop.rectangle.stack")
                    }
                } header: {
                    Text(SettingsScope.you.title)
                } footer: {
                    Text(SecretsPage.settingsLine)
                }
                .paperListRow()
                Section(SettingsScope.thisApp.title) {
                    SettingsPageLink("Voice", systemImage: "waveform") { VoiceSettingsSection() }
                    // The notice limits are rules of Agents ▸ Policy now (#2957, FR-023).
                    SettingsPageLink("Notifications", systemImage: "bell") { LiveActivitySettingsSection() }
                    // Kept on the Mac's host, the same on every client (#1033, #2079). The archive
                    // switch is a rule of Agents ▸ Policy now (FR-023).
                    SettingsPageLink("Sidebar", systemImage: "sidebar.left") { SidebarSettingsSection() }
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
                // The daily summary is Core (#3351): its one control, as the Mac's and the web's
                // Agent Configuration ▸ Workflows.
                Section("Agent Configuration") {
                    SettingsPageLink("Workflows", systemImage: "clock.arrow.circlepath") { DailySummarySettingsSection() }
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

/// Settings ▸ You ▸ Accounts (#4033): the person's outside accounts through the control plane, as
/// the window's `AccountsView`.
private struct RemoteAccountsPage: View {
    @Environment(RemoteModel.self) private var model

    var body: some View {
        AccountsPane(control: model.controlPlane, state: model.userScope,
                     machineNames: SecretInfoWords.names(model.controlHosts))
            .staleBanner()
            .navigationTitle(AccountsWords.title)
            .navigationBarTitleDisplayMode(.inline)
    }
}

/// Status ▸ Secrets (#5257) and Status ▸ Accounts (#4161), as the window's. Accounts is still the list you
/// only read, whose tap opens Settings ▸ You ▸ Accounts over it, until its own page (#5258).
struct RemoteStatusSecretsPage: View {
    let kind: StatusSecretsKind
    @Environment(RemoteModel.self) private var model
    @State private var editing = false
    @State private var selection: String?

    var body: some View {
        if kind == .secrets {
            // Status ▸ Secrets (#5257): the page, as the window's; no Reveal or Copy (tier Mac + web, #5112).
            SecretsPageView(control: model.controlPlane, state: model.userScope, source: model,
                            machineNames: SecretInfoWords.names(model.controlHosts), hostName: "the Mac",
                            selection: $selection, agentTitle: { model.work.agent($0)?.title },
                            openAccount: { _ in model.sidebarItem = .accountsStatus })
                .navigationTitle(SecretsPage.title)
                .navigationBarTitleDisplayMode(.inline)
        } else {
            accounts
        }
    }

    private var accounts: some View {
        StatusSecretsPage(kind: kind, control: model.controlPlane, state: model.userScope) { _ in editing = true }
            .navigationTitle(kind.title)
            .navigationBarTitleDisplayMode(.inline)
            .sheet(isPresented: $editing) {
                NavigationStack {
                    RemoteAccountsPage()
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) { Button("Done") { editing = false } }
                    }
                }
            }
    }
}
