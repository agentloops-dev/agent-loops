import Foundation

/// The one lookup that every conversation window shares (#7075): the windows opened again at launch wait on the same
/// pages of messages/conversationsAll, and no page is read after each has found its key. A window alone reads only as
/// many pages as it needs.
@MainActor
public final class ConversationLookup {
    public typealias Page = (lists: [MessagesList.HostConversations], cursor: String?)

    /// Reads the page at the cursor (nil: the first), added to the pages before.
    public typealias ReadPage = @MainActor (_ cursor: String?, _ into: [MessagesList.HostConversations]) async -> Page

    /// One read of the pages from the first, for one set of machines.
    @MainActor
    public final class Sweep {
        fileprivate let machines: [MachineRef]
        fileprivate let began: Date
        fileprivate var waiters = 0
        fileprivate var pages = 0
        fileprivate var cursor: String?
        fileprivate var reading: Task<Void, Never>?
        fileprivate var rows: [String: MessagesList.Row] = [:]
        public fileprivate(set) var lists: [MessagesList.HostConversations] = []

        fileprivate init(machines: [MachineRef], began: Date) {
            self.machines = machines
            self.began = began
        }

        fileprivate var hasMore: Bool { pages == 0 || (cursor != nil && pages < MessagesList.mostPagesToFind) }

        /// The next page, joined when it is already being read. It goes on when the window that began it closes: the
        /// others wait on it.
        fileprivate func next(_ read: @escaping ReadPage) async {
            if let reading { return await reading.value }
            let task = Task {
                let page = await read(cursor, lists)
                lists = page.lists
                cursor = page.cursor
                pages += 1
                rows = Dictionary(MessagesList.merged(page.lists).map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
                reading = nil
            }
            reading = task
            await task.value
        }
    }

    public enum Answer: Sendable {
        case found(MessagesList.Row)
        /// Not in the pages: what they said.
        case missing(Sweep)
        case cancelled
    }

    /// How long the pages stay for a window that asks after the others are done: the windows of one launch do not all
    /// ask at the same moment.
    static let keep: TimeInterval = 60

    private var sweep: Sweep?
    private let now: () -> Date

    public init(now: @escaping () -> Date = Date.init) { self.now = now }

    /// The conversation `key` on `machines`. `known` is asked before each page, so a row the window got another way ends
    /// the lookup. `after` is the sweep that missed it, for Try Again: that one is not asked again, but a newer one that
    /// another window began is joined.
    public func find(_ key: String, machines: [MachineRef], after: Sweep? = nil,
                     known: () -> MessagesList.Row? = { nil }, read: @escaping ReadPage) async -> Answer {
        let sweep = current(machines, after: after)
        sweep.waiters += 1
        defer { sweep.waiters -= 1 }
        while true {
            if Task.isCancelled { return .cancelled }
            if let row = known() ?? sweep.rows[key] { return .found(row) }
            guard sweep.hasMore else { return .missing(sweep) }
            await sweep.next(read)
        }
    }

    private func current(_ machines: [MachineRef], after: Sweep?) -> Sweep {
        if let sweep, sweep !== after, sweep.machines == machines,
           sweep.waiters > 0 || now().timeIntervalSince(sweep.began) < Self.keep { return sweep }
        let fresh = Sweep(machines: machines, began: now())
        sweep = fresh
        return fresh
    }
}
