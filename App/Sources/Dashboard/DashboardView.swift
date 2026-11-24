import AgentsKitCore
import SwiftUI

/// The Dashboard (#5511, PoC #5534): one place to see what is being worked on, on this Mac. The
/// counts on top, the epics' cards with their progress, then a row per work item under Needs you,
/// Working and Waiting (#5698), each with its sessions, its pull request and checks; finished work leaves (#5695).
/// What waits on the person is answered on its row, with the chat's own cards. A row's title
/// opens its Work page; a session opens its chat. No releases and no cost (Alex, 2026-11-14).
///
/// The sessions are the window's own, live. The forge's open issues, pull requests and epics are
/// read when the page opens and each minute after, and "Last update" says when (#5139).
///
/// The Mac window's for now: the web page follows in #5536 and the Remote in #5537.
struct DashboardView: View {
    @Environment(AppModel.self) private var model
    @State private var forge = DashboardForge()
    /// A count on top picked: only its section is listed. Nil lists them all.
    @State private var only: Dashboard.Section?

    private var board: Dashboard.Board {
        Dashboard.board(agents: model.agents.filter { $0.host == .mac }, group: { model.work.group(of: $0) },
                        asks: { hasCard($0.id) },
                        issues: forge.issues, pulls: forge.pulls, epics: forge.epics)
    }

    var body: some View {
        let board = board
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                header
                if board.isEmpty {
                    empty
                } else {
                    counts(board)
                    if !board.epics.isEmpty {
                        epics(board.epics)
                    } else if !forge.answered {
                        // The epics come with the forge's first read: a grey box keeps their place
                        // so the rows below do not move when they arrive (#5697).
                        DashboardEpicsLoading()
                    }
                    sections(board)
                }
            }
            .frame(maxWidth: 1000, alignment: .leading)
            .padding(24)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Dashboard")
        .task(id: model.firstListDrawn) {
            // Each minute while the page is open: the forge's state moves with no event here. The first read waits
            // for the sessions' first list (#6311): the Dashboard is the page a window opens on.
            guard model.firstListDrawn else { return }
            while !Task.isCancelled {
                await forge.read(model)
                try? await Task.sleep(for: .seconds(60))
            }
        }
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline) {
            Text("Dashboard").appText(.title)
            Spacer()
            if let at = forge.readAt { LastUpdateText(read: at) }
        }
    }

    @ViewBuilder
    private var empty: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Nothing is being worked on").appText(.reading)
            Text("The sessions on this Mac show here, one row for each issue or pull request they work on.")
                .appText(.supporting).foregroundStyle(Paper.secondary)
            if model.selectedFolderKey != nil {
                Button("New Session") {
                    model.composing = true
                }
                .buttonStyle(.paperProminent)
            }
        }
        .padding(.top, 40)
    }

    /// ✋ 2 need you · ● 3 working · the beacon and 2 waiting: each one lists only its section.
    private func counts(_ board: Dashboard.Board) -> some View {
        HStack(spacing: 10) {
            ForEach(Dashboard.Section.allCases) { section in
                let n = board.count(section)
                Button {
                    only = only == section ? nil : section
                } label: {
                    HStack(spacing: 6) {
                        DashboardMark(mark: section.mark)
                        Text("\(n) \(section.countWords(n))")
                    }
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(only == section ? Paper.selection : Paper.raised,
                                in: RoundedRectangle(cornerRadius: Paper.Radius.control))
                    .overlay(RoundedRectangle(cornerRadius: Paper.Radius.control).strokeBorder(Paper.rule))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .appText(.supporting)
                .help(only == section ? "Show every section" : "Show only \(section.title)")
            }
            if forge.failed {
                Text("The forge did not answer, so some rows show only their labels.")
                    .appText(.fine).foregroundStyle(Paper.secondary)
            }
        }
    }

    private func epics(_ epics: [Dashboard.Epic]) -> some View {
        ScrollView(.horizontal) {
            HStack(spacing: 10) {
                ForEach(epics) { epic in
                    DashboardEpicCard(epic: epic) {
                        model.showWork(WorkPlace(kind: .issue, ref: epic.node.ref))
                    }
                    // Work off (#6572): there is no page to open.
                    .allowsHitTesting(model.flags.isOn(.work))
                }
            }
        }
        .scrollIndicators(.never)
    }

    @ViewBuilder
    private func sections(_ board: Dashboard.Board) -> some View {
        let epicTitles = Dictionary(board.epics.map { ($0.node.ref, $0.title) }, uniquingKeysWith: { a, _ in a })
        // The first card on the page takes ⌘1…9 and Return, as the chat's one card does.
        let keys = board.rows[.needsYou]?.lazy.flatMap(\.sessions).first { hasCard($0.agent.id) }?.agent.id
        ForEach(board.sections, id: \.section) { section, items in
            if only == nil || only == section {
                VStack(alignment: .leading, spacing: 8) {
                    Text(section.title.uppercased())
                        .appText(.fine)
                        .foregroundStyle(Paper.secondary)
                    ForEach(items) { item in
                        DashboardRow(item: item, epicTitle: item.epic.flatMap { epicTitles[$0.ref] }, keysFor: keys,
                                     reread: { await forge.read(model) })
                    }
                }
            }
        }
    }

    /// Whether a session has a card for the person: a permission, a form, a change to the folder's
    /// own files (#531) or a use of a secret (#4918).
    private func hasCard(_ id: UUID) -> Bool {
        !model.work.permissions(for: id).isEmpty || model.work.elicitation(for: id) != nil
            || !model.work.guardedChanges(askedIn: id).isEmpty || !model.work.secretUses(askedBy: id).isEmpty
    }
}

/// The forge's open issues, pull requests and epics on this Mac, and when they were read.
@MainActor @Observable
final class DashboardForge {
    var issues: [DaemonAPI.ForgeItem] = []
    var pulls: [DaemonAPI.ForgeItem] = []
    var epics: [DaemonAPI.ForgeItem] = []
    var readAt: Date?
    /// The last read failed: the rows keep what was read before, or their labels.
    var failed = false
    /// The first read has answered, with or without the forge: until then the epics are not known.
    var answered = false

    func read(_ model: AppModel) async {
        let open = DaemonAPI.ForgeListRequest(state: "open", limit: 100)
        async let i = try? model.forgeList(.issue, open, on: .mac)
        async let p = try? model.forgeList(.pr, open, on: .mac)
        async let e = try? model.forgeList(.issue, DaemonAPI.ForgeListRequest(state: "open", label: "epic", limit: 100),
                                           on: .mac)
        let (issues, pulls, epics) = await (i, p, e)
        guard !Task.isCancelled else { return }
        failed = issues == nil || pulls == nil
        if let issues { self.issues = issues.items }
        if let pulls { self.pulls = pulls.items }
        if let epics { self.epics = epics.items }
        if !failed { readAt = Date() }
        answered = true
    }
}

extension Dashboard.Section {
    /// The mark its count shows.
    var mark: Dashboard.Mark {
        switch self {
        case .needsYou: .needsYou
        case .working: .working
        case .waiting: .waiting
        }
    }

    func countWords(_ n: Int) -> String {
        switch self {
        case .needsYou: n == 1 ? "needs you" : "need you"
        case .working: "working"
        case .waiting: "waiting"
        }
    }
}

/// A mark: the hand when it needs the person, the ring while it works, a cross when it failed, a
/// tick when done, and the session list's own marks for the rest (#5763): the breathing beacon
/// while it waits, drawn as what it waits on when there is one session to ask (#5022), a numbered
/// list while queued and a pause mark once stopped. Only the hand and the cross have colour.
struct DashboardMark: View {
    let mark: Dashboard.Mark
    /// The session the mark is for, which says what its beacon draws; none for a row or a count.
    var agent: Agent?
    @ScaledMetric private var scale: CGFloat = 1

    var body: some View {
        Group {
            switch mark {
            case .needsYou: Image(icon: "hand.raised.fill").foregroundStyle(StateTint.attention.style(or: .secondary))
            case .working: WorkingRing(size: 12 * scale, color: Paper.accent)
            case .waiting:
                // In step with every other beacon on the screen (#2456, #2657).
                WaitingBeacon(size: 12 * scale, color: Paper.accent, kind: agent.map { WaitKind.of($0) } ?? .other)
            case .queued: Image(icon: "list.number").foregroundStyle(Paper.accent)
            case .paused: Image(icon: "pause.circle").foregroundStyle(Paper.accent)
            case .failed: Image(icon: "xmark.circle").foregroundStyle(StateTint.failure.style(or: .secondary))
            case .done: Image(icon: "checkmark").foregroundStyle(Paper.accent)
            }
        }
        .frame(width: 16 * scale, height: 16 * scale)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(mark.words)
    }
}

/// An epic's card: its mark, number and title, how many of its children are closed, and how many
/// of its items are on the board. Opens the epic's Work page.
private struct DashboardEpicCard: View {
    let epic: Dashboard.Epic
    let open: () -> Void

    var body: some View {
        Button(action: open) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 6) {
                    DashboardMark(mark: epic.mark)
                    Text("#\(epic.number)").foregroundStyle(Paper.secondary).monospacedDigit()
                    Text(epic.title).lineLimit(1)
                }
                .appText(.supporting)
                if let p = epic.progress, p.total > 0 {
                    ProgressView(value: Double(p.done), total: Double(p.total))
                        .progressViewStyle(.linear)
                        .tint(Paper.accent)
                    Text("\(p.done) of \(p.total) done · \(epic.items) here")
                        .appText(.fine).foregroundStyle(Paper.secondary)
                } else {
                    Text(epic.items == 1 ? "1 item here" : "\(epic.items) items here")
                        .appText(.fine).foregroundStyle(Paper.secondary)
                }
            }
            .frame(width: 240, alignment: .leading)
            .padding(12)
            .background(Paper.raised, in: RoundedRectangle(cornerRadius: Paper.Radius.card))
            .overlay(RoundedRectangle(cornerRadius: Paper.Radius.card).strokeBorder(Paper.rule))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help("Open epic #\(epic.number)")
    }
}

/// The grey box in the epics' place until the forge's first read answers (#5697): an epic card's
/// lines, hidden, so it is as tall as the band the cards take.
private struct DashboardEpicsLoading: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                DashboardMark(mark: .waiting)
                Text("#0 Epic")
            }
            .appText(.supporting)
            ProgressView(value: 0).progressViewStyle(.linear)
            Text("0 of 0 done · 0 here").appText(.fine)
        }
        .hidden()
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Paper.wash, in: RoundedRectangle(cornerRadius: Paper.Radius.card))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Loading epics")
    }
}

/// A work item's row: its mark, number, title, epic, pull request and checks, and when it last did
/// anything; its sessions under it, and under a session that waits on the person, its cards.
private struct DashboardRow: View {
    @Environment(AppModel.self) private var model
    let item: Dashboard.Item
    let epicTitle: String?
    /// The session whose card takes the keys, of the whole page.
    let keysFor: UUID?
    /// Read the forge again: after an approval, so the row leaves Needs you.
    let reread: () async -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .center, spacing: 8) {
                DashboardMark(mark: item.mark)
                if let ref = item.refText {
                    Text(ref).foregroundStyle(Paper.secondary).monospacedDigit()
                }
                title
                Spacer(minLength: 8)
                if let epicTitle, let epic = item.epic {
                    Text("epic #\(WorkList.number(epic.ref))")
                        .appText(.fine).foregroundStyle(Paper.secondary)
                        .help(epicTitle)
                }
                if let pr = item.pullRequest, item.subject != .pullRequest(number: pr.number) || pr.checks != nil {
                    DashboardPullChip(pr: pr) { open($0) }
                }
                ActivityTime(date: item.lastActivity)
            }
            .appText(.supporting)
            // The pull request waits for the person's approval: approved here, or opened to read first.
            if let pr = item.pullRequest, pr.waitsForApproval, let node = pr.node {
                DashboardApprovalCard(pr: pr, node: node, title: item.title, open: { open(node) }, reread: reread)
                    .padding(.leading, 24)
            }
            ForEach(item.sessions) { session in
                DashboardSessionLine(session: session)
                if session.mark == .needsYou {
                    DashboardCards(agentID: session.agent.id)
                        .environment(\.cardKeys, session.agent.id == keysFor)
                        .padding(.leading, 24)
                }
            }
        }
        .padding(12)
        .background(Paper.raised, in: RoundedRectangle(cornerRadius: Paper.Radius.card))
        .overlay(RoundedRectangle(cornerRadius: Paper.Radius.card)
            .strokeBorder(item.mark == .needsYou ? Paper.lineAttention : Paper.rule))
    }

    @ViewBuilder
    private var title: some View {
        // With Work off (#6572) an item has no page to open.
        if model.flags.isOn(.work), let node = item.node {
            Button { open(node) } label: { Text(item.title).lineLimit(1) }
                .buttonStyle(.plain)
                .help("Open \(item.refText ?? item.title) in Work")
        } else if item.subject == .session, let session = item.sessions.first {
            Button { model.openAgent(session.agent.id) } label: { Text(item.title).lineLimit(1) }
                .buttonStyle(.plain)
                .help("Open its chat")
        } else {
            Text(item.title).lineLimit(1)
        }
    }

    private func open(_ node: DaemonAPI.LineageNode) {
        model.showWork(WorkPlace(kind: node.kind == "pr" ? .pr : .issue, ref: node.ref))
    }
}

/// "PR #20 ✓": a pull request and its checks. Opens the pull request's Work page when it was listed.
private struct DashboardPullChip: View {
    @Environment(AppModel.self) private var model
    let pr: Dashboard.PullRequest
    let open: (DaemonAPI.LineageNode) -> Void

    var body: some View {
        let chip = HStack(spacing: 4) {
            Image(icon: "arrow.triangle.pull").foregroundStyle(Paper.pull)
            Text("PR #\(pr.number)").monospacedDigit()
            if let checks = pr.checks, checks != "none" { checkMark(checks) }
        }
        .appText(.fine)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(Paper.well, in: RoundedRectangle(cornerRadius: Paper.Radius.control))
        if model.flags.isOn(.work), let node = pr.node {
            Button { open(node) } label: { chip }
                .buttonStyle(.plain)
                .help(help)
        } else {
            chip.help(help)
        }
    }

    @ViewBuilder
    private func checkMark(_ checks: String) -> some View {
        switch checks {
        case "passing": Image(icon: "checkmark").foregroundStyle(Paper.forgeOpen)
        case "failing": Image(icon: "xmark.circle").foregroundStyle(Paper.forgeClosed)
        default: WorkingRing(size: 10, color: Paper.forgeWaiting)
        }
    }

    private var help: String {
        let checks = switch pr.checks {
        case "passing": "checks pass"
        case "failing": "checks fail"
        case "running": "checks running"
        default: "no checks"
        }
        return "Pull request #\(pr.number)\(pr.draft ? ", a draft" : ""): \(checks)"
    }
}

/// A session under its row: its mark and title, what it does or asks, and when it last did
/// anything. Opens its chat.
private struct DashboardSessionLine: View {
    @Environment(AppModel.self) private var model
    let session: Dashboard.Session

    var body: some View {
        let agent = session.agent
        Button { model.openAgent(agent.id) } label: {
            HStack(alignment: .center, spacing: 8) {
                DashboardMark(mark: session.mark, agent: session.agent)
                Text(title(agent)).lineLimit(1)
                // High or low (#5849), as the session list marks it.
                if let chip = WorkPriorityChip(agent) { chip }
                Text(line(agent)).foregroundStyle(Paper.secondary).lineLimit(1)
                Spacer(minLength: 8)
                ActivityTime(date: agent.lastActivityAt)
            }
            .appText(.fine)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.leading, 24)
        .help("Open its chat")
    }

    private func title(_ agent: Agent) -> String {
        let t = agent.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return t.isEmpty ? "Untitled session" : t
    }

    private func line(_ agent: Agent) -> String {
        switch session.mark {
        case .needsYou:
            if let change = model.work.guardedChanges(askedIn: agent.id).first {
                return "changed \(change.path): review it"
            }
            let question = model.work.permission(for: agent.id)?.toolCall.line
                ?? model.work.elicitation(for: agent.id)?.message
            return StatusShape.needsYouLine(row: agent, isComingBack: model.isComingBack(agent), question: question)
                ?? "asks you"
        case .working:
            return agent.runningTool.map(StatusShape.runningLabel) ?? "working"
        case .waiting:
            return "waiting"
        case .queued:
            return "queued"
        case .paused:
            return "paused"
        case .failed:
            return agent.endedReason?.summary ?? "failed"
        case .done:
            return "done"
        }
    }
}

/// What a session waits on the person for, answered here with the chat's own cards: a permission
/// question, a form, a change to the folder's own files, a use of a secret.
private struct DashboardCards: View {
    @Environment(AppModel.self) private var model
    let agentID: UUID

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(model.work.permissions(for: agentID)) { request in
                PermissionView(request: request)
            }
            if let agent = model.work.agent(agentID) {
                ForEach(model.work.guardedChanges(askedIn: agentID), id: \.id) { change in
                    GuardedChangeQuestion(change: change, key: FolderKey(host: agent.host, folder: agent.folderPath))
                }
            }
            if let request = model.work.elicitation(for: agentID) {
                ElicitationView(request: request).id(request.id)
            }
            ForEach(model.work.secretUses(askedBy: agentID), id: \.id) { request in
                if let use = request.secretUse {
                    SecretUseCard(request: request, use: use, answer: { await model.answerSecretUse(request, $0) })
                }
            }
        }
    }
}

/// A pull request that waits for the person's approval (#5534), as a card like the chat's: what it
/// is and where its checks and review stand, Open pull request on the left to read it first, and
/// Approve on the right. Approve reads the pull request's head and approves that commit, as Work's
/// own Approve does, so a push after the read is not approved unseen.
private struct DashboardApprovalCard: View {
    @Environment(AppModel.self) private var model
    let pr: Dashboard.PullRequest
    let node: DaemonAPI.LineageNode
    let title: String
    let open: () -> Void
    let reread: () async -> Void

    @State private var busy = false
    @State private var failed: String?

    var body: some View {
        let writes = model.forgeWrites(on: .mac)
        AskCard(icon: "arrow.triangle.pull", title: "Pull request #\(pr.number) waits for your approval",
                asker: title) {
            VStack(alignment: .leading, spacing: 4) {
                Text(state).appText(.supporting).foregroundStyle(Paper.secondary)
                if let failed { Text(failed).appText(.fine).foregroundStyle(StateTint.failure.style(or: .secondary)) }
            }
        } decline: {
            AskDecline("Open pull request", action: open)
                .disabled(!model.flags.isOn(.work))
        } buttons: {
            Button { approve() } label: {
                HStack(spacing: 6) {
                    if busy { ProgressView().controlSize(.small) }
                    Text("Approve")
                }
            }
            .buttonStyle(.paperProminent)
            .disabled(busy || !writes)
            .help(writes ? "Approve pull request #\(pr.number) on its forge"
                         : "This Mac's host cannot write to the forge: update Agent Loops")
        }
    }

    /// "Checks pass · review required", as the forge says them.
    private var state: String {
        let checks = switch pr.checks {
        case "passing": "Checks pass"
        case "failing": "Checks fail"
        case "running": "Checks running"
        default: "No checks"
        }
        let review = switch pr.review {
        case "changes_requested": "changes requested"
        case "review_required": "review required"
        default: "not reviewed yet"
        }
        return "\(checks) · \(review)"
    }

    private func approve() {
        guard !busy else { return }
        busy = true
        failed = nil
        Task {
            defer { busy = false }
            do {
                let detail = try await model.forgeItem(node, cursor: nil, on: .mac)
                guard let head = detail.head else {
                    failed = "The forge did not say which commit to approve. Open the pull request to approve it there."
                    return
                }
                _ = try await model.forge(DaemonAPI.Method.forgeApprove, DaemonAPI.ForgeApproveRequest(node: node, sha: head),
                                          returning: DaemonAPI.ForgeDone.self, on: .mac)
                await reread()
            } catch {
                failed = WorkList.failureWords(error)
            }
        }
    }
}
