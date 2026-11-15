import AgentsKitCore
import SwiftUI

/// Status ▸ Secrets (#5257) and Status ▸ Accounts (#5258): every secret, or every account, of the person's on one page,
/// with the selected one's detail beside the list (`SecretsPageView`, `AccountsPageView`). The Remote and the web page
/// draw the same.
struct StatusSecretsView: View {
    let kind: StatusSecretsKind
    @Environment(AppModel.self) private var model
    /// Reveal and Copy (#5115, #5116), opened with this window's own key.
    @State private var show = SecretShowState { ControlConfig.sealing() }

    var body: some View {
        @Bindable var model = model
        let names = SecretInfoWords.names(model.controlPlaneHosts ?? [], thisMac: model.thisMacHostID)
        // Through the window's own connection, which hears when they change (#1760).
        if kind == .secrets {
            SecretsPageView(control: model.controlRequests, state: model.userScope, source: model,
                            machineNames: names, show: show, selection: $model.secretsSelection,
                            agentTitle: { id in model.work.agent(id)?.title },
                            openAccount: { model.showAccounts(item: $0.id) })
                .navigationTitle(SecretsPage.title)
        } else {
            AccountsPageView(control: model.controlRequests, state: model.userScope, machineNames: names,
                             show: ControlConfig.client == nil ? nil : show, selection: $model.accountsSelection,
                             openSecret: { model.showSecrets(item: "yours/\($0)") })
                .navigationTitle(AccountsPage.title)
        }
    }
}
