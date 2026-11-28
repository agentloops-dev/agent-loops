import Foundation
import Testing
@testable import AgentsKitCore

/// A session under a lead (#7539, spec 7535): the tree by parent behind the flag, and the
/// checks and words the Mac uses before it calls agents/setParent.
@MainActor
@Suite("Session parents")
struct SessionParentsTests {
    private let shop = URL(filePath: "/tmp/work/shop-demo")
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func session(_ title: String, at offset: Double, lead: Bool = false, by starter: Agent? = nil,
                         under parent: Agent? = nil, top: Bool = false, state: AgentState = .finished) -> Agent {
        var made = Agent(runtimeID: "claude", cwd: shop, title: title, state: state,
                         createdAt: t0.addingTimeInterval(offset), lastActivityAt: t0.addingTimeInterval(offset))
        if state == .finished { made.endedReason = .endTurn }
        if lead { made.labels = [SessionLabel(value: "lead", owner: .person, addedAt: t0)] }
        made.startedByAgent = starter?.id
        if let parent { made.parent = SessionParent(id: parent.id, setAt: t0) }
        if top { made.parent = SessionParent(id: nil, setAt: t0) }
        return made
    }

    /// With the flag, a parent wins over the starter, and a parent with no id is the top; the
    /// subtree goes with a moved lead. Without the flag, the tree is the starter's, as before.
    @Test func theTreeFollowsTheParentOnlyWithTheFlag() {
        let director = session("Plan the spring release", at: 1, lead: true)
        let billing = session("Billing page", at: 2, lead: true, by: director)
        let docs = session("Docs refresh", at: 3, lead: true, by: director, under: billing)
        let guide = session("Write the setup guide", at: 4, by: docs)
        let logo = session("Try a new logo", at: 5, by: billing, top: true)
        let list = [logo, guide, docs, billing, director]

        let on = SidebarTree.nest(list, starter: { _ in nil }, followsParent: true)
        #expect(on.tops.map(\.id) == [logo.id, director.id])
        #expect(on.children[director.id]?.map(\.id) == [billing.id])
        #expect(on.children[billing.id]?.map(\.id) == [docs.id])
        #expect(on.children[docs.id]?.map(\.id) == [guide.id])

        let off = SidebarTree.nest(list, starter: { _ in nil })
        #expect(off.tops.map(\.id) == [director.id])
        #expect(off.children[director.id]?.map(\.id) == [billing.id, docs.id])
        #expect(off.children[billing.id]?.map(\.id) == [logo.id])
    }

    /// A parent not in the list puts the session at the top; a loop the daemon never made
    /// is cut, so no session is lost from the list.
    @Test func aParentNotListedOrALoopIsTheTop() {
        let gone = session("Archived lead", at: 1, lead: true)
        let child = session("Fix invoice totals", at: 2, under: gone)
        var a = session("A", at: 3, lead: true)
        let b = session("B", at: 4, lead: true, under: a)
        a.parent = SessionParent(id: b.id, setAt: t0)
        let nested = SidebarTree.nest([child, a, b], starter: { _ in nil }, followsParent: true)
        // The loop is cut at its first session in the list: B stays under A.
        #expect(nested.tops.map(\.id) == [child.id, a.id])
        #expect(nested.children[a.id]?.map(\.id) == [b.id])
    }

    /// The refusals the cache can see: itself, its own subtree, too deep, full, not a lead.
    @Test func theCacheRefusesWhatTheDaemonWould() {
        let director = session("Plan the spring release", at: 1, lead: true)
        let docs = session("Docs refresh", at: 2, lead: true, under: director)
        let logo = session("Try a new logo", at: 3, under: docs)
        let plain = session("Tax line", at: 4)
        let index = SessionParents.Index([director, docs, logo, plain])

        #expect(index.refusal(moving: docs, under: docs) == .itself)
        #expect(index.refusal(moving: director, under: docs)
            == .cycle(lead: "Docs refresh", session: "Plan the spring release"))
        #expect(index.refusal(moving: logo, under: plain) == .notLead("Tax line"))
        #expect(index.refusal(moving: plain, under: docs) == nil)
        #expect(index.levels(of: logo.id) == 3)
        #expect(index.height(of: director.id) == 2)

        var chain = [session("L0", at: 10, lead: true)]
        for level in 1..<6 { chain.append(session("L\(level)", at: 10 + Double(level), lead: true, under: chain[level - 1])) }  // index-ok: made above
        let deep = SessionParents.Index(chain + [docs, logo, director])
        // L5 is at level 6: a session under it makes 7, and Docs refresh with its child 8.
        #expect(deep.refusal(moving: plain, under: chain[5]) == .tooDeep(levels: 7))  // index-ok: six made above
        #expect(deep.refusal(moving: docs, under: chain[4]) == .tooDeep(levels: 7))  // index-ok: six made above
        #expect(SessionParents.Refusal.tooDeep(levels: 7).words
            == "Too deep. Sessions can go 6 levels down; this move makes 7.")

        let full = session("Load tests", at: 20, lead: true)
        let kids = (0..<100).map { session("check \($0)", at: 21 + Double($0), under: full) }
        let crowded = SessionParents.Index([full, plain] + kids)
        #expect(crowded.refusal(moving: plain, under: full) == .full(lead: "Load tests"))
        // One already under it moves within it, so it does not count.
        #expect(crowded.refusal(moving: kids[0], under: full) == nil)  // index-ok: a hundred made above
    }

    /// The picker: live leads, this folder's first, with how many are under each, "director"
    /// for one with a lead under it, and the reason for one that is dimmed.
    @Test func thePickerListsLeadsWithTheirCounts() {
        let director = session("Plan the spring release", at: 1, lead: true)
        let billing = session("Billing page", at: 2, lead: true, under: director)
        let fix = session("Fix invoice totals", at: 3, under: billing)
        let archived = session("Old lead", at: 4, lead: true, state: .archived)
        let choices = SessionParents.choices(for: nil, folder: shop, in: [director, billing, fix, archived])
        #expect(choices.map(\.title) == ["Billing page", "Plan the spring release"])
        #expect(choices.map(\.description) == ["shop-demo · 1 under it", "shop-demo · 1 under it · director"])
        #expect(SessionParents.choices(for: billing, folder: shop, in: [director, billing, fix]).map(\.title)
            == ["Plan the spring release"])
    }

    /// Undo puts back the placement it had, and the record says the placement it has.
    @Test func undoPutsTheOldPlacementBack() {
        let billing = session("Billing page", at: 1, lead: true)
        let docs = session("Docs refresh", at: 2, lead: true)
        let fix = session("Fix invoice totals", at: 3, under: billing)
        let undo = SessionUndo.parent(fix, to: .under(docs.id), lead: "Docs refresh")
        #expect(undo?.line == "Moved “Fix invoice totals” under “Docs refresh”")
        #expect(undo?.inverse == .parent(fix.id, .under(billing.id)))
        #expect(SessionUndo.parent(fix, to: .under(billing.id), lead: "Billing page") == nil)

        let moved = AgentWrite.parent(.top).applied(to: fix)
        #expect(SessionPlacement(of: moved) == .top)
        #expect(AgentWrite.parent(.top).isSettled(by: moved))
        #expect(SessionPlacement(of: AgentWrite.parent(.starter).applied(to: fix)) == .starter)
    }

    /// The record's parent comes and goes on the wire as the schema says.
    @Test func theParentIsReadAndWritten() throws {
        let lead = UUID()
        let json = """
        {"id":"\(UUID().uuidString)","runtimeID":"claude","cwd":"file:///tmp/work/shop-demo/","state":"finished",
         "createdAt":"2027-01-15T08:00:00Z","lastActivityAt":"2027-01-15T08:00:00Z",
         "parent":{"id":"\(lead.uuidString)","setAt":"2027-01-15T08:00:00Z","setBy":"person"}}
        """
        let agent = try StoreCoding.decoder.decode(Agent.self, from: Data(json.utf8))
        #expect(agent.parent?.id == lead)
        #expect(SessionPlacement(of: agent) == .under(lead))
        let again = try StoreCoding.decoder.decode(Agent.self, from: StoreCoding.encoder.encode(agent))
        #expect(again.parent == agent.parent)

        let request = DaemonAPI.SetParentRequest(agentID: agent.id, placement: .top, expectedRevision: 4)
        let sent = try JSONSerialization.jsonObject(with: JSONEncoder().encode(request)) as? [String: Any]
        #expect(sent?["placement"] as? String == "top")
        #expect(sent?["parentID"] == nil)
        #expect(sent?["expectedRevision"] as? Int == 4)
    }
}
