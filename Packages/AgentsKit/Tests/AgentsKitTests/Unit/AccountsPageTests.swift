@testable import AgentsKitCore
import Foundation
import Testing

/// The Accounts page (#5258): the same rules as `Web/src/model/accountsPage.ts`.
struct AccountsPageTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func day(in days: Int) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "UTC")
        f.dateFormat = "yyyy-MM-dd"
        return f.string(from: now.addingTimeInterval(Double(days) * 86_400))
    }

    private func secret(_ name: String, machines: [String] = ["m1", "m2"], backedUp: Bool? = true) -> DaemonAPI.UserSecretRow {
        DaemonAPI.UserSecretRow(name: name, updated: "2027-01-01T00:00:00Z", machines: machines, hint: "ab12", backedUp: backedUp)
    }

    private var sample: [AccountsPage.Item] {
        AccountsPage.items([
            DaemonAPI.OutsideAccount(service: "GitHub", description: "Pushes, PRs and Actions", address: "alex@example.com", plan: "Team",
                                     monthlyCostUsd: 4, secret: "GITHUB_TOKEN", projects: ["Agents", "agents-web"],
                                     lastOp: .init(what: "read", at: now.addingTimeInterval(-7200)), username: "alexec"),
            DaemonAPI.OutsideAccount(service: "npm", description: "Publish to npm", address: "agentloops", monthlyCostUsd: 0,
                                     secret: "NPM_TOKEN", askBeforeUse: false,
                                     lastOp: .init(what: "read", at: now.addingTimeInterval(-5 * 86_400))),
            DaemonAPI.OutsideAccount(service: "Cloudflare", address: "agentloops@example.com", secret: "CF_API_TOKEN",
                                     tokenExpires: day(in: 6)),
            DaemonAPI.OutsideAccount(service: "Google Cloud", address: "agentloops@example.com", monthlyCostUsd: 12, secret: "GCP_KEY",
                                     projects: ["Agents"]),
            DaemonAPI.OutsideAccount(service: "GitHub", address: "bot@example.com", secret: "BOT_TOKEN", askBeforeUse: false),
        ], secrets: [secret("GITHUB_TOKEN"), secret("NPM_TOKEN"), secret("CF_API_TOKEN"), secret("GCP_KEY", machines: ["m2"], backedUp: false)],
        machineNames: ["m1": "mac-studio", "m2": "devbox"], now: now)
    }

    @Test func needsYouIsOnTopWhateverTheSort() throws {
        for sort in AccountsPage.Sort.allCases {
            let groups = AccountsPage.groups(sample, search: "", chip: .all, project: nil, service: nil, sort: sort)
            #expect(groups.first?.id == "needsYou")
            #expect(Set(try #require(groups.first).items.map(\.account.service)) == ["Cloudflare", "Google Cloud"])
            // An item in Needs you is not in the rest as well.
            #expect(groups.flatMap(\.items).count == sample.count)
        }
    }

    @Test func needsYouSaysWhy() {
        let byService = Dictionary(uniqueKeysWithValues: sample.map { ($0.account.service + ($0.account.address ?? ""), $0) })
        #expect(byService["Cloudflareagentloops@example.com"]?.needs == "Its token expires on \(day(in: 6)).")
        #expect(byService["Google Cloudagentloops@example.com"]?.needs == "Its key, GCP_KEY, is only on devbox. Not backed up.")
        #expect(byService["GitHuballex@example.com"]?.needs == nil)
    }

    @Test func oneMachineForEverySecretIsNotEachAccountsWarning() {
        let items = AccountsPage.items([DaemonAPI.OutsideAccount(service: "AWS", secret: "AWS_KEY")],
                                       secrets: [secret("AWS_KEY", machines: ["m1"], backedUp: false)], now: now)
        #expect(items.first?.needs == nil)
    }

    @Test func searchMatchesEachFieldAndTheSecretsName() {
        let found = { (search: String) in
            AccountsPage.found(sample, search: search, project: nil, service: nil).map { AccountsPage.name($0.account) }
        }
        #expect(found("alexec") == ["GitHub · alexec"])
        #expect(found("team") == ["GitHub · alexec"])
        #expect(found("publish") == ["npm · agentloops"])
        #expect(found("gcp_key") == ["Google Cloud · agentloops@example.com"])
        #expect(found("agents-web") == ["GitHub · alexec"])
        // Every word, in any field.
        #expect(found("github bot") == ["GitHub · bot@example.com"])
        #expect(found("stripe").isEmpty)
    }

    @Test func chipsAndMenusAddTogether() {
        let counts = AccountsPage.counts(sample, search: "", project: nil, service: nil)
        #expect(counts[.all] == 5)
        #expect(counts[.needsYou] == 2)
        #expect(counts[.asks] == 3)
        // An account that names no project is for every project.
        let agents = AccountsPage.counts(sample, search: "", project: "agents", service: nil)
        #expect(agents[.all] == 5)
        let web = AccountsPage.counts(sample, search: "", project: "agents-web", service: nil)
        #expect(web[.all] == 4)
        let github = AccountsPage.counts(sample, search: "", project: nil, service: "github")
        #expect(github[.all] == 2)
        #expect(github[.asks] == 1)
        #expect(AccountsPage.services(sample) == ["Cloudflare", "GitHub", "Google Cloud", "npm"])
    }

    @Test func sorts() {
        let names = { (sort: AccountsPage.Sort) in AccountsPage.sorted(sample, by: sort).map { AccountsPage.name($0.account) } }
        #expect(names(.service) == ["Cloudflare · agentloops@example.com", "GitHub · alexec", "GitHub · bot@example.com",
                                    "Google Cloud · agentloops@example.com", "npm · agentloops"])
        #expect(names(.lastUsed).prefix(2) == ["GitHub · alexec", "npm · agentloops"])
        #expect(names(.cost).prefix(3) == ["Google Cloud · agentloops@example.com", "GitHub · alexec", "npm · agentloops"])
    }

    @Test func footSaysTheTotalOfWhatShows() {
        #expect(AccountsPage.foot(shown: sample, of: 5) == "5 accounts · $16 a month")
        let github = AccountsPage.found(sample, search: "github", project: nil, service: nil)
        #expect(AccountsPage.foot(shown: github, of: 5) == "2 of 5 · $4 a month")
    }

    @Test func words() throws {
        let github = try #require(sample.first)
        #expect(AccountsPage.shortCost(github.account) == "$4/mo")
        #expect(AccountsPage.shortCost(sample[1].account) == "free")
        #expect(AccountsPage.planLine(github.account) == "Team · $4 a month")
        #expect(AccountsPage.subtitle(github) == "Pushes, PRs and Actions")
        #expect(AccountsPage.noMatch("stripe live") == "No account matches “stripe live”.")
    }
}
