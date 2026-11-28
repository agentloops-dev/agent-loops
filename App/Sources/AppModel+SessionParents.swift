import AgentsKitCore
import SwiftUI

/// The row a person drags onto a lead in the sidebar (#7539), and the rows that go with it.
struct ParentDrag: Equatable {
    let id: UUID
    /// The dragged row and every row under it: none of them is a target.
    let subtree: Set<UUID>
    /// How many sessions go with it: the ghost's "+2".
    let carried: Int
}

/// A session under a lead or a director (#7539, epic #7535, spec 7535): the picker in New
/// Session, the drag in the sidebar and Move Under Lead…, all behind the per-person flag
/// `session-parents`. The daemon makes the checks again and is the one that is true; the Mac
/// refuses what its cache can see first, and moves the row at once.
extension AppModel {
    /// The flag is on for this person: the controls and the nesting by parent show.
    var sessionParentsOn: Bool { flags.isOn(.sessionParents) }

    /// Whether this session can be moved under a lead now: not archived, on a host that says
    /// `agents.parent/1` and answers.
    func canSetParent(_ agent: Agent) -> Bool {
        sessionParentsOn && agent.state != .archived && agent.purpose == nil
            && self.agent(agent.id, has: Capability.agentsParent1) && !hostUnreachable(agent.host)
    }

    /// The held sessions of one host, each under its effective parent.
    func parentIndex(on host: MachineRef) -> SessionParents.Index {
        SessionParents.Index(agents.filter { $0.host == host })
    }

    /// The leads a picker lists for `child` (nil in New Session), on `host`, this folder's first.
    func leadChoices(for child: Agent?, on host: MachineRef, folder: URL?) -> [SessionParents.Choice] {
        SessionParents.choices(for: child, folder: folder, in: agents.filter { $0.host == host })
    }

    /// Why `child` cannot go under `lead`, from the cache: nil when it can.
    func parentRefusal(moving child: Agent, under lead: Agent) -> SessionParents.Refusal? {
        guard lead.host == child.host else { return .notLead(SessionParents.title(lead)) }
        return parentIndex(on: child.host).refusal(moving: child, under: lead)
    }

    /// The lead a session sits under now, when its parent names one the list holds.
    func lead(of agent: Agent) -> Agent? {
        agent.parent?.id.flatMap { work.agent($0) }
    }

    /// The lead New Session has chosen, while it is still a live lead on the form's host.
    var draftLead: Agent? {
        guard sessionParentsOn, let id = draftParentID, let lead = work.agent(id),
              SessionParents.isLead(lead), lead.host == draftHost else { return nil }
        return lead
    }

    /// Whether New Session offers the Lead chip: the flag, a host that says it, and a lead.
    var offersDraftLead: Bool {
        sessionParentsOn && host(draftHost, has: Capability.agentsParent1)
            && agents.contains { $0.host == draftHost && SessionParents.isLead($0) }
    }

    /// Starts a drag of a row (#7539): the rows that go with it are held until the button is up.
    func beginParentDrag(_ agent: Agent) {
        let under = parentIndex(on: agent.host).descendants(of: agent.id).filter { $0.state != .archived }
        parentDrag = ParentDrag(id: agent.id, subtree: Set(under.map(\.id)).union([agent.id]), carried: under.count)
        // A drag that ends outside every target says nothing: the button coming up ends it.
        Task { @MainActor [weak self] in
            while NSEvent.pressedMouseButtons & 1 != 0 { try? await Task.sleep(for: .milliseconds(150)) }
            try? await Task.sleep(for: .milliseconds(150))
            guard let self, parentDrag?.id == agent.id else { return }
            parentDrag = nil
            parentDropTip = nil
        }
    }

    /// Under a lead, at the top or back under the starter: the row moves at once, the host is
    /// asked with the row's revision, and a refusal puts it back with the daemon's reason. A
    /// revision that moved on is read again and tried once more.
    func setParent(_ id: UUID, _ placement: SessionPlacement, offeringUndo: Bool = true) async {
        guard let agent = work.agent(id), SessionPlacement(of: agent) != placement else { return }
        let leadTitle = placement.leadID.flatMap { work.agent($0) }.map(SessionParents.title)
        let undo = offeringUndo ? SessionUndo.parent(agent, to: placement, lead: leadTitle) : nil
        let host = agent.host
        var refusal: String?
        let sent = await work.optimistically(.parent(placement), on: id) {
            for attempt in 0..<2 {
                let revision = attempt == 0 ? agent.revision : work.agent(id)?.revision
                do {
                    _ = try await client(forAgent: id).call(DaemonAPI.Method.agentsSetParent,
                                                            DaemonAPI.SetParentRequest(agentID: id, placement: placement,
                                                                                       expectedRevision: revision),
                                                            returning: Agent.self)
                    return true
                } catch let error as JSONRPCError where error.code == DaemonAPI.Failure.changedElsewhere && attempt == 0 {
                    if host == .mac { await refreshAgents() } else { await refreshServer(host) }
                } catch {
                    refusal = describe(error)
                    return false
                }
            }
            return false
        }
        if sent {
            offer(undo)
            AccessibilityNotification.Announcement(SessionParents.moved(placement, lead: leadTitle) + ".").post()
        } else {
            let line = SessionParents.couldNotMove(SessionParents.title(agent), refusal ?? "")
            problem = line
            AccessibilityNotification.Announcement(line).post()
        }
    }
}
