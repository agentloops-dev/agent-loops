import Foundation
import Testing
@testable import AgentsKitCore

/// A signer's join cards (#6625): the loops CLI asked to join while the Mac window's connection
/// to the control plane was down, and the window showed no card.
@Suite("Lock join cards")
struct LockJoinCardsTests {
    static let window = "6F1C2D3E-0000-4000-8000-000000000001"
    static let later = Date().addingTimeInterval(300)

    static func request(_ id: String, client: String = "6F1C2D3E-0000-4000-8000-0000000000CC", nonce: Data? = nil,
                        expires: Date = later) -> DaemonAPI.LockJoinRequest {
        DaemonAPI.LockJoinRequest(request: id, client: client, name: "loops on a Mac", kind: .mac, key: Data([4]),
                                  commitment: Data(repeating: 1, count: 32), expires: expires, nonce: nonce)
    }

    @Test("the CLI's request on the same Mac is another client's, and shows")
    func theCLIShows() {
        var cards = LockJoinCards()
        cards.heard(Self.request("r1"))
        cards.heard(Self.request("own", client: Self.window.lowercased()))
        cards.heard(Self.request("gone", expires: Date().addingTimeInterval(-1)))
        #expect(cards.open(at: Date(), client: Self.window).map(\.request) == ["r1"])
    }

    @Test("a read that fails keeps the cards, and their nonces")
    func aFailedReadKeeps() {
        var cards = LockJoinCards()
        cards.heard(Self.request("r1"))
        cards.sent(Data([9]), for: "r1")
        cards.reading()
        cards.read(nil)
        #expect(cards.requests.map(\.request) == ["r1"])
        #expect(cards.nonce(for: "r1") == Data([9]))
    }

    @Test("a read finds a request whose notice never came, and drops one that ended")
    func aReadFindsAMissedRequest() {
        var cards = LockJoinCards()
        cards.heard(Self.request("old"))
        cards.sent(Data([9]), for: "old")
        cards.reading()
        cards.read([Self.request("missed")])
        #expect(cards.requests.map(\.request) == ["missed"])
        #expect(cards.nonce(for: "old") == nil)
    }

    @Test("a notice heard while a read is on its way is not undone by it")
    func aNoticeDuringAReadStays() {
        var cards = LockJoinCards()
        cards.heard(Self.request("r1"))
        cards.heard(Self.request("r2"))
        cards.reading()
        // While the read is on its way: r1 has a signer's nonce now, r2 ended, r3 is new.
        cards.heard(Self.request("r1", nonce: Data([7])))
        cards.ended("r2")
        cards.heard(Self.request("r3"))
        // The read was answered before those: it has r1 with no nonce, and r2.
        cards.read([Self.request("r1"), Self.request("r2")])
        #expect(cards.requests.map(\.request) == ["r1", "r3"])
        #expect(cards.requests.first?.nonce == Data([7]))
        // The next read is not held back by the last one's notices.
        cards.reading()
        cards.read([Self.request("r3")])
        #expect(cards.requests.map(\.request) == ["r3"])
    }

    @Test("a request that ends takes its card and its nonce")
    func anEndTakesTheCard() {
        var cards = LockJoinCards()
        cards.heard(Self.request("r1"))
        cards.sent(Data([9]), for: "r1")
        cards.ended("r1")
        #expect(cards.requests.isEmpty)
        #expect(cards.nonce(for: "r1") == nil)
    }
}
