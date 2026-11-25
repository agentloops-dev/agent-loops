import Foundation
import Testing
@testable import AgentsKitCore

/// A Work page's blocked images and Settings ▸ Image Hosts (#7139, epic #7136).
@MainActor
struct ImageHostsTests {
    /// A machine that answers as `forge/image` does: `redirects` is each URL's hosts, in order.
    @MainActor
    final class Machine {
        var allowed: Set<String> = []
        var redirects: [URL: [String]] = [:]
        var failures: [URL: PageImageAnswer] = [:]
        var refuses: String?
        private(set) var asked: [(url: URL, show: [String])] = []
        private var waiting: [CheckedContinuation<Void, Never>] = []

        func read(_ url: URL, _ show: [String]) -> PageImageAnswer {
            asked.append((url, show))
            defer { wake() }
            var from: String?
            for host in redirects[url] ?? [url.host ?? ""] {
                if !allowed.contains(host), !show.contains(host) { return .blocked(.init(host: host, from: from)) }
                from = host
            }
            return failures[url] ?? .image(Data("bytes".utf8))
        }

        func allow(_ host: String) throws {
            if let refuses { throw JSONRPCError(code: -32602, message: refuses) }
            allowed.insert(host)
        }

        /// Until `count` reads were asked.
        func reads(_ count: Int) async {
            while asked.count < count { await withCheckedContinuation { waiting.append($0) } }
        }

        private func wake() {
            let all = waiting
            waiting = []
            for each in all { each.resume() }
        }
    }

    let machine = Machine()
    let mac = MachineRef(rawValue: "mac-1")
    let pics = URL(string: "https://pics.example.com/a.png")!
    let pics2 = URL(string: "https://pics.example.com/b.png")!
    let graphs = URL(string: "https://graphs.example.org/c.png")!

    func page() -> PageImages<String> {
        PageImages { [machine] url, show, _ in machine.read(url, show) }
            allow: { [machine] host, _ in try machine.allow(host) }
            decode: { String(decoding: $0, as: UTF8.self) }
    }

    /// What a phase is, in one word and its host.
    func words(_ phase: PageImages<String>.Phase) -> String {
        switch phase {
        case .loading: "loading"
        case .image: "image"
        case .blocked(let b, let problem): "blocked \(b.from.map { "\($0) to " } ?? "")\(b.host)\(problem.map { " (\($0))" } ?? "")"
        case .reading(let b): "reading \(b.host)"
        case .failed(let why, let again): "failed \(why) again=\(again)"
        case .none: "none"
        }
    }

    /// After `reads` reads, each has been drawn: their answers come on the main actor, in order.
    func settled(_ reads: Int) async {
        await machine.reads(reads)
        await Task.yield()
    }

    @Test func aBlockedImageIsShownOnceAndBlockedOnTheNextPage() async {
        let one = page()
        one.load(pics, on: mac)
        await settled(1)
        #expect(words(one.phase(pics)) == "blocked pics.example.com")
        one.show(pics)
        #expect(words(one.phase(pics)) == "reading pics.example.com")
        await settled(2)
        #expect(machine.asked.last?.show == ["pics.example.com"])
        #expect(words(one.phase(pics)) == "image")
        #expect(one.shownOnce(pics) == "pics.example.com")
        // The page is read again: it stays, and is not asked for again.
        one.load(pics, on: mac)
        one.load(pics, on: MachineRef(rawValue: "other"))
        #expect(words(one.phase(pics)) == "image")
        // The next page: blocked again.
        let next = page()
        next.load(pics, on: mac)
        await settled(3)
        #expect(machine.asked.count == 3)
        #expect(machine.asked.last?.show == [])
        #expect(words(next.phase(pics)) == "blocked pics.example.com")
    }

    @Test func aRedirectIsBlockedAgainAndOnePressAcceptsOneHost() async {
        machine.redirects[pics] = ["pics.example.com", "cdn.example.net"]
        let page = page()
        page.load(pics, on: mac)
        await settled(1)
        page.show(pics)
        await settled(2)
        #expect(words(page.phase(pics)) == "blocked pics.example.com to cdn.example.net")
        // Always allow adds the redirect's host only, and the image's own is still one press.
        await page.allow("cdn.example.net", from: pics, machineName: "Desk Mac")
        await settled(3)
        #expect(machine.allowed == ["cdn.example.net"])
        #expect(machine.asked.last?.show == ["pics.example.com"])
        #expect(words(page.phase(pics)) == "image")
        #expect(page.shownOnce(pics) == "pics.example.com")
    }

    @Test func alwaysAllowLoadsThePagesOtherImagesFromThatHostOnly() async {
        let page = page()
        for url in [pics, pics2, graphs] { page.load(url, on: mac) }
        await settled(3)
        await page.allow("pics.example.com", from: pics, machineName: "Desk Mac")
        await settled(5)
        #expect(words(page.phase(pics)) == "image")
        #expect(words(page.phase(pics2)) == "image")
        #expect(page.shownOnce(pics) == nil)
        #expect(words(page.phase(graphs)) == "blocked graphs.example.org")
        #expect(machine.asked.count == 5)
    }

    @Test func aRefusedAllowChangesBackAndSaysWhyOnThePressedOne() async {
        machine.refuses = "The list is full (100 hosts)."
        let page = page()
        for url in [pics, pics2] { page.load(url, on: mac) }
        await settled(2)
        await page.allow("pics.example.com", from: pics, machineName: "Desk Mac")
        #expect(words(page.phase(pics)) == "blocked pics.example.com (pics.example.com was not allowed on Desk Mac. The list is full (100 hosts).)")
        #expect(words(page.phase(pics2)) == "blocked pics.example.com")
        #expect(machine.asked.count == 2)
        // On a shown image: its caption stays, with why.
        page.show(pics)
        await settled(3)
        await page.allow("pics.example.com", from: pics, machineName: "Desk Mac")
        #expect(page.shownOnce(pics) == "pics.example.com")
        #expect(page.problem(pics)?.contains("full") == true)
    }

    @Test func aFailureAfterAPressSaysWhyAndOneAsItLoadsIsTheAlternativeText() async {
        machine.failures[pics] = .failed("pics.example.com says it is not there (404)", again: true)
        machine.allowed = ["graphs.example.org"]
        machine.failures[graphs] = .failed("gone", again: true)
        let page = page()
        page.load(pics, on: mac)
        page.load(graphs, on: mac)
        await settled(2)
        #expect(words(page.phase(graphs)) == "none")
        page.show(pics)
        await settled(3)
        #expect(words(page.phase(pics)) == "failed pics.example.com says it is not there (404) again=true")
        machine.failures[pics] = nil
        page.again(pics)
        await settled(4)
        #expect(words(page.phase(pics)) == "image")
        #expect(page.shownOnce(pics) == "pics.example.com")
    }

    @Test func theMachinesFailureSaysWhatItIsByItsCode() throws {
        let data = try JSONValue.encoding(DaemonAPI.ImageBlocked(host: "cdn.example.net", from: "pics.example.com"))
        guard case .blocked(let blocked) = PageImageAnswer(JSONRPCError(code: DaemonAPI.Failure.imageBlocked, message: "x", data: data)) else {
            Issue.record("not blocked")
            return
        }
        #expect(blocked == DaemonAPI.ImageBlocked(host: "cdn.example.net", from: "pics.example.com"))
        guard case .failed(_, again: false) = PageImageAnswer(JSONRPCError(code: DaemonAPI.Failure.imageNotPublic, message: "x")) else {
            Issue.record("a private address can be tried again")
            return
        }
        // An older machine, and any other failure.
        guard case .failed("x", again: true) = PageImageAnswer(JSONRPCError(code: -32000, message: "x")) else {
            Issue.record("not a failure")
            return
        }
    }

    @Test func aLongHostKeepsItsEnd() {
        let long = "a-very-long-tenant-name-0000000000.media-delivery.eu-central.images.example.net"
        #expect(ImageHostsWords.allow(long) == "Always allow …images.example.net")
        #expect(ImageHostsWords.learns(long, machine: "Desk Mac") == "If you show it, this host learns that Desk Mac read it.")
        #expect(ImageHostsWords.allow("pics.example.com") == "Always allow pics.example.com")
        #expect(ImageHostsWords.learns("pics.example.com", machine: "This Mac") == "If you show it, pics.example.com learns that this Mac read it.")
    }

    // MARK: Settings

    @MainActor
    final class Store {
        var kept: (hosts: DaemonAPI.ImageHosts, at: Date)?
        var hosts: [DaemonAPI.ImageHost]
        var refuses = false

        init(_ hosts: [String]) { self.hosts = hosts.map { .init(host: $0, addedAt: Date(timeIntervalSince1970: 0)) } }

        func list() -> ImageHostsList {
            ImageHostsList(.init(kept: { self.kept }, list: { try self.answer() }, remove: { host in
                let list = try self.answer()
                self.hosts.removeAll { $0.host == host }
                _ = list
                return .init(hosts: self.hosts)
            }, keep: { self.kept = ($0, Date()) }))
        }

        private func answer() throws -> DaemonAPI.ImageHosts {
            if refuses { throw JSONRPCError(code: -32000, message: "no") }
            return .init(hosts: hosts)
        }
    }

    @Test func settingsDrawsTheKeptListFirstAndAnOfflineMachinesOnly() async {
        let store = Store(["pics.example.com"])
        let old = Date(timeIntervalSince1970: 100)
        store.kept = (.init(hosts: [.init(host: "old.example.com", addedAt: old)]), old)
        let list = store.list()
        await list.load(live: false)
        #expect(list.hosts?.map(\.host) == ["old.example.com"])
        #expect(list.readAt == old)
        await list.load(live: true)
        #expect(list.hosts?.map(\.host) == ["pics.example.com"])
        #expect(store.kept?.hosts.hosts.map(\.host) == ["pics.example.com"])
    }

    @Test func aRefusedRemoveComesBackInItsPlace() async {
        let store = Store(["a.example.com", "b.example.com"])
        let list = store.list()
        await list.load(live: true)
        store.refuses = true
        await list.remove("a.example.com")
        #expect(list.hosts?.map(\.host) == ["a.example.com", "b.example.com"])
        #expect(list.notRemoved?.host == "a.example.com")
        store.refuses = false
        await list.remove("a.example.com")
        #expect(list.hosts?.map(\.host) == ["b.example.com"])
        #expect(list.notRemoved == nil)
    }

    @Test func anImageFromAChosenHostSaysNotToKeepIt() throws {
        let wire = try JSONValue.encoding(DaemonAPI.ForgeImage(mimeType: "image/png", data: Data(), keep: false))
        #expect(try wire.decode(DaemonAPI.ForgeImage.self).keep == false)
    }
}
