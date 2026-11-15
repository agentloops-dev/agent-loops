@testable import AgentsKitCore
import Foundation
import Testing

/// The Secrets page (#5257): the same rules as `Web/src/model/secretsPage.ts`.
struct SecretsPageTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let agent = UUID(uuidString: "11111111-2222-3333-4444-555555555555")!

    private func iso(daysAgo: Double) -> String {
        ISO8601DateFormatter().string(from: now.addingTimeInterval(-daysAgo * 86_400))
    }

    private func secret(_ name: String, description: String? = nil, machines: [String] = ["m1", "m2"], readDaysAgo: Double? = nil,
                        createdDaysAgo: Double = 1, backedUp: Bool? = true, waiting: [String]? = nil) -> DaemonAPI.UserSecretRow {
        DaemonAPI.UserSecretRow(name: name, updated: iso(daysAgo: createdDaysAgo), machines: machines, waiting: waiting,
                                description: description, created: iso(daysAgo: createdDaysAgo), hint: "ab12",
                                lastOp: readDaysAgo.map { .init(what: "read", at: now.addingTimeInterval(-$0 * 86_400)) },
                                backedUp: backedUp)
    }

    private var sample: [SecretsPage.Item] {
        SecretsPage.items(
            yours: [
                secret("GITHUB_TOKEN", description: "GitHub token for alexec", readDaysAgo: 0.1),
                secret("NPM_TOKEN", description: "Publish to npm", readDaysAgo: 5),
                secret("AWS_SECRET_KEY", machines: ["m2"], readDaysAgo: 12, backedUp: false),
                secret("OLD_KEY", readDaysAgo: 120, createdDaysAgo: 200),
            ],
            sessions: [SessionSecret(agentID: agent, title: "Fix the shop sign-in", name: "SHOP_ADMIN_PW",
                                     setAt: now.addingTimeInterval(-1200))],
            local: [LocalSecret(name: "CF_API_TOKEN", setAt: now.addingTimeInterval(-3 * 86_400), description: "Saved by an agent")],
            accounts: [DaemonAPI.OutsideAccount(service: "GitHub", address: "alex@example.com", secret: "GITHUB_TOKEN",
                                                projects: ["Agents", "agents-web"])],
            machineNames: ["m1": "mac-studio", "m2": "devbox"], hostName: "this Mac", now: now)
    }

    @Test func needsYouIsOnTopWhateverTheSort() throws {
        for sort in SecretsPage.Sort.allCases {
            let groups = SecretsPage.groups(sample, search: "", chip: .all, project: nil, sort: sort)
            try #require(groups.map(\.id) == ["needsYou", "yours", "session", "machine"])
            #expect(Set(groups[0].items.map(\.name)) == ["AWS_SECRET_KEY", "OLD_KEY"])
            #expect(!groups[1].items.contains { $0.name == "AWS_SECRET_KEY" })
        }
        let needs = sample.first { $0.name == "AWS_SECRET_KEY" }?.needs
        #expect(needs == "Only on devbox. Not backed up.")
        #expect(sample.first { $0.name == "OLD_KEY" }?.needs == "Not used in 90 days.")
    }

    @Test func lastUsedSortsNewestReadFirstAndNameSortsAlphabetically() {
        let used = SecretsPage.groups(sample, search: "", chip: .yours, project: nil, sort: .lastUsed)
        #expect(used.last?.items.map(\.name) == ["GITHUB_TOKEN", "NPM_TOKEN"])
        let named = SecretsPage.sorted(sample, by: .name).map(\.name)
        #expect(named.first == "AWS_SECRET_KEY")
        #expect(named.last == "SHOP_ADMIN_PW")
    }

    @Test func searchMatchesNameDescriptionSessionMachineProjectAndServiceButNeverAValue() {
        func names(_ search: String) -> Set<String> {
            Set(sample.filter { SecretsPage.matches($0, search: search) }.map(\.name))
        }
        #expect(names("npm") == ["NPM_TOKEN"])
        #expect(names("shop sign") == ["SHOP_ADMIN_PW"])
        #expect(names("devbox").contains("AWS_SECRET_KEY"))
        #expect(names("agents-web") == ["GITHUB_TOKEN"])
        #expect(names("github") == ["GITHUB_TOKEN"])
        // The hint is the end of a value: it is not searched.
        #expect(names("ab12").isEmpty)
    }

    @Test func chipsAndSearchAddTogetherAndEachChipCountsWhatTheSearchLeaves() {
        let counts = SecretsPage.counts(sample, search: "")
        #expect(counts[.all] == 6)
        #expect(counts[.yours] == 4)
        #expect(counts[.session] == 1)
        #expect(counts[.machine] == 1)
        #expect(counts[.needsYou] == 2)
        let found = SecretsPage.counts(sample, search: "token")
        #expect(found[.all] == 3)
        #expect(found[.machine] == 1)
        #expect(found[.needsYou] == 0)
        let groups = SecretsPage.groups(sample, search: "token", chip: .machine, project: nil, sort: .name)
        #expect(groups.map(\.id) == ["machine"])
    }

    @Test func aSecretsProjectsAreThoseOfTheAccountsThatUseIt() {
        #expect(SecretsPage.projects(sample) == ["Agents", "agents-web"])
        let groups = SecretsPage.groups(sample, search: "", chip: .all, project: "Agents", sort: .name)
        #expect(groups.flatMap(\.items).map(\.name) == ["GITHUB_TOKEN"])
        #expect(sample.first { $0.name == "GITHUB_TOKEN" }?.services == ["GitHub"])
    }

    @Test func noCopyForThisClientNeedsThePerson() {
        let items = SecretsPage.items(yours: [secret("A")], noCopy: ["A"], clientName: "this browser", now: now)
        #expect(items[0].needs == "No copy for this browser yet.")
    }

    @Test func detailWords() throws {
        let github = try #require(sample.first { $0.name == "GITHUB_TOKEN" })
        #expect(SecretsPage.whereLine(github) == "Yours, on 2 machines")
        #expect(SecretsPage.shortAge(github, now: now) == "2 h")
        #expect(SecretsPage.recentLine(github, now: now) == "2 h ago: read")
        let session = try #require(sample.first { $0.place == .session })
        #expect(SecretsPage.whereLine(session) == "For “Fix the shop sign-in”, on this Mac only")
        #expect(SecretsPage.subtitle(session) == "Fix the shop sign-in · this Mac")
        #expect(SecretsPage.rowHint(session) == "••••")
    }
}
