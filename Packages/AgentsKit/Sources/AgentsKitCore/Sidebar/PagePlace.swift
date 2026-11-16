import Foundation

/// What the Mac window's detail column shows (#5784): the page the sidebar picked, and for
/// Work and Docs the place in them, so one history goes along every page, as Finder's and
/// Safari's Back and Forward do.
public struct PagePlace: Hashable, Sendable {
    public var item: SidebarItem
    /// Work's place, while `item` is one of Work's lists.
    public var work: WorkPlace?
    /// Docs' place, while `item` is Docs.
    public var docs: DocsPlace?

    public init(item: SidebarItem, work: WorkPlace? = nil, docs: DocsPlace? = nil) {
        self.item = item
        if case .work = item { self.work = work }
        if item == .docs { self.docs = docs }
    }
}

/// The window's one history (#5784): each place a person goes to, and Back and Forward along
/// them. A place that is gone (an archived session, a removed folder) is skipped.
public struct PageHistory: Hashable, Sendable {
    public private(set) var places = LineageHistory<PagePlace>()

    public init() {}

    public var current: PagePlace? { places.current }

    /// The window came to a place. Back and Forward put back the place they went to, which is
    /// the current one already, so it does not go on again. Work's Previous and Next take the
    /// item's place, so Back goes to the list and not through each item (#3257).
    public mutating func note(_ place: PagePlace) {
        guard place != places.current else { return }
        if let now = places.current, Self.steps(from: now, to: place) {
            places.replace(place)
        } else {
            places.go(place)
        }
    }

    /// Whether `to` is Previous or Next from `from`: one item of a list for another of it.
    static func steps(from: PagePlace, to: PagePlace) -> Bool {
        guard let a = from.work, let b = to.work else { return false }
        return a.fromList && b.fromList && a.kind == b.kind && !a.files && !b.files
    }

    public func canGoBack(isThere: (PagePlace) -> Bool) -> Bool {
        places.places.prefix(max(places.at, 0)).contains(where: isThere)
    }

    public func canGoForward(isThere: (PagePlace) -> Bool) -> Bool {
        places.places.dropFirst(places.at + 1).contains(where: isThere)
    }

    /// Back to the nearest earlier place that is still there, or nil when there is none.
    public mutating func back(isThere: (PagePlace) -> Bool) -> PagePlace? {
        guard canGoBack(isThere: isThere) else { return nil }
        repeat { places.back() } while !(places.current.map(isThere) ?? true)
        return places.current
    }

    /// Forward to the nearest later place that is still there, or nil when there is none.
    public mutating func forward(isThere: (PagePlace) -> Bool) -> PagePlace? {
        guard canGoForward(isThere: isThere) else { return nil }
        repeat { places.forward() } while !(places.current.map(isThere) ?? true)
        return places.current
    }
}
