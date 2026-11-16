import Foundation

/// The ⌘K command menu's secrets, accounts and environment variables (#5261, epic #5254,
/// `specs/5254-secrets-accounts-environment/wireframes.md` section 7): each found by its name, and opened on its page
/// (Status ▸ Secrets, Accounts or Environment) with it selected. A row says the kind and the scope, never a value: an
/// item holds none, so the menu can match none. The web page's rules are the same, in `Web/src/model/commandSetup.ts`.
///
/// The menu lists what the client keeps without asking a machine: the person's own secrets (Yours) and accounts, and the
/// variables of the files of one machine (Yours and each repo's), read when the menu opens. A secret for one session or
/// on one machine is found on the Secrets page's search, which reads them from the machine.
public enum CommandMenuSetup {
    /// Which page an item opens, and the page's id of the thing it selects there.
    public enum Page: String, Sendable, Hashable {
        case secrets, accounts, environment
    }

    /// The person's own secrets: `secret:yours/NAME`, "Secret · Yours" (or "Card · Yours").
    public static func secrets(_ rows: [DaemonAPI.UserSecretRow]) -> [CommandFinder.Item] {
        rows.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }.map { row in
            CommandFinder.Item(id: "secret:yours/\(row.name)", kind: .secret, title: row.name,
                               detail: "\(row.isCard ? "Card" : "Secret") · \(SecretsPage.Chip.yours.title)")
        }
    }

    /// The person's accounts: `account:<id>`, "GitHub · alexec", "Account". Its service and address find it too.
    public static func accounts(_ accounts: [DaemonAPI.OutsideAccount]) -> [CommandFinder.Item] {
        accounts.sorted { AccountsPage.name($0).localizedStandardCompare(AccountsPage.name($1)) == .orderedAscending }
            .map { account in
                CommandFinder.Item(id: "account:\(account.id)", kind: .account, title: AccountsPage.name(account),
                                   detail: CommandFinder.Kind.account.word,
                                   keywords: [account.service, account.address ?? ""].filter { !$0.isEmpty })
            }
    }

    /// The variables of a machine's files, Yours first: `variable:yours/NAME` or `variable:repo/<folder>/NAME`,
    /// "Environment · Yours" or "Environment · <repo>". `yoursText` is the person's `~/.agents/env` from their settings,
    /// for Yours when the machine read no Yours file or an empty one (a machine with no personal home has none).
    public static func variables(_ files: [EnvFile], yoursText: String? = nil) -> [CommandFinder.Item] {
        var files = files
        if !files.contains(where: { $0.layer == .yours && !$0.variables.isEmpty }), let yoursText {
            files.removeAll { $0.layer == .yours }
            files.insert(EnvFile(layer: .yours, path: "~/.agents/env", variables: names(yoursText)), at: 0)
        }
        let yours = files.filter { $0.layer == .yours }
        let repos = files.filter { $0.layer == .repo }
            .sorted { EnvironmentPage.repoName($0.folder ?? $0.path).localizedStandardCompare(EnvironmentPage.repoName($1.folder ?? $1.path)) == .orderedAscending }
        return (yours + repos).flatMap { file in
            let scope = file.layer == .yours ? SecretsPage.Chip.yours.title : EnvironmentPage.repoName(file.folder ?? file.path)
            return file.variables.sorted { $0.name < $1.name }.map { variable in
                CommandFinder.Item(id: "variable:\(EnvironmentPage.id(layer: file.layer, folder: file.folder, name: variable.name))",
                                   kind: .variable, title: variable.name, detail: "\(EnvironmentPage.title) · \(scope)")
            }
        }
    }

    /// All three, in the menu's order: the secrets, the accounts, then the variables.
    public static func items(secrets rows: [DaemonAPI.UserSecretRow], accounts list: [DaemonAPI.OutsideAccount],
                             files: [EnvFile], yoursText: String? = nil) -> [CommandFinder.Item] {
        secrets(rows) + accounts(list) + variables(files, yoursText: yoursText)
    }

    /// The page an item opens, and the id it selects there (`yours/NAME`, an account's id, `repo/<folder>/NAME`); nil
    /// for an item that is not one of these.
    public static func place(of item: CommandFinder.Item) -> (page: Page, id: String)? {
        guard let colon = item.id.firstIndex(of: ":") else { return nil }
        let rest = String(item.id[item.id.index(after: colon)...])
        switch item.id[..<colon] {
        case "secret": return (.secrets, rest)
        case "account": return (.accounts, rest)
        case "variable": return (.environment, rest)
        default: return nil
        }
    }

    /// The names in a `~/.agents/env` text, with no values: the menu has no use for one.
    static func names(_ text: String) -> [EnvVariable] {
        text.components(separatedBy: .newlines).enumerated().compactMap { index, line in
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.hasPrefix("#"), let pair = EnvFileText.split(trimmed) else { return nil }
            return EnvVariable(name: pair.name, value: "", line: index + 1)
        }
    }
}
