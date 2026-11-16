import AgentsKitCore
import SwiftUI

/// Which of the Status rows (#4161): each opens its page, Secrets (#5257), Accounts (#5258) or Environment (#5260).
enum StatusSecretsKind: Hashable {
    case secrets, accounts, environment

    var title: String {
        switch self {
        case .secrets: StatusSecretsWords.secretsTitle
        case .accounts: StatusSecretsWords.accountsTitle
        case .environment: EnvironmentPage.title
        }
    }

    var help: String {
        switch self {
        case .secrets: StatusSecretsWords.secretsHelp
        case .accounts: StatusSecretsWords.accountsHelp
        case .environment: EnvironmentPage.whereLine
        }
    }

    var systemImage: String {
        switch self {
        case .secrets: "key"
        case .accounts: "person.crop.rectangle.stack"
        case .environment: "terminal"
        }
    }

    /// Its sidebar row.
    var sidebarItem: SidebarItem {
        switch self {
        case .secrets: .secretsStatus
        case .accounts: .accountsStatus
        case .environment: .environmentStatus
        }
    }
}

extension UserScopeState {
    /// The person's outside accounts, from their settings.
    var accounts: [DaemonAPI.OutsideAccount] {
        (try? settings?.accounts?.decode(DaemonAPI.OutsideAccounts.self))?.accounts ?? []
    }

    /// A Status list's lines (#4161), newest operation first.
    func statusLines(_ kind: StatusSecretsKind) -> [StatusSecretsWords.Line] {
        let secrets = secrets?.secrets ?? []
        return switch kind {
        case .secrets: StatusSecretsWords.secretLines(secrets)
        case .accounts: StatusSecretsWords.accountLines(accounts, secrets: secrets)
        case .environment: []
        }
    }
}

/// The sidebar's Status ▸ Secrets and Status ▸ Accounts rows (#4161): the title, and at its end the
/// age of the newest operation on any of them. Under it, in grey, the secret or account that operation was on, the
/// operation when it is not a read, and the session that did it, when it is less than 4 hours old (#4931). The icon
/// keeps its plain tint: an operation is not a warning.
struct StatusSecretsRow: View {
    let kind: StatusSecretsKind
    let state: UserScopeState
    /// A session's title by its id, or nil when this client does not know it.
    var agentTitle: (UUID) -> String? = { _ in nil }

    var body: some View {
        let lines = state.statusLines(kind)
        let newest = StatusSecretsWords.newest(lines)
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let read = StatusSecretsWords.recentOperation(lines, now: context.date, agentTitle: agentTitle)
            VStack(alignment: .leading, spacing: 1) {
                HStack(alignment: .firstTextBaseline) {
                    Text(kind.title).foregroundStyle(.primary)
                    Spacer()
                    if let newest {
                        Text(StatusSecretsWords.ago(newest, now: context.date))
                            .foregroundStyle(.secondary).monospacedDigit()
                    }
                }
                if let read {
                    Text(read).appText(.fine).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                }
            }
            .accessibilityValue(read.map { "\(StatusSecretsWords.lastLabel) \($0)" } ?? "")
        }
        .help(kind.help)
    }
}
