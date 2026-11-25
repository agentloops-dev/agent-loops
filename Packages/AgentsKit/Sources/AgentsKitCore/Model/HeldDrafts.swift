import Foundation

/// The drafts of one app: those its `DraftStore` keeps, and those it refused (#6794), held
/// while the app runs, as the web page's `Drafts` holds them. Room saves a refused draft.
public struct HeldDrafts {
    /// Said when a draft is refused, once for each. The web page's `draftsFull`.
    public static let full = "This draft is not saved: \(DraftStore.draftsKept) drafts are kept already. "
        + "It stays until Agent Loops closes. Send or empty another draft to save it."
    public static let tooBig = "This draft is not saved: it is over 1 MB. It stays until Agent Loops closes. "
        + "Send it, or make it shorter to save it."
    /// Said when the start form is refused, once until it fits (#7252).
    public static let formTooBig = "The new session's choices are not saved: they are over 1 MB."
    /// A full store looks for drafts past their 30 days at most this often.
    static let sweepEvery: TimeInterval = 60 * 60

    private let store: DraftStore
    private var refused: [DraftKey: Draft] = [:]
    private var sweptAt: Date?

    public init(store: DraftStore) { self.store = store }

    public func draft(for key: DraftKey) -> Draft? { refused[key] ?? store.draft(for: key) }

    /// Keep what changed, and answer what to say: nil unless a draft was refused that was not
    /// before.
    public mutating func save(_ drafts: [DraftKey: Draft], now: Date = Date()) -> String? {
        // The emptied ones first: each makes room.
        for (key, draft) in drafts where draft.isEmpty {
            refused[key] = nil
            store.clear(key)
        }
        // The longest refused first, then the new ones as they were typed.
        let typed = drafts.filter { !$0.value.isEmpty }
        let waited = refused.keys.filter { typed[$0] == nil }
        var said: String?
        for (key, draft) in refused.filter({ waited.contains($0.key) }).sorted(by: { $0.value.editedAt < $1.value.editedAt })
            + typed.sorted(by: { $0.value.editedAt < $1.value.editedAt }) {
            let first = refused[key] == nil
            let saved = keep(draft, for: key, now: now)
            refused[key] = saved == .kept ? nil : draft
            switch saved {
            case .full where first: said = Self.full
            case .tooBig where first: said = Self.tooBig
            default: break
            }
        }
        return said
    }

    /// It became a prompt, or its agent was archived.
    public mutating func clear(_ key: DraftKey) {
        refused[key] = nil
        store.clear(key)
        retry()
    }

    /// There may be room: what was refused is saved.
    public mutating func retry() {
        if !refused.isEmpty { _ = save([:]) }
    }

    public mutating func clearAll() {
        refused = [:]
        store.clearAll()
    }

    private mutating func keep(_ draft: Draft, for key: DraftKey, now: Date) -> DraftStore.Saved {
        let saved = store.save(draft, for: key)
        guard saved == .full, sweptAt.map({ now.timeIntervalSince($0) >= Self.sweepEvery }) ?? true else { return saved }
        sweptAt = now
        store.sweep(archived: [], now: now)
        return store.save(draft, for: key)
    }
}
