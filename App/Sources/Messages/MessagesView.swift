import AgentsKitCore
import SwiftUI

/// The list's state (#4344), kept by the model while the window is open, so a conversation's page
/// goes back to the list as it was left.
@MainActor @Observable
final class MessagesListState {
    var lists: [MessagesList.HostConversations]?
    /// A service's word (slack, email), or "" for All.
    var source = ""
    var unreadOnly = false
    /// The conversation open over the list.
    var open: MessagesList.Row?
    var asked = 0
    var loadingMore = false
    /// Goes up each time a conversation or all of them were marked read (#5137), so the sidebar row reads its count
    /// again at once.
    var marked = 0
    var markingAll = false
    var markProblems: [String] = []

    /// The conversation last opened in a window of its own (#5482), and how many were: its window starts from this row,
    /// and one already open reads its messages again, as opening it in place does.
    private(set) var windowRow: MessagesList.Row?
    private(set) var windowAsks = 0

    func openInWindow(_ row: MessagesList.Row, _ window: OpenWorkWindow) {
        windowRow = row
        windowAsks += 1
        window(WorkWindowKey(conversation: row.id))
    }

    /// The conversation `key` as this window knows it now.
    func known(_ key: String) -> MessagesList.Row? {
        if let windowRow, windowRow.id == key { return windowRow }
        return MessagesList.merged(lists ?? []).first { $0.id == key }
    }

    var ask: [String] { [String(asked), String(unreadOnly)] }
    var rows: [MessagesList.Row] { MessagesList.merged(lists ?? [], source: source) }
    /// Where the next page of messages/conversationsAll starts (#5571); none at the end.
    var nextCursor: String?
    var hasMore: Bool { lists != nil && nextCursor != nil }

    func load(model: AppModel) async {
        let asked = asked
        let unread = unreadOnly
        lists = nil
        let (read, cursor) = await Self.read(model: model, unread: unread)
        // A newer Refresh wins.
        if asked == self.asked, unread == unreadOnly { lists = read; nextCursor = cursor }
    }

    /// A read marked the conversation `key` read (#5137): its count goes to 0 here, and the sidebar's is read again.
    func markedRead(_ key: String) {
        if let lists { self.lists = MessagesList.markedRead(lists, key: key) }
        marked += 1
    }

    /// Mark All as Read (#5137): every machine that can marks every conversation of its connectors read; then the
    /// list is read again, as the services now say.
    func markAllRead(model: AppModel) async {
        guard !markingAll else { return }
        markingAll = true
        markProblems = []
        defer { markingAll = false }
        let machines = model.messagesMarkMachines
        var answers: [(name: String, marked: DaemonAPI.MessagesMarkedAllRead?, problem: String?)] = []
        await withTaskGroup(of: (String, DaemonAPI.MessagesMarkedAllRead?, String?).self) { group in
            for machine in machines {
                group.addTask {
                    // The result first, then the tuple from one place (#5946, `Result.catching`).
                    let got = await Result.catching {
                        try await model.messages(DaemonAPI.Method.messagesMarkAllRead, DaemonAPI.MessagesMarkAllReadRequest(),
                                                 returning: DaemonAPI.MessagesMarkedAllRead.self, on: machine.id)
                    }
                    return (machine.name, try? got.get(), got.error?.localizedDescription)
                }
            }
            for await (name, got, problem) in group { answers.append((name: name, marked: got, problem: problem)) }
        }
        markProblems = MessagesList.markProblems(answers.sorted { a, b in
            (machines.firstIndex { $0.name == a.name } ?? 0) < (machines.firstIndex { $0.name == b.name } ?? 0)
        })
        marked += 1
        asked += 1
    }

    /// The read's next page, after the pages before.
    func more(model: AppModel) async {
        guard let lists, let cursor = nextCursor, !loadingMore else { return }
        loadingMore = true
        defer { loadingMore = false }
        let unread = unreadOnly
        let (read, next) = await Self.read(model: model, unread: unread, cursor: cursor, into: lists)
        guard unread == unreadOnly else { return }
        self.lists = read
        nextCursor = next
    }

    /// Every machine's conversations in one call to the control plane (#5571): the first page, or the one after
    /// `cursor`, added to `into`; and where the next page starts. A failed call is a line of its own.
    static func read(model: AppModel, unread: Bool, cursor: String? = nil,
                             into: [MessagesList.HostConversations] = [])
        async -> (lists: [MessagesList.HostConversations], cursor: String?) {
        guard !model.messagesMachines.isEmpty else { return (into, nil) }
        // The result first, then the tuple (#5946, `Result.catching`).
        let read = await Result.catching {
            try await model.messagesAll(DaemonAPI.MessagesConversationsAllRequest(unread: unread ? true : nil,
                                                                              limit: 50, cursor: cursor))
        }
        switch read {
        case let .success(got):
            return (MessagesList.hostConversations(got, into: into, thisMac: model.thisMacHostID), got.cursor)
        case let .failure(error):
            return (into + [.init(host: MachineRef(rawValue: ""), name: "Control plane", problem: error.localizedDescription)],
                    nil)
        }
    }
}

/// Messages (#4344): the conversations of every messaging connector that follows the messaging
/// contract, on every machine, in one list, the newest first, with Source and Unread filters; a
/// conversation opens to its messages, with a reply field when its connector can send (#4345). The
/// web page's is `Web/src/views/Messages.tsx`.
struct MessagesView: View {
    @Bindable var list: MessagesListState

    private var next: MessagesList.Row? {
        list.open.flatMap { MessagesList.next(after: $0.id, in: list.rows) }
    }

    var body: some View {
        Group {
            if let row = list.open {
                ConversationPage(row: row) { list.markedRead(row.id) }.id(row.id)
            } else {
                MessagesListPane(list: list)
            }
        }
        .navigationTitle(list.open?.conversation.title ?? MessagesList.title)
        .toolbar {
            ToolbarItemGroup(placement: .navigation) {
                Button { list.open = nil } label: { Image(toolbarIcon: "chevron.left") }
                    .disabled(list.open == nil)
                    .help("Back to Messages")
                // Next (#5137): the next conversation in the list as it is filtered and sorted now.
                Button { list.open = next } label: { Image(toolbarIcon: "chevron.down") }
                    .keyboardShortcut(.downArrow, modifiers: [.command, .option])
                    .disabled(next == nil)
                    .help(MessagesList.nextHelp)
                    .accessibilityLabel(MessagesList.nextConversation)
            }
            ToolbarItem(placement: .primaryAction) {
                if let row = list.open { ConversationWindowButton(row: row, list: list, inToolbar: true) }
            }
        }
        .modifier(WorkWindows())
    }
}

/// **Open in New Window** on a conversation (#5482), in its row's menu and in its page's toolbar.
private struct ConversationWindowButton: View {
    @Environment(\.openWorkWindow) private var openWorkWindow
    let row: MessagesList.Row
    let list: MessagesListState
    var inToolbar = false

    var body: some View {
        if let openWorkWindow {
            if inToolbar {
                Button { list.openInWindow(row, openWorkWindow) } label: { Image(toolbarIcon: "macwindow.badge.plus") }
                    .help(OpenWorkWindow.words)
                    .accessibilityLabel(OpenWorkWindow.words)
            } else {
                Button(OpenWorkWindow.words) { list.openInWindow(row, openWorkWindow) }
            }
        }
    }
}

/// A conversation in a window of its own (#5482), as Messages shows it. One opened again at launch is found by its key
/// in the machines' lists, with the same read as the list's.
struct ConversationWindow: View {
    @Environment(AppModel.self) private var model
    let conversation: String
    let list: MessagesListState
    @State private var row: MessagesList.Row?
    /// What the lookup read when it did not find the conversation; nil before.
    @State private var missing: [MessagesList.HostConversations]?
    @State private var tries = 0
    @State private var asks = 0

    private struct Ask: Equatable {
        let machines: [MachineRef]
        let tries: Int
    }

    var body: some View {
        Group {
            if let row {
                ConversationPage(row: row, asks: asks) { list.markedRead(row.id) }
            } else if let missing {
                notFound(missing)
            } else if model.controlPlaneListed, model.messagesMachines.isEmpty {
                Text(MessagesList.noMachine).foregroundStyle(.secondary).padding(24)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List { WorkPlaceholderRows(count: 4) }.listStyle(.inset).disabled(true)
            }
        }
        .navigationTitle(row?.conversation.title ?? MessagesList.title)
        .task(id: Ask(machines: model.messagesMachines.map(\.id), tries: tries)) { await find() }
        .onChange(of: list.windowAsks) {
            guard let opened = list.windowRow, opened.id == conversation else { return }
            row = opened
            asks += 1
        }
    }

    private func notFound(_ read: [MessagesList.HostConversations]) -> some View {
        VStack(spacing: 12) {
            ForEach(MessagesList.problems(read), id: \.self) { line in
                Label(line, icon: "exclamationmark.triangle")
            }
            if MessagesList.problems(read).isEmpty { Text(MessagesList.notInList) }
            Button("Try Again") { tries += 1 }.linkStyle()
        }
        .appText(.supporting)
        .foregroundStyle(.secondary)
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func find() async {
        guard row == nil else { return }
        missing = nil
        row = list.known(conversation)
        guard row == nil, !model.messagesMachines.isEmpty else { return }
        var read: [MessagesList.HostConversations] = []
        var cursor: String?
        for _ in 0 ..< MessagesList.mostPagesToFind {
            (read, cursor) = await MessagesListState.read(model: model, unread: false, cursor: cursor, into: read)
            guard !Task.isCancelled, row == nil else { return }
            row = MessagesList.merged(read).first { $0.id == conversation }
            if row != nil || cursor == nil { break }
        }
        if row == nil { missing = read }
    }
}

private struct MessagesListPane: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWorkWindow) private var openWorkWindow
    @Bindable var list: MessagesListState

    var body: some View {
        VStack(spacing: 0) {
            filters.padding(.horizontal, 16).padding(.vertical, 8)
            Divider()
            content
        }
        .task(id: list.ask) { await list.load(model: model) }
    }

    /// The conversation here, or with ⌘ held in a window of its own (#5482).
    private func open(_ row: MessagesList.Row) {
        if let openWorkWindow, OpenWorkWindow.commandHeld {
            list.openInWindow(row, openWorkWindow)
        } else {
            list.open = row
        }
    }

    private var filters: some View {
        HStack(spacing: 12) {
            Picker("Source", selection: $list.source) {
                Text("All").tag("")
                ForEach(MessagesList.sources(list.lists ?? []), id: \.self) { Text(ChannelLinkWords.channel($0)).tag($0) }
            }
            .fixedSize()
            Toggle("Unread", isOn: $list.unreadOnly)
                .toggleStyle(.checkbox)
            Spacer()
            if !model.messagesMarkMachines.isEmpty {
                Button(list.markingAll ? MessagesList.markingAllRead : MessagesList.markAllRead) {
                    Task { await list.markAllRead(model: model) }
                }
                .linkStyle()
                .disabled(list.markingAll || !(MessagesList.hasUnread(list.lists ?? []) || list.hasMore))
                .help(MessagesList.markAllHelp)
            }
            Button("Refresh") { list.asked += 1 }
                .linkStyle()
        }
    }

    @ViewBuilder
    private var content: some View {
        if model.messagesMachines.isEmpty {
            message(MessagesList.noMachine)
        } else if let lists = list.lists {
            if MessagesList.noConnector(lists), !list.unreadOnly {
                noConnector
            } else {
                let rows = list.rows
                List {
                    ForEach(MessagesList.problems(lists) + list.markProblems, id: \.self) { line in
                        Label(line, icon: "exclamationmark.triangle").foregroundStyle(.secondary)
                    }
                    if rows.isEmpty {
                        Text(list.unreadOnly ? MessagesList.noUnread : MessagesList.noConversations).foregroundStyle(.secondary)
                    }
                    ForEach(rows) { row in
                        Button { open(row) } label: { ConversationRow(row: row) }
                            .buttonStyle(.plain)
                            .contextMenu { ConversationWindowButton(row: row, list: list) }
                    }
                    if list.hasMore {
                        Button(list.loadingMore ? "Loading…" : "More") { Task { await list.more(model: model) } }
                            .linkStyle()
                            .disabled(list.loadingMore)
                    }
                }
                .listStyle(.inset)
            }
        } else {
            // The first read: placeholder rows, as Work draws them, no spinner in the middle.
            List { WorkPlaceholderRows() }.listStyle(.inset).disabled(true)
        }
    }

    /// No connector yet: a way to the Messaging slot, in the folder's Connectors.
    private var noConnector: some View {
        VStack(spacing: 12) {
            Text(MessagesList.noConnector)
                .appText(.supporting)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            if let key = model.selectedFolderKey ?? model.folders.first.map({ FolderKey(host: $0.host, folder: $0.folder.folder) }) {
                Button(MessagesList.setUp) { model.sidebarItem = .folderMCPServers(key) }
                    .linkStyle()
                    .help("The folder’s Connectors, with its Messaging slot")
            }
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func message(_ text: String) -> some View {
        Text(text)
            .appText(.supporting)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.center)
            .padding(24)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// A row: the service's icon, who sent the newest message (when the service says), the conversation's title,
/// its unread count and when it last moved.
private struct ConversationRow: View {
    let row: MessagesList.Row

    var body: some View {
        let c = row.conversation
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(icon: MessagesList.symbol(c.source))
                .foregroundStyle(.secondary)
                .frame(width: 18)
                .help(ChannelLinkWords.channel(c.source))
                .accessibilityLabel(ChannelLinkWords.channel(c.source))
            if let sender = c.sender {
                Text(verbatim: MessagesList.sender(sender)).fontWeight(row.isUnread ? .semibold : .regular).lineLimit(1)
                    .frame(maxWidth: 180, alignment: .leading)
                    .help(sender.identity)
            }
            Text(verbatim: c.title).fontWeight(row.isUnread ? .semibold : .regular).lineLimit(1)
                .foregroundStyle(c.sender == nil ? .primary : .secondary)
            if let unread = MessagesList.unread(c) {
                Text(unread).appText(.fine).foregroundStyle(.tint)
            }
            Spacer(minLength: 8)
            if let at = c.lastActivityDate {
                Text(at, format: .relative(presentation: .named)).appText(.fine).monospacedDigit().foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .contentShape(Rectangle())
    }
}

/// A conversation's messages, the newest first, each with its sender, time, text (outside content,
/// drawn as Markdown that loads nothing) and a link to the original; older pages with More. Under them, the reply
/// field (#4345), when the connector can send: the person's own reply, which says who it goes as. The footer says how
/// old the messages are, and Refresh (⌘R) reads them again (#7076).
private struct ConversationPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openURL) private var openURL
    let row: MessagesList.Row
    /// Goes up when the person opens the conversation again while it is shown (#5482): it is read again.
    var asks = 0
    /// The read marked the conversation read on its service (#5137).
    var markedRead: () -> Void = {}

    @State private var messages: [DaemonAPI.MessagingMessage]?
    @State private var cursor: String?
    @State private var problem: String?
    @State private var loading = false
    /// When the newest page was read.
    @State private var readAt: Date?
    @State private var refreshes = 0
    /// Goes up with each read of the newest page: an older read that ends after it is dropped.
    @State private var generation = 0
    @State private var draft = ""
    /// The thread the reply answers in; nil: the conversation itself.
    @State private var thread: String?
    @State private var sending = false
    @State private var sendProblem: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header.padding(.horizontal, 16).padding(.vertical, 8)
            Divider()
            if let messages {
                List {
                    Label(MessagesList.outside, icon: "exclamationmark.shield").appText(.fine).foregroundStyle(.secondary)
                    if messages.isEmpty {
                        Text(MessagesList.noMessages).foregroundStyle(.secondary)
                    }
                    ForEach(messages) { m in
                        MessageRow(message: m, replyInThread: row.item.replyAs != nil && m.thread != nil ? { thread = m.thread } : nil)
                    }
                    if cursor != nil {
                        Button(loading ? "Loading…" : "Older") { Task { await read(more: true) } }
                            .linkStyle()
                            .disabled(loading)
                    }
                }
                .listStyle(.inset)
            } else if let problem {
                Text(problem).foregroundStyle(.secondary).padding(24).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List { WorkPlaceholderRows(count: 4) }.listStyle(.inset).disabled(true)
            }
            if let replyAs = row.item.replyAs, messages != nil {
                Divider()
                replyField(replyAs).padding(.horizontal, 16).padding(.vertical, 8)
            }
            WorkPageBar(state: WorkLook.pageFooterState(drawn: messages != nil, reading: loading, fromDevice: false,
                                                        lines: problem.map { [$0] } ?? []),
                        count: nil, read: readAt) { refreshes += 1 }
        }
        .toolbar {
            ToolbarItem(placement: .primaryAction) { WorkRefreshButton(running: loading) { refreshes += 1 } }
        }
        .workTask(id: [asks, refreshes]) { await read(more: false) }
    }

    private func replyField(_ replyAs: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            if thread != nil {
                HStack(spacing: 8) {
                    Label(MessagesList.inThread, icon: "arrowshape.turn.up.left").appText(.fine).foregroundStyle(.secondary)
                    Button(MessagesList.leaveThread) { thread = nil }
                        .linkStyle()
                }
            }
            HStack(alignment: .bottom, spacing: 8) {
                TextField(MessagesList.replyPlaceholder, text: $draft, axis: .vertical)
                    .lineLimit(1...6)
                    .textFieldStyle(.roundedBorder)
                    .disabled(sending)
                    .accessibilityLabel(MessagesList.reply)
                Button(sending ? MessagesList.sending : MessagesList.send) { Task { await send() } }
                    .keyboardShortcut(.return, modifiers: .command)
                    .disabled(sending || MessagesList.replyText(draft) == nil)
                    .help("Send the reply (⌘↩)")
            }
            Text(MessagesList.sentAs(replyAs)).appText(.fine).foregroundStyle(.secondary)
            if let sendProblem {
                Label(sendProblem, icon: "exclamationmark.triangle").appText(.fine).foregroundStyle(.secondary)
            }
        }
    }

    private func send() async {
        guard let text = MessagesList.replyText(draft), !sending else { return }
        sending = true
        sendProblem = nil
        defer { sending = false }
        do {
            let sent = try await model.messages(
                DaemonAPI.Method.messagesReply,
                DaemonAPI.MessagesReplyRequest(server: row.item.server, folder: row.item.folder, conversation: row.conversation.id,
                                               thread: thread, text: text),
                returning: DaemonAPI.MessagesReplied.self, on: row.host)
            messages = [sent.message] + (messages ?? [])
            draft = ""
        } catch {
            sendProblem = model.describe(error)
        }
    }

    private var header: some View {
        let c = row.conversation
        return HStack(spacing: 8) {
            Image(icon: MessagesList.symbol(c.source)).foregroundStyle(.secondary)
            Text(ChannelLinkWords.channel(c.source)).foregroundStyle(.secondary)
            Text(verbatim: "▸ \(row.item.server)").appText(.fine).foregroundStyle(.secondary)
            Spacer()
            if let link = c.link, let url = URL(string: link) {
                Button("Open in \(ChannelLinkWords.channel(c.source))") { openURL(url) }
                    .linkStyle()
                    .help(link)
            }
        }
    }

    private func read(more: Bool) async {
        if !more { generation += 1 }
        let mine = generation
        loading = true
        defer { if mine == generation { loading = false } }
        do {
            let got = try await model.messages(
                DaemonAPI.Method.messagesRead,
                DaemonAPI.MessagesReadRequest(server: row.item.server, folder: row.item.folder, conversation: row.conversation.id,
                                              cursor: more ? cursor : nil, limit: 50),
                returning: DaemonAPI.MessagesRead.self, on: row.host)
            guard mine == generation else { return }
            messages = (more ? messages ?? [] : []) + got.messages
            cursor = got.cursor
            problem = nil
            if !more { readAt = .now }
            if got.markedRead == true { markedRead() }
        } catch {
            guard mine == generation, !Task.isCancelled else { return }
            problem = model.describe(error)
        }
    }
}

private struct MessageRow: View {
    @Environment(\.openURL) private var openURL
    let message: DaemonAPI.MessagingMessage
    /// Answer in this message's thread; nil when the conversation takes no reply, or it is in none.
    var replyInThread: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(verbatim: MessagesList.sender(message.sender)).fontWeight(.semibold).lineLimit(1)
                    .help(message.sender.identity)
                if let at = message.date {
                    Text(at, format: .dateTime.month(.abbreviated).day().hour().minute())
                        .appText(.fine).monospacedDigit().foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                if let replyInThread {
                    Button(MessagesList.replyInThread, action: replyInThread)
                        .linkStyle()
                }
                if let link = message.link, let url = URL(string: link) {
                    Button("Original") { openURL(url) }
                        .linkStyle()
                        .help(link)
                }
            }
            // Outside content: Markdown with no base and no remote-image loader, so an image
            // stays its alternative text and nothing is fetched (FR-013).
            MarkdownText(markdown: message.text).textSelection(.enabled)
        }
        .padding(.vertical, 4)
    }
}

/// The sidebar's Messages row (#4934): the title, and under it in grey the unread count and what the newest
/// `message.received` woke, as the Summary and Events rows have a line ("12 unread · woke Fix login bug 4 min ago").
/// The count asks every messaging connector over MCP, so it is read on a timer, and left out while it fails;
/// the events are each machine's newest page. No machine that reads messages: no line.
struct MessagesRow: View {
    @Environment(AppModel.self) private var model
    @State private var unread: Int?
    @State private var last: MessagesList.LastConsequence?

    /// How often the line is read again.
    static let every: Duration = .seconds(60)

    /// What starts the reads again: the machines, a mark as read (#5137), and whether the sessions have been listed.
    private struct Key: Equatable {
        let machines: [String]
        let marked: Int
        let listed: Bool
    }

    var body: some View {
        let machines = model.messagesMachines
        // The time ago moves on between reads.
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let line = machines.isEmpty ? nil : MessagesList.statusLine(unread: unread, last: last, now: context.date)
            VStack(alignment: .leading, spacing: 1) {
                HStack { Text(MessagesList.title).foregroundStyle(.primary); Spacer() }
                if let line {
                    Text(line).appText(.fine).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .accessibilityValue(line ?? "")
        }
        // Not before the sessions' first list (#5173): the line is no reason to keep the list waiting
        // at launch, and the night's run holds the Mac to 2 calls before it (#4891).
        .task(id: Key(machines: machines.map(\.id.rawValue), marked: model.messagesList.marked,
                      listed: model.hasLoadedAgents || !model.hasMacHost)) {
            guard model.hasLoadedAgents || !model.hasMacHost else { return }
            while !Task.isCancelled {
                await read(machines)
                try? await Task.sleep(for: Self.every)
            }
        }
    }

    private func read(_ machines: [(id: MachineRef, name: String)]) async {
        guard !machines.isEmpty else { unread = nil; last = nil; return }
        let model = model
        // One call for every machine's count (#5571); a failed one leaves the count out. This Mac first, so its copy
        // of a conversation is the one counted, as the page keeps it.
        async let all = try? model.messagesAll(DaemonAPI.MessagesConversationsAllRequest(unread: true, limit: 50))
        async let events = Self.newestEvents(machines, model: model)
        let lists = await all.map { MessagesList.hostConversations($0, thisMac: model.thisMacHostID) }
        unread = lists.map { $0.isEmpty ? 0 : MessagesList.unreadTotal($0) ?? 0 }
        last = MessagesList.lastConsequence(await events)
    }

    /// Each machine's newest page of events: every machine's in one call to the control plane (`events/listAll`, #5592);
    /// with none, a call to each. A machine that did not answer gives none. The plane gives at most 100 a machine.
    private static func newestEvents(_ machines: [(id: MachineRef, name: String)], model: AppModel) async -> [Event] {
        let asked = Set(machines.map(\.id))
        do {
            if let all = try await model.controlAll(DaemonAPI.Method.eventsListAll, DaemonAPI.EventsListAllRequest(limit: 100),
                                                    returning: DaemonAPI.EventsListAll.self) {
                return all.pages.filter { asked.contains($0.machine) }.flatMap(\.page.events)
            }
        } catch {
            return []
        }
        var events: [Event] = []
        await withTaskGroup(of: [Event].self) { group in
            for machine in machines {
                group.addTask {
                    let page = try? await model.client(for: machine.id).call(DaemonAPI.Method.eventsList,
                                                                             DaemonAPI.EventsListRequest(),
                                                                             returning: DaemonAPI.EventsPage.self)
                    return page?.events ?? []
                }
            }
            for await page in group { events += page }
        }
        return events
    }
}
