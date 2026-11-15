import AgentsKitCore
import SwiftUI

/// Settings ▸ You ▸ Accounts (#5258): one line, for people who look in Settings. The accounts have a page of their own,
/// Status ▸ Accounts, where they find, see, add, edit and remove them. The web page and the Remote say the same.
struct AccountsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Form {
            Section {
                HStack {
                    Text(AccountsPage.settingsLine).fixedSize(horizontal: false, vertical: true)
                    Spacer()
                    Button(AccountsPage.openAccounts) { model.showAccounts() }
                }
            }
            .paperListRow()
        }
        .paperForm()
        .frame(maxWidth: 560)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .navigationTitle(AccountsWords.title)
    }
}
