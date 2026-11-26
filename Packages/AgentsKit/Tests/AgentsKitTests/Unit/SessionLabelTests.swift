import Foundation
import Testing
@testable import AgentsKitCore

@Suite("Session labels")
struct SessionLabelTests {
    private let folder = URL(filePath: "/work/labels")

    @Test func trimsAndMatchesCaseWithoutDuplicating() throws {
        let first = try SessionLabelPolicy.change(current: [], add: [" Perf "],
                                                  actor: .person, folderLabels: [])
        #expect(first.map(\.value) == ["Perf"])
        let second = try SessionLabelPolicy.change(current: first, add: ["perf"],
                                                   actor: .person, folderLabels: first)
        #expect(second == first)
    }

    @Test func commaFinishesWhatWasTyped() {
        #expect(SessionLabelPolicy.split(typed: "perf") == (finished: [], remainder: "perf"))
        #expect(SessionLabelPolicy.split(typed: "perf,") == (finished: ["perf"], remainder: ""))
        #expect(SessionLabelPolicy.split(typed: "a, b,c") == (finished: ["a", " b"], remainder: "c"))
        #expect(SessionLabelPolicy.split(typed: "") == (finished: [], remainder: ""))
    }

    @Test func kindComesFromTheTextAlone() {
        #expect(SessionLabel.kind(of: "#2958") == .issue)
        #expect(SessionLabel.kind(of: "ENG-4") == .issue)
        #expect(SessionLabel.kind(of: "P#2955") == .pullRequest)
        #expect(SessionLabel.kind(of: "Merged") == .finished)
        #expect(SessionLabel.kind(of: "archived") == .finished)
        #expect(SessionLabel.kind(of: "#12a") == .plain)
        #expect(SessionLabel.kind(of: "eng-4") == .plain)
        #expect(SessionLabel(value: "bug", owner: .person).kind == .plain)
    }

    @Test func acceptsOnlyWhatFits() {
        #expect(SessionLabelPolicy.accepted([" ui ", "", "UI", "perf"], existing: ["Perf"]) == ["ui"])
        #expect(SessionLabelPolicy.accepted([String(repeating: "x", count: 25), "ok"], existing: []) == ["ok"])
        #expect(SessionLabelPolicy.accepted(["f", "g"], existing: ["a", "b", "c", "d"]) == ["f"])
    }

    @Test func refusesEmptyAndOverlongValues() throws {
        #expect(throws: SessionLabelPolicy.Refusal.self) {
            try SessionLabelPolicy.change(current: [], add: ["   "],
                                          actor: .person, folderLabels: [])
        }
        #expect(throws: SessionLabelPolicy.Refusal.self) {
            try SessionLabelPolicy.change(current: [], add: [String(repeating: "x", count: 25)],
                                          actor: .person, folderLabels: [])
        }
    }

    /// #7241: a lane that joins five issues still adds lane, its PR and merged, and the
    /// refusal says which labels it counted.
    @Test func linksAndFinishedWordsHaveARoomOfTheirOwn() throws {
        let issues = try SessionLabelPolicy.change(current: [], add: ["#1", "#2", "#3", "#4", "#5"],
                                                   actor: .agent, folderLabels: [])
        let more = try SessionLabelPolicy.change(current: issues, add: ["lane", "P#9", "merged", "a", "b", "c", "d"],
                                                 actor: .agent, folderLabels: [])
        #expect(more.count == 12)
        #expect(throws: SessionLabelPolicy.Refusal.tooMany(["lane", "a", "b", "c", "d"])) {
            try SessionLabelPolicy.change(current: more, add: ["e"], actor: .agent, folderLabels: [])
        }
        let links = (100..<120).map { "#\($0)" }
        #expect(SessionLabelPolicy.refusal(of: links) == nil)
        #expect(SessionLabelPolicy.refusal(of: links + ["P#1"]) == .tooManyLinks(links))
        #expect(SessionLabelPolicy.refusal(of: links + ["merged", "closed", "archived", "x"]) == nil)
        #expect(SessionLabelPolicy.hasRoom(existing: ["a", "b", "c", "d", "e"]))
        #expect(!SessionLabelPolicy.hasRoom(existing: ["a", "b", "c", "d", "e"] + links))
    }

    @Test func refusesSixthWithoutChangingFive() throws {
        let five = try SessionLabelPolicy.change(current: [],
                                                 add: ["one", "two", "three", "four", "five"],
                                                 actor: .person, folderLabels: [])
        #expect(five.count == 5)
        #expect(throws: SessionLabelPolicy.Refusal.self) {
            try SessionLabelPolicy.change(current: five, add: ["six"],
                                          actor: .person, folderLabels: five)
        }
        #expect(five.count == 5)
    }

    @Test func reusesTheFoldersFirstSpelling() throws {
        let first = try SessionLabelPolicy.change(current: [], add: ["Perf"],
                                                  actor: .person, folderLabels: [])
        let second = try SessionLabelPolicy.change(current: [], add: ["perf"],
                                                   actor: .agent, folderLabels: first)
        #expect(second.map(\.value) == ["Perf"])
        #expect(second.map(\.owner) == [.agent])
    }

    @Test func vocabularyIncludesArchiveAndForgetsFinalRemoval() throws {
        var archived = Agent(runtimeID: "claude", cwd: folder, state: .archived,
                             archivedReason: .byUser)
        archived.labels = try SessionLabelPolicy.change(current: [], add: ["Perf"],
                                                        actor: .person, folderLabels: [])
        var elsewhere = Agent(runtimeID: "claude", cwd: URL(filePath: "/work/elsewhere"))
        elsewhere.labels = try SessionLabelPolicy.change(current: [], add: ["other"],
                                                         actor: .person, folderLabels: [])
        let listed = SessionLabelPolicy.vocabulary(in: folder, agents: [archived, elsewhere])
        #expect(listed.map(\.value) == ["Perf"])
        archived.labels = []
        #expect(SessionLabelPolicy.vocabulary(in: folder, agents: [archived, elsewhere]).isEmpty)
    }

    @Test func personCanTakeOwnershipButAgentCannotClaimPersonLabel() throws {
        let byAgent = try SessionLabelPolicy.change(current: [], add: ["urgent"],
                                                    actor: .agent, folderLabels: [])
        let byPerson = try SessionLabelPolicy.change(current: byAgent, add: ["URGENT"],
                                                     actor: .person, folderLabels: byAgent)
        try #require(byPerson.count == 1)
        #expect(byPerson[0].owner == .person)
        #expect(throws: SessionLabelPolicy.Refusal.self) {
            try SessionLabelPolicy.change(current: byPerson, add: ["urgent"],
                                          actor: .agent, folderLabels: byPerson)
        }
        #expect(throws: SessionLabelPolicy.Refusal.self) {
            try SessionLabelPolicy.change(current: byPerson, remove: [" UrGeNt "],
                                          actor: .agent, folderLabels: byPerson)
        }
    }
}
