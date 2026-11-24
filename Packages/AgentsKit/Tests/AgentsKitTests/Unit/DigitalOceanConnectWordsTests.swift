import Foundation
import Testing
@testable import AgentsKitCore

/// The guided DigitalOcean Connect's words (#6074): the same as the web page's
/// (Web/test/digitalOceanConnect.test.mjs checks those).
@Suite("DigitalOcean Connect words")
struct DigitalOceanConnectWordsTests {
    typealias Words = DigitalOceanConnectWords

    @Test("each refusal says what happened and offers its one fix")
    func refusals() {
        #expect(Words.refusal(reason: "denied", words: "x").text == "DigitalOcean did not connect: you chose Deny.")
        #expect(Words.refusal(reason: "denied", words: "x").fix == .connect)
        #expect(Words.refusal(reason: "account_inactive", words: "x").fix == .payment)
        #expect(Words.refusal(reason: "disconnected", words: "x").text == Words.disconnected)
        #expect(Words.refusal(reason: nil, words: "The machine is offline.").text == "The machine is offline.")
    }

    @Test("the card's account line names the account and the team")
    func accountLine() {
        #expect(Words.accountLine(.init(account: "a@example.com", team: "Personal")) == "a@example.com · team “Personal”")
        #expect(Words.accountLine(.init(account: "a@example.com")) == "a@example.com")
        #expect(Words.accountLine(.init()) == nil)
    }

    @Test("an OAuth provider is a digitalocean-api one with oauth")
    func isOAuth() {
        var provider = DaemonAPI.ProviderSummary(id: ProviderID(rawValue: "p1"), name: "DigitalOcean", kind: "commands", preset: "digitalocean-api")
        #expect(!Words.isOAuth(provider))
        provider.oauth = .init(account: "a@example.com")
        #expect(Words.isOAuth(provider))
    }

    @Test("the hand-back page's address gives the code and the state, or Deny's error alone")
    func callback() throws {
        let code = try #require(DaemonAPI.ProviderOAuthFinish(callback: URL(string: "agents://oauth/callback?code=c1&state=s1")!))
        #expect(code == .init(state: "s1", code: "c1"))
        let deny = try #require(DaemonAPI.ProviderOAuthFinish(callback: URL(string: "agents://oauth/callback?error=access_denied")!))
        #expect(deny == .init(error: "access_denied"))
        #expect(DaemonAPI.ProviderOAuthFinish(callback: URL(string: "agents://oauth/callback")!) == nil)
    }
}
