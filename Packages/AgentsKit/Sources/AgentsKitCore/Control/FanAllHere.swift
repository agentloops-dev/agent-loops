import Foundation

/// Each machine's answer, asked by this device itself, when it is in its lock (#5996).
///
/// The control plane's one call for every machine (#5571, #5592) asks each machine in its own name, with no
/// signature, and a machine that holds a lock refuses that: it takes a read only on a channel a device of the lock
/// opened (specs/3399-machine-lock, D6). So in a lock the window asks each machine itself, over its own checked
/// channel (`LockedLink`), and makes the same answer the control plane would: the same machines, bounds and order,
/// so no caller changes. The web page's `answerHere` (`Web/src/model/fanAll.ts`) is the same.
public enum FanAllHere {
    /// The most machines one call asks, as the control plane's mostFanMachines.
    public static let mostMachines = 50
    /// How long a machine has to answer, as the control plane's fanPatience.
    public static let patience: Duration = .seconds(5)

    /// The control plane's calls that ask every machine of the person's.
    public static let methods: Set<String> = [
        "forge/listAll", "forge/boardsAll", "lineage/forecastAll", "reports/featuresAll", "events/listAll",
        "connections/listAll", "docs/listAll", "messages/conversationsAll",
    ]

    /// A machine this device may ask.
    public struct Host: Sendable, Equatable {
        public var id: String
        public var name: String
        public init(id: String, name: String) {
            self.id = id
            self.name = name
        }

        public init(_ machine: DaemonAPI.ControlMachine) {
            self.init(id: machine.id.rawValue, name: machine.name)
        }
    }

    /// One machine's answer, or nil when it did not answer, or answered with an error.
    public typealias Ask = @Sendable (_ method: String, _ params: JSONValue, _ host: String) async -> JSONValue?

    /// A bad request, as the control plane says it.
    public struct Invalid: LocalizedError {
        public var errorDescription: String?
    }

    /// The answer to one of `methods`, made here, as JSON; nil for another method. `hosts` is this device's
    /// machines that have a capability.
    public static func answer(_ method: String, _ params: JSONValue, hosts: @Sendable ([String]) -> [Host],
                              ask: @escaping Ask) async throws -> JSONValue? {
        let p = params.objectValue ?? [:]
        func each(_ capability: String, _ method: String, _ key: String,
                  params: @escaping @Sendable (Host) -> JSONValue) async -> JSONValue {
            let (asked, notAsked) = bounded(hosts([capability]))
            let got = await askEach(asked, method, params: params, ask: ask)
            return .object([
                key: .array(got.items.map { .object(machine($0.host).merging([answerKey(key): $0.item]) { a, _ in a }) }),
                "unreached": .array(got.unreached.map { .object(machine($0)) }),
                "notAsked": .int(notAsked),
            ])
        }
        switch method {
        case "forge/listAll":
            let kind = p["kind"]?.stringValue
            guard kind == "issues" || kind == "pulls" else { throw Invalid(errorDescription: "kind is issues or pulls.") }
            let board = p["board"]?.stringValue ?? ""
            // Only a machine that knows boards is asked for a board's list, as one that does not would list it all (#3282).
            var (asked, notAsked) = bounded(hosts([board.isEmpty ? Capability.forge1 : Capability.forgeBoards1]))
            var cursors: [String: String] = [:]
            if let pages = p["pages"]?.arrayValue {
                for page in pages {
                    if let m = page["machine"]?.stringValue { cursors[m] = page["cursor"]?.stringValue ?? "" }
                }
                asked = asked.filter { cursors[$0.id] != nil }
                notAsked = 0
            }
            let limit = limitOf(p["limit"], byDefault: 50, most: 100)
            let state = p["state"], boardValue = p["board"], label = p["label"], assignee = p["assignee"], sort = p["sort"]
            let known = cursors
            let got = await askEach(asked, "forge/\(kind!)", params: { host in
                var ask: [String: JSONValue] = ["limit": .int(limit)]
                if let state { ask["state"] = state }
                if let boardValue { ask["board"] = boardValue }
                // A label, an assignee and the sort go to each machine, so a narrowed list is one call (#6383, #6634).
                if let label { ask["label"] = label }
                if let assignee { ask["assignee"] = assignee }
                if let sort { ask["sort"] = sort }
                if let c = known[host.id], !c.isEmpty { ask["cursor"] = .string(c) }
                return .object(ask)
            }, ask: ask)
            return .object([
                "lists": .array(got.items.map { .object(machine($0.host).merging(["list": $0.item]) { a, _ in a }) }),
                "unreached": .array(got.unreached.map { .object(machine($0)) }),
                "notAsked": .int(notAsked),
            ])
        case "forge/boardsAll":
            return await each(Capability.forgeBoards1, "forge/boards", "boards") { _ in .object([:]) }
        case "lineage/forecastAll":
            return await each(Capability.lineageForecast1, "lineage/forecast", "forecasts") { _ in params }
        case "reports/featuresAll":
            return await each(Capability.features1, "reports/features", "usage") { _ in .object([:]) }
        case "events/listAll":
            let limit = limitOf(p["limit"], byDefault: 50, most: 100)
            return await each(Capability.events1, "events/list", "pages") { _ in .object(["limit": .int(limit)]) }
        case "connections/listAll":
            return await each(Capability.connections1, "connections/list", "lists") { _ in .object([:]) }
        case "docs/listAll":
            let limit = limitOf(p["limit"], byDefault: 200, most: 500)
            let source = p["source"]
            return try await merged(hosts([Capability.docs1]), cursor: p["cursor"]?.stringValue, limit: limit,
                                    method: "docs/list", itemsKey: "docs", extraKey: "sources",
                                    params: { page in
                                        var ask: [String: JSONValue] = ["limit": .int(limit)]
                                        if let source { ask["source"] = source }
                                        if let page { ask["cursor"] = .string(page) }
                                        return .object(ask)
                                    },
                                    outItems: "docs", outItem: "doc", outExtra: "sources", outExtraItem: "source",
                                    newer: { ($0["updated"].flatMap(number) ?? 0) > ($1["updated"].flatMap(number) ?? 0) },
                                    ask: ask)
        case "messages/conversationsAll":
            let limit = limitOf(p["limit"], byDefault: 20, most: 100)
            let unread = p["unread"]
            return try await merged(hosts([Capability.messages1]), cursor: p["cursor"]?.stringValue, limit: limit,
                                    method: "messages/conversations", itemsKey: "conversations", extraKey: "errors",
                                    params: { page in
                                        var ask: [String: JSONValue] = ["limit": .int(limit)]
                                        if let unread { ask["unread"] = unread }
                                        if let page { ask["cursor"] = .string(page) }
                                        return .object(ask)
                                    },
                                    outItems: "conversations", outItem: "conversation", outExtra: "errors", outExtraItem: "error",
                                    newer: {
                                        ($0["conversation"]?["lastActivity"]?.stringValue ?? "")
                                            > ($1["conversation"]?["lastActivity"]?.stringValue ?? "")
                                    },
                                    ask: ask)
        default:
            return nil
        }
    }

    /// The key each machine's answer goes under in a `…All` answer, by the list's key.
    private static func answerKey(_ key: String) -> String {
        switch key {
        case "boards": "boards"
        case "forecasts": "forecast"
        case "usage": "usage"
        case "pages": "page"
        default: "connections"
        }
    }

    private static func machine(_ host: Host) -> [String: JSONValue] {
        ["machine": .string(host.id), "machineName": .string(host.name)]
    }

    private static func number(_ value: JSONValue) -> Double? {
        switch value {
        case .int(let v): Double(v)
        case .double(let v): v
        default: nil
        }
    }

    private static func limitOf(_ said: JSONValue?, byDefault: Int, most: Int) -> Int {
        guard let said = said.flatMap(number), said >= 1 else { return byDefault }
        return min(Int(said), most)
    }

    private static func bounded(_ hosts: [Host]) -> ([Host], Int) {
        (Array(hosts.prefix(mostMachines)), max(0, hosts.count - mostMachines))
    }

    /// One machine's answer within `patience`; nil when it did not answer in time. Every machine is asked, whatever
    /// state this device last heard for it, as the control plane asks them: one that is not there fails at once, and
    /// one that just joined the lock may not have said so to this device yet.
    ///
    /// The answer goes at `patience` also when the ask does not stop on a cancel (#6638): a machine that does not answer
    /// holds a call until its link's own time-out, about 40 s, and a task group waits for every child before it ends,
    /// so a frozen machine held every machine's answer that long. The ask is cancelled and left to end by itself.
    static func askOne(_ host: Host, _ method: String, _ params: JSONValue, patience: Duration = FanAllHere.patience,
                       ask: @escaping Ask) async -> JSONValue? {
        let first = FirstAnswer()
        let asking = Task { await first.give(await ask(method, params, host.id)) }
        let waiting = Task {
            try? await Task.sleep(for: patience)
            await first.give(nil)
        }
        let answer = await withTaskCancellationHandler {
            await first.value()
        } onCancel: {
            Task { await first.give(nil) }
        }
        asking.cancel()
        waiting.cancel()
        return answer
    }

    /// The first of an ask and its time-out: `value` ends at the first `give`, and the later one is dropped.
    private actor FirstAnswer {
        private var given: JSONValue??
        private var waiter: CheckedContinuation<JSONValue?, Never>?

        func give(_ answer: JSONValue?) {
            if case .some = given { return }
            given = .some(answer)
            waiter?.resume(returning: answer)
            waiter = nil
        }

        func value() async -> JSONValue? {
            if let given { return given }
            return await withCheckedContinuation { waiter = $0 }
        }
    }

    /// Each host's answer at once, in the order of hosts, and those that did not answer.
    private static func askEach(_ hosts: [Host], _ method: String, params: @escaping @Sendable (Host) -> JSONValue,
                                ask: @escaping Ask) async -> (items: [(host: Host, item: JSONValue)], unreached: [Host]) {
        var got = [JSONValue?](repeating: nil, count: hosts.count)
        await withTaskGroup(of: (Int, JSONValue?).self) { group in
            for (i, host) in hosts.enumerated() {
                group.addTask { (i, await askOne(host, method, params(host), ask: ask)) }
            }
            for await (i, answer) in group { got[i] = answer }
        }
        var items: [(host: Host, item: JSONValue)] = []
        var unreached: [Host] = []
        for (i, host) in hosts.enumerated() {
            if let answer = got[i] { items.append((host, answer)) } else { unreached.append(host) }
        }
        return (items, unreached)
    }

    /// Where one machine is in a cursor, as the control plane's fanPlace.
    private struct Place: Codable {
        var p: String?
        var n: Int?
        var d: Bool?
    }

    private static func decodeCursor(_ cursor: String?) throws -> [String: Place] {
        guard let cursor, !cursor.isEmpty else { return [:] }
        guard let data = Data(base64Encoded: cursor), let places = try? JSONDecoder().decode([String: Place].self, from: data) else {
            throw Invalid(errorDescription: "That is not a cursor this answer gave.")
        }
        return places
    }

    /// The control plane's fanOut: each host's page from its place, merged newest first, `limit` of them, with each
    /// machine's `extraKey` (its sources, or its connectors' errors), and the cursor of the next page.
    private static func merged(_ all: [Host], cursor: String?, limit: Int, method: String, itemsKey: String, extraKey: String,
                               params: @escaping @Sendable (String?) -> JSONValue,
                               outItems: String, outItem: String, outExtra: String, outExtraItem: String,
                               newer: (JSONValue, JSONValue) -> Bool, ask: @escaping Ask) async throws -> JSONValue {
        let (hosts, notAsked) = bounded(all)
        var places = try decodeCursor(cursor)
        let known = places
        var answers = [JSONValue?](repeating: nil, count: hosts.count)
        await withTaskGroup(of: (Int, JSONValue?).self) { group in
            for (i, host) in hosts.enumerated() where known[host.id]?.d != true {
                group.addTask { (i, await askOne(host, method, params(known[host.id]?.p), ask: ask)) }
            }
            for await (i, answer) in group { answers[i] = answer }
        }
        let pages = answers.map { $0?[itemsKey]?.arrayValue }
        var next = hosts.enumerated().map { i, h in min(known[h.id]?.n ?? 0, pages[i]?.count ?? 0) }
        var unreached: [Host] = []
        for (i, h) in hosts.enumerated() where known[h.id]?.d != true && pages[i] == nil { unreached.append(h) }
        var items: [JSONValue] = []
        while items.count < limit {
            var best = -1
            for i in hosts.indices {
                guard let page = pages[i], next[i] < page.count else { continue }
                if best < 0 || newer(page[next[i]], pages[best]![next[best]]) { best = i }
            }
            guard best >= 0 else { break }
            items.append(.object(machine(hosts[best]).merging([outItem: pages[best]![next[best]]]) { a, _ in a }))
            next[best] += 1
        }
        var more = false
        for (i, h) in hosts.enumerated() {
            var place = known[h.id] ?? Place()
            if place.d == true {
                place = Place(d: true)
            } else if let page = pages[i] {
                if next[i] < page.count {
                    place.n = next[i]
                } else if let after = answers[i]?["cursor"]?.stringValue, !after.isEmpty {
                    place = Place(p: after)
                } else {
                    place = Place(d: true)
                }
            } else {
                place = Place(d: true)
            }
            places[h.id] = place
            more = more || place.d != true
        }
        var out: [String: JSONValue] = [
            outItems: .array(items),
            outExtra: .array(hosts.enumerated().flatMap { i, h in
                (answers[i]?[extraKey]?.arrayValue ?? []).map { .object(machine(h).merging([outExtraItem: $0]) { a, _ in a }) }
            }),
            "unreached": .array(unreached.map { .object(machine($0)) }),
            "notAsked": .int(notAsked),
        ]
        if more, let data = try? JSONEncoder().encode(places) { out["cursor"] = .string(data.base64EncodedString()) }
        return .object(out)
    }
}
