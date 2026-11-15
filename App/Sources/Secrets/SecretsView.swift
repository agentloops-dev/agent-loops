import AgentsKitCore
import SwiftUI

/// Settings ▸ You ▸ Secrets (#5257): one line, for people who look in Settings. The secrets have a page
/// of their own, Status ▸ Secrets, where they find, see, add, edit and remove them. The web page and the
/// Remote say the same.
struct SecretsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Form {
            Section {
                HStack {
                    Text(SecretsPage.settingsLine).fixedSize(horizontal: false, vertical: true)
                    Spacer()
                    Button(SecretsPage.openSecrets) { model.showSecrets() }
                }
            }
            .paperListRow()
        }
        .paperForm()
        .frame(maxWidth: 560)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .navigationTitle(UserScopeWords.secretsTitle)
    }
}
