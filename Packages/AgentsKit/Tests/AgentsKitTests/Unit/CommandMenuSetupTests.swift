@testable import AgentsKitCore
import Testing

/// The ⌘K menu's secrets, accounts and environment variables (#5261): found by name, a row says the kind and the
/// scope, never a value, and each opens on its page.
struct CommandMenuSetupTests {
    private let secrets = [
        DaemonAPI.UserSecretRow(name: "GITHUB_TOKEN", updated: "", machines: [], hint: "ab12"),
        DaemonAPI.UserSecretRow(name: "VISA", updated: "", machines: [], kind: "card"),
    ]
    private let accounts = [DaemonAPI.OutsideAccount(service: "GitHub", address: "agents@example.com", username: "octo")]
    private let files = [
        EnvFile(layer: .yours, path: "~/.agents/env", variables: [EnvVariable(name: "EDITOR", value: "vim", line: 1)]),
        EnvFile(layer: .repo, path: "/src/Agents/.agents/env", folder: "/src/Agents",
                variables: [EnvVariable(name: "GITHUB_ORG", value: "secret-org-name", line: 2)]),
    ]

    private var items: [CommandFinder.Item] {
        CommandMenuSetup.items(secrets: secrets, accounts: accounts, files: files)
    }

    @Test func eachRowSaysItsKindAndScope() {
        let rows = items.map { "\($0.kind.word)|\($0.title)|\($0.detail)" }
        #expect(rows == [
            "Secret|GITHUB_TOKEN|Secret · Yours",
            "Secret|VISA|Card · Yours",
            "Account|GitHub · octo|Account",
            "Environment variable|EDITOR|Environment · Yours",
            "Environment variable|GITHUB_ORG|Environment · Agents",
        ])
    }

    @Test func aNameFindsThemAndAValueNever() {
        #expect(CommandFinder.matches(items, query: "github").map(\.title) == ["GITHUB_TOKEN", "GitHub · octo", "GITHUB_ORG"])
        #expect(CommandFinder.matches(items, query: "secret-org").isEmpty)
        #expect(CommandFinder.matches(items, query: "vim").isEmpty)
        #expect(CommandFinder.matches(items, query: "ab12").isEmpty)
        // An account's address finds it, as on its page.
        #expect(CommandFinder.matches(items, query: "agents@example").map(\.title) == ["GitHub · octo"])
    }

    @Test func eachOpensOnItsPageSelected() {
        let places = items.compactMap { CommandMenuSetup.place(of: $0) }.map { "\($0.page.rawValue) \($0.id)" }
        #expect(places == [
            "secrets yours/GITHUB_TOKEN",
            "secrets yours/VISA",
            "accounts \(accounts[0].id)",
            "environment yours/EDITOR",
            "environment repo//src/Agents/GITHUB_ORG",
        ])
        #expect(CommandMenuSetup.place(of: CommandFinder.Item(id: "session:1", kind: .session, title: "x")) == nil)
    }

    @Test func yoursFromTheSettingsWhenNoFileWasRead() {
        let found = CommandMenuSetup.variables([], yoursText: "# The editor\nEDITOR=vim\nexport PAGER='less -R'\n")
        #expect(found.map(\.id) == ["variable:yours/EDITOR", "variable:yours/PAGER"])
        // A file read wins over the settings' text; an empty one does not.
        #expect(CommandMenuSetup.variables(files, yoursText: "OTHER=1\n").map(\.title) == ["EDITOR", "GITHUB_ORG"])
        let empty = [EnvFile(layer: .yours, path: "", variables: [])]
        #expect(CommandMenuSetup.variables(empty, yoursText: "OTHER=1\n").map(\.id) == ["variable:yours/OTHER"])
    }
}
