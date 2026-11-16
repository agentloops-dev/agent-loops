@testable import AgentsKitCore
import Foundation
import Testing

/// The Environment page (#5260): the same rules as `Web/src/model/environmentPage.ts`, and the file's edit the same as
/// `Go/internal/envfile` (`TestEditKeepsOtherLines`).
struct EnvironmentPageTests {
    @Test func editKeepsOtherLines() {
        let text = "# Top comment\n\n# The region\nREGION=eu\nOTHER=1 # trailing\n"
        #expect(EnvFileText.edit(text, name: "REGION", value: "us", description: nil)
            == "# Top comment\n\n# The region\nREGION=us\nOTHER=1 # trailing\n")
        #expect(EnvFileText.edit(text, name: "REGION", value: "us", description: "Where we run")
            == "# Top comment\n\n# Where we run\nREGION=us\nOTHER=1 # trailing\n")
        #expect(EnvFileText.edit(text, name: "REGION", value: nil, description: nil) == "# Top comment\n\nOTHER=1 # trailing\n")
        #expect(EnvFileText.edit(text, name: "NEW", value: "a b", description: "A new one") == text + "\n# A new one\nNEW='a b'\n")
        #expect(EnvFileText.edit("ONLY=1\n", name: "ONLY", value: nil, description: nil) == "")
        #expect(EnvFileText.edit("", name: "A", value: "1", description: nil) == "A=1\n")
        #expect(EnvFileText.edit("A=1\nB=2\nA=3\n", name: "A", value: "4", description: nil) == "B=2\nA=4\n")
        #expect(EnvFileText.edit("export A=\"x\"\n", name: "A", value: "y", description: "") == "A=y\n")
    }

    @Test func textOfVariables() {
        let text = EnvFileText.text(of: [EnvVariable(name: "B", value: "2", line: 4),
                                         EnvVariable(name: "A", value: "one two", description: "First", line: 2)])
        #expect(text == "# First\nA='one two'\n\nB=2\n")
        #expect(EnvFileText.text(of: []) == "")
    }

    @Test func refusals() {
        #expect(EnvironmentPage.refusal(name: "AWS_REGION", value: "eu-west-2") == nil)
        #expect(EnvironmentPage.refusal(name: "1BAD", value: "x") != nil)
        #expect(EnvironmentPage.refusal(name: "GITHUB_TOKEN", value: "abc") == EnvironmentPage.looksSecret)
        #expect(EnvironmentPage.refusedAsSecret(EnvironmentPage.looksSecret))
        #expect(EnvironmentPage.refusedAsSecret("The value of X looks like a token."))
        #expect(!EnvironmentPage.refusedAsSecret("The control plane can’t be reached."))
    }

    private var files: [EnvFile] {
        [
            EnvFile(layer: .yours, path: "/Users/me/.agents/env", variables: [
                EnvVariable(name: "AWS_REGION", value: "us-east-1", line: 1),
                EnvVariable(name: "EDITOR", value: "code --wait", line: 2),
            ]),
            EnvFile(layer: .repo, path: "/src/Agents/.agents/env", folder: "/src/Agents", variables: [
                EnvVariable(name: "AWS_REGION", value: "eu-west-2", description: "The region for the deploy scripts.", line: 2),
                EnvVariable(name: "GOFLAGS", value: "-mod=mod", line: 3),
            ]),
            EnvFile(layer: .repo, path: "/src/shop/.agents/env", folder: "/src/shop", variables: [
                EnvVariable(name: "NODE_OPTIONS", value: "--require x", line: 1, state: .refused),
                EnvVariable(name: "HTTP_PROXY", value: "http://p", line: 2, state: .policyBlocked),
                EnvVariable(name: "PORT", value: "8081", line: 3),
            ], uncommitted: true),
        ]
    }

    @Test func groupsPutNeedsYouOnTop() throws {
        let groups = EnvironmentPage.groups(files, search: "", chip: .all)
        try #require(groups.map(\.title) == ["Needs you", "Yours", "Repo · Agents", "Repo · shop"])
        let needs = groups[0].items // index-ok: the #require above says there are four groups
        try #require(needs.map(\.name) == ["NODE_OPTIONS", "HTTP_PROXY"])
        #expect(needs.map(\.needs) == ["A repo cannot set this. Not given.", "Blocked by your organisation’s policy."])
        let shop = groups[3] // index-ok: the #require above says there are four groups
        #expect(shop.uncommitted)
        #expect(shop.items.map(\.name) == ["PORT"])
        #expect(EnvironmentPage.groups(files, search: "", chip: .needsYou).map(\.title) == ["Needs you"])
        #expect(EnvironmentPage.groups(files, search: "", chip: .yours).map(\.title) == ["Yours"])
    }

    @Test func searchMatchesNameValueAndDescription() {
        let counts = EnvironmentPage.counts(EnvironmentPage.items(files), search: "deploy")
        #expect(counts[.all] == 1)
        #expect(EnvironmentPage.counts(EnvironmentPage.items(files), search: "8081")[.repos] == 1)
        #expect(EnvironmentPage.counts(EnvironmentPage.items(files), search: "editor")[.yours] == 1)
        #expect(EnvironmentPage.counts(EnvironmentPage.items(files), search: "")[.all] == 7)
    }

    @Test func alsoSetSaysYoursWins() {
        let items = EnvironmentPage.items(files)
        let repo = items.first { $0.id == "repo//src/Agents/AWS_REGION" }!
        #expect(EnvironmentPage.alsoSet(repo, in: items) == ["Yours: us-east-1", "Yours wins: sessions get us-east-1"])
        let yours = items.first { $0.id == "yours/AWS_REGION" }!
        #expect(EnvironmentPage.alsoSet(yours, in: items) == ["Repo · Agents: eu-west-2", "Yours wins over a repo’s"])
    }

    @Test func secretLookingWordsByLayer() {
        let looking = EnvVariable(name: "X", value: "y", line: 1, state: .secretLooking)
        #expect(EnvironmentPage.needs(looking, layer: .repo) == "This is in the repo, so it is public. Revoke it, then add it to Secrets.")
        #expect(EnvironmentPage.needs(looking, layer: .yours) == "This looks like a secret. Move it to Secrets.")
    }

    @Test func resolvedSource() {
        let wins = EnvResolvedVariable(name: "AWS_REGION", value: "us-east-1", layer: .yours, over: [.repo])
        #expect(EnvironmentPage.resolvedSource(wins, repo: "agents-web") == "Yours (wins over the repo)")
        let repo = EnvResolvedVariable(name: "PORT", value: "8081", layer: .repo)
        #expect(EnvironmentPage.resolvedSource(repo, repo: "agents-web") == "Repo agents-web")
        let refused = EnvResolvedVariable(name: "NODE_OPTIONS", state: .refused, reason: "A repo cannot set this")
        #expect(EnvironmentPage.resolvedSource(refused, repo: "agents-web") == "A repo cannot set this")
    }
}
