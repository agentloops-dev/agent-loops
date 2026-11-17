import Foundation
import Testing
@testable import AgentsKitCore

/// New Session's Suggested section (#5460, epic #5457): the daemon's answer reads, and the row's words.
struct SuggestedLeadsTests {
    private let json = """
    {"suggestions":[
      {"node":{"kind":"issue","ref":"acme/app#4487"},"title":"Screens foundation","url":"https://github.com/acme/app/issues/4487",
       "folder":"/tmp/app","milestone":"Screens","due":"2026-12-04","done":0,"total":8,"high":true,
       "start":{"prompt":"#4487: Screens foundation\\n\\nLead this epic","labels":["#4487","lead"]}},
      {"node":{"kind":"issue","ref":"acme/app#4207"},"title":"Bug lane","done":0,"total":0,"standing":true,
       "start":{"prompt":"#4207: Bug lane","labels":["#4207","lead"]}}
    ],"more":4}
    """

    @Test func reads() throws {
        let read = try JSONDecoder().decode(DaemonAPI.WorkSuggestLeads.self, from: Data(json.utf8))
        #expect(read.suggestions.count == 2)
        #expect(read.more == 4)
        #expect(read.errors == nil)
        let first = try #require(read.suggestions.first)
        #expect(first.id == "acme/app#4487")
        #expect(first.start.labels == ["#4487", "lead"])
        #expect(first.start.worktree == nil)
    }

    @Test func words() throws {
        let read = try JSONDecoder().decode(DaemonAPI.WorkSuggestLeads.self, from: Data(json.utf8))
        let epic = read.suggestions[0], standing = read.suggestions[1]
        #expect(SuggestedLeads.title(epic) == "Lead #4487 Screens foundation")
        #expect(SuggestedLeads.detail(epic) == "0/8 · due 12-04")
        #expect(SuggestedLeads.spokenDetail(epic) == "0 of 8 done, due 12-04")
        #expect(SuggestedLeads.detail(standing) == "Standing")
        #expect(SuggestedLeads.more(read.more) == "4 more in Work")
        #expect(SuggestedLeads.more(0) == nil)
    }

    @Test func noNumberNoCount() {
        let s = DaemonAPI.WorkLeadSuggestion(node: .init(kind: "issue", ref: "ENG-12"), title: "Billing", due: "soon",
                                             start: .init(prompt: "Billing", labels: ["lead"]))
        #expect(SuggestedLeads.title(s) == "Lead Billing")
        #expect(SuggestedLeads.detail(s) == "")
    }

    // MARK: When to ask, and what a failed read says (#5790)

    private func machine(_ id: String, capabilities: [String]?) -> DaemonAPI.ControlMachine {
        var row = DaemonAPI.ControlMachine(id: MachineRef(rawValue: id), name: id, platform: "macOS", version: "0.17",
                                           state: "online", reach: "direct", machineID: nil)
        row.capabilities = capabilities
        return row
    }

    @Test func asksOnceTheHostIsListed() {
        let mac = MachineRef(rawValue: "mac")
        #expect(SuggestedLeads.canAsk(mac, in: nil), "no control plane: ask the folder's host")
        #expect(!SuggestedLeads.canAsk(mac, in: []), "the list has not come in: wait for it")
        #expect(SuggestedLeads.canAsk(mac, in: [machine("mac", capabilities: [Capability.workSuggestLeads1])]))
        #expect(SuggestedLeads.canAsk(mac, in: [machine("mac", capabilities: nil)]), "a host with no list has them all")
        #expect(!SuggestedLeads.canAsk(mac, in: [machine("mac", capabilities: [])]))
        #expect(!SuggestedLeads.canAsk(mac, in: [machine("devbox", capabilities: nil)]))
    }

    @Test func failedReadSaysWhy() {
        #expect(SuggestedLeads.failure(JSONRPCError(code: -32602, message: "work/suggest-leads: no forge")) ==
            "Could not read: work/suggest-leads: no forge")
        #expect(SuggestedLeads.failure(JSONRPCError.methodNotFound("work/suggest-leads")) == nil)
        #expect(SuggestedLeads.failure(CancellationError()) == nil)
        #expect(SuggestedLeads.hiddenLine == "Suggested is hidden.")
        let unread = DaemonAPI.WorkSuggestLeads(errors: [.init(forge: "github", message: "GitHub is not connected")])
        #expect(SuggestedLeads.unread(unread) == "Could not read: GitHub is not connected")
        #expect(SuggestedLeads.unread(DaemonAPI.WorkSuggestLeads()) == nil, "every epic has a lead: nothing to say")
    }
}
