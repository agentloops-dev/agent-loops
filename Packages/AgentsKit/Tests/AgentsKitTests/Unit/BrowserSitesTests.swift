import AgentsKitCore
import Foundation
import Testing

/// The bounds of the Mac browser pane's website data (#6796), with no WebKit store.
@Suite("Browser pane sites")
struct BrowserSitesTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func ago(_ days: Double) -> Date { now.addingTimeInterval(-days * 86_400) }

    @Test func aSiteGoesNinetyDaysAfterItWasLastOpened() {
        var sites = BrowserSites()
        let moved1 = sites.open("docs.example.co.uk", at: ago(90))
        #expect(moved1)
        let moved2 = sites.open("www.recent.com", at: ago(89))
        #expect(moved2)
        sites.fold(records: ["example.co.uk", "recent.com"], open: [], now: now)
        #expect(sites.opened == ["example.co.uk": ago(90), "recent.com": ago(89)])
        #expect(sites.expired(open: [], now: now) == ["example.co.uk"])
    }

    @Test func aSiteNotSeenBeforeCountsFromNow() {
        var sites = BrowserSites()
        sites.fold(records: ["old.com"], open: [], now: now)
        #expect(sites.expired(open: [], now: now).isEmpty)
        #expect(sites.expired(open: [], now: now.addingTimeInterval(BrowserSites.Bounds.age)) == ["old.com"])
    }

    @Test func aDateInTheFutureCountsAsNow() {
        var sites = BrowserSites()
        _ = sites.open("a.com", at: now.addingTimeInterval(1e9))
        sites.fold(records: ["a.com"], open: [], now: now)
        #expect(sites.opened == ["a.com": now])
    }

    @Test func aSiteInAnOpenPaneStays() {
        var sites = BrowserSites()
        _ = sites.open("a.com", at: ago(200))
        #expect(sites.expired(open: ["www.a.com"], now: now).isEmpty)
        sites.fold(records: ["a.com"], open: ["www.a.com"], now: now)
        #expect(sites.opened == ["a.com": now])
        #expect(sites.idle(open: ["www.a.com"]).isEmpty)
    }

    @Test func overTheCountTheSiteOpenedLongestAgoGoes() {
        var sites = BrowserSites()
        let names = (0 ..< BrowserSites.Bounds.sites + 2).map { "s\($0).com" }
        for (index, name) in names.enumerated() { _ = sites.open(name, at: ago(Double(index) / 2)) }
        sites.fold(records: names, open: [], now: now)
        let gone = sites.expired(open: [], now: now)
        #expect(gone == ["s100.com", "s101.com"])
        #expect(sites.idle(open: []).first == "s101.com")
        sites.remove(gone)
        #expect(sites.opened.count == BrowserSites.Bounds.sites)
    }

    @Test func theHostsBetweenSweepsAreBoundedAndOldDatesStay() {
        var sites = BrowserSites()
        for index in 0 ..< 1_100 { _ = sites.open("h\(index).com", at: ago(1)) }
        #expect(sites.opened.count == 1_000)
        #expect(sites.opened["h0.com"] == ago(1))
        let moved3 = sites.open("h0.com", at: now)
        #expect(moved3)
        let moved4 = sites.open("h0.com", at: now.addingTimeInterval(60))
        #expect(!moved4)
    }

    @Test func hostsMatchOnlyTheirOwnSite() {
        #expect(BrowserSites.isSite("a.com", of: "x.a.com"))
        #expect(!BrowserSites.isSite("a.com", of: "nota.com"))
        #expect(BrowserSites.isSite("Local documents on your computer", of: ""))
        #expect(!BrowserSites.isSite("a.com", of: ""))
        #expect(BrowserSites.host(of: URL(string: "http://LOCALHOST:3000/")) == "localhost")
        #expect(BrowserSites.host(of: URL(filePath: "/tmp/a.html"))?.isEmpty == true)
        #expect(BrowserSites.host(of: URL(string: "about:blank")) == nil)
    }
}
