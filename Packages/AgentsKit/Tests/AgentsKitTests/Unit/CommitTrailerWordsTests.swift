import Foundation
import Testing
import AgentsKitCore

/// The Commit trailers group of Agents ▸ Policy (#6373) and its feature flag.
@Suite("Commit trailers words")
struct CommitTrailerWordsTests {
    private let you = DaemonAPI.PolicyLayerRef(kind: "you", name: "You")
    private let folder = DaemonAPI.PolicyLayerRef(kind: "folder", name: "agent-loops", folder: "/src/agent-loops")

    private func trailer(_ key: String) throws -> CommitTrailerWords.Trailer {
        try #require(CommitTrailerWords.trailers.first { $0.key == key })
    }

    @Test func everyTrailerIsOnByDefaultButCost() {
        #expect(CommitTrailerWords.trailers.count == 10)
        #expect(CommitTrailerWords.trailers.filter { !$0.isOnByDefault }.map(\.key) == ["Cost"])
        #expect(CommitTrailerWords.trailers(.spend).map(\.key) == ["Cost", "Tokens", "Duration"])
        #expect(CommitTrailerWords.trailers.last?.key == "Co-authored-by")
    }

    @Test func aRuleNoLayerSetsHasItsDefault() throws {
        let cost = try trailer("Cost")
        let unset = DaemonAPI.EffectiveRule(rule: cost.rule, value: .bool(false), words: "Off")
        #expect(!CommitTrailerWords.isOn(cost, unset))
        #expect(!CommitTrailerWords.isOn(cost, nil))
        let set = DaemonAPI.EffectiveRule(rule: cost.rule, value: .bool(true), words: "On", decides: you)
        #expect(CommitTrailerWords.isOn(cost, set))
    }

    @Test func onlyTheTightValueAboveCapsYourSwitch() throws {
        let madeBy = try trailer("Made-by"), tokens = try trailer("Tokens")
        let required = DaemonAPI.EffectiveRule(rule: madeBy.rule, value: .bool(true), words: "On", decides: folder,
                                               cap: .bool(true), capLine: "This folder (agent-loops): Made-by is required")
        #expect(CommitTrailerWords.isCapped(madeBy, required))
        let offAbove = DaemonAPI.EffectiveRule(rule: madeBy.rule, value: .bool(false), words: "Off", decides: folder, cap: .bool(false))
        #expect(!CommitTrailerWords.isCapped(madeBy, offAbove))
        let keptOut = DaemonAPI.EffectiveRule(rule: tokens.rule, value: .bool(false), words: "Off", decides: folder, cap: .bool(false))
        #expect(CommitTrailerWords.isCapped(tokens, keptOut))
        #expect(!CommitTrailerWords.isCapped(tokens, nil))
    }

    @Test func settingATrailerKeepsTheKeysThisBuildDoesNotKnow() throws {
        let json = #"{"commitTrailers":{"Signed-off":"on","Cost":"off"}}"#
        let rules = try JSONDecoder().decode(DaemonAPI.PolicyRules.self, from: Data(json.utf8))
        let next = CommitTrailerWords.setting(rules, try trailer("Cost"), on: true)
        #expect(next.commitTrailers == ["Signed-off": .on, "Cost": .on])
        #expect(CommitTrailerWords.yours(try trailer("Cost"), in: next) == true)
        #expect(CommitTrailerWords.yours(try trailer("Tokens"), in: next) == nil)
    }

    @Test func thePreviewHasTheTrailersThatAreOnInTheBlocksOrder() {
        let lines = CommitTrailerWords.preview { $0.isOnByDefault }
        #expect(lines.count == 9)
        #expect(lines.first == "Made-by: claude-code:claude-opus-5-5")
        #expect(!lines.contains { $0.hasPrefix("Cost:") })
        #expect(lines.last?.hasPrefix("Co-authored-by: ") == true)
    }

    @Test func thePreviewIsTheSquashBlockInTheDaemonsFormat() {
        // A branch commit gets Refs and no Approved-by, so the Preview is the squash's block (#6532).
        #expect(CommitTrailerWords.previewScope.hasPrefix("the squash commit of a pull request"))
        let lines = CommitTrailerWords.preview { _ in true }
        #expect(lines.contains("Fixes: #6334"))
        #expect(lines.contains("Approved-by: alexec"))
        #expect(lines.contains("Cost: $1.84"))
        #expect(lines.contains("Tokens: in=412.0k out=38.0k"))
        #expect(lines.contains("Duration: 47m"))
    }

    @Test func aMachineThatDoesNotSayTheRuleDoesNotFollowIt() {
        #expect(!CommitTrailerWords.isFollowed(DaemonAPI.EffectivePolicy(rules: [DaemonAPI.EffectiveRule(rule: "sandbox", words: "On")])))
        #expect(CommitTrailerWords.isFollowed(DaemonAPI.EffectivePolicy(rules: [DaemonAPI.EffectiveRule(rule: "commitTrailers.Cost", words: "Off")])))
        #expect(CommitTrailerWords.behindLine([]) == nil)
        #expect(CommitTrailerWords.behindLine(["Studio Mac"])
            == "Studio Mac does not follow Commit trailers yet. Its agents’ commits get no trailers until it updates.")
    }

    @Test func aFailedSaveSaysTheSwitchIsBack() throws {
        #expect(CommitTrailerWords.failed(try trailer("Requested-by"), wasOn: true, reason: "The Mac did not answer.")
            == "Not saved: The Mac did not answer. Requested-by is On again.")
    }

    /// #6534: this repo sets Cost on, so its commits get Cost while the pane says Off.
    @Test func aFolderLineSaysWhatItsRuleChanges() throws {
        let cost = try trailer("Cost"), madeBy = try trailer("Made-by"), tokens = try trailer("Tokens")
        let outside = DaemonAPI.EffectivePolicy(rules: [
            DaemonAPI.EffectiveRule(rule: cost.rule, value: .bool(false), words: "Off"),
            DaemonAPI.EffectiveRule(rule: madeBy.rule, value: .bool(true), words: "On", decides: you),
        ])
        let inFolder = DaemonAPI.EffectivePolicy(folder: "/src/agents", rules: [
            DaemonAPI.EffectiveRule(rule: cost.rule, value: .bool(true), words: "On", decides: folder),
            DaemonAPI.EffectiveRule(rule: madeBy.rule, value: .bool(true), words: "On", decides: folder),
            DaemonAPI.EffectiveRule(rule: tokens.rule, value: .bool(false), words: "Off", decides: folder),
        ])
        let change = CommitTrailerWords.folderChange(folder: "/src/agents", name: "Agents", inFolder: inFolder, outside: outside)
        #expect(change == CommitTrailerWords.FolderChange(folder: "/src/agents", name: "Agents", changes: "Cost on, Tokens off"))
        // A folder whose rule sets only what applies outside it changes nothing.
        #expect(CommitTrailerWords.folderChange(folder: "/src/x", name: "x", inFolder: outside, outside: outside) == nil)
        // Attested is no trailer since #6545: a folder that still turns it off changes no commit.
        let offAttested = DaemonAPI.EffectivePolicy(rules: [DaemonAPI.EffectiveRule(rule: "commitTrailers.Attested", value: .bool(false),
                                                                                     words: "Off", decides: folder)])
        #expect(CommitTrailerWords.folderChange(folder: "/src/x", name: "x", inFolder: offAttested, outside: outside) == nil)
        #expect(CommitTrailerWords.trailers.map(\.key).contains("Attested") == false)
    }
}
