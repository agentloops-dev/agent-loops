import AgentsKitCore
import SwiftUI

/// Status ▸ Secrets (#5257): every secret of the person's on one page, with its detail beside the list
/// (`SecretsPageView`). Status ▸ Accounts (#4161) is still the list you only read, whose press opens
/// Settings ▸ You ▸ Accounts, until its own page (#5258). The Remote and the web page draw the same.
struct StatusSecretsView: View {
    let kind: StatusSecretsKind
    @Environment(AppModel.self) private var model
    @Environment(\.openSettings) private var openSettings
    /// Reveal and Copy (#5115), opened with this window's own key.
    @State private var show = SecretShowState { ControlConfig.sealing() }

    var body: some View {
        @Bindable var model = model
        if kind == .secrets {
            // Through the window's own connection, which hears when they change (#1760).
            SecretsPageView(control: model.controlRequests, state: model.userScope, source: model,
                            machineNames: SecretInfoWords.names(model.controlPlaneHosts ?? [], thisMac: model.thisMacHostID),
                            show: show, selection: $model.secretsSelection,
                            agentTitle: { id in model.work.agent(id)?.title },
                            openAccount: { _ in model.statusSecretsPage = .accounts })
                .navigationTitle(SecretsPage.title)
        } else {
            StatusSecretsPage(kind: kind, control: model.controlRequests, state: model.userScope) { _ in
                model.settingsPaneAsked = .accounts
                openSettings()
            }
            .frame(maxWidth: 640)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .navigationTitle(kind.title)
        }
    }
}
