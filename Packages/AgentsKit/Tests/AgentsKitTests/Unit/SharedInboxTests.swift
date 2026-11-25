#if canImport(Darwin)
import CoreGraphics
import Foundation
import ImageIO
import Testing
import UniformTypeIdentifiers
@testable import AgentsKitCore

/// Share ▸ Agents (#4915): what another app shares waits in the app group, by the
/// paperclip's rules, until New session takes it.
@Suite("Shared to Agents")
struct SharedInboxTests {
    private func scratch() throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("shared-inbox-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func picture() throws -> Data {
        let context = try #require(CGContext(data: nil, width: 40, height: 30, bitsPerComponent: 8, bytesPerRow: 0,
                                             space: CGColorSpaceCreateDeviceRGB(),
                                             bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        context.setFillColor(red: 0.2, green: 0.5, blue: 0.8, alpha: 1)
        context.fill(CGRect(x: 0, y: 0, width: 40, height: 30))
        let image = try #require(context.makeImage())
        let data = NSMutableData()
        let destination = try #require(CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(destination, image, nil)
        #expect(CGImageDestinationFinalize(destination))
        return data as Data
    }

    @Test func itemsGoAsThePaperclipWouldPutThem() throws {
        var items = SharedItems()
        items.add(text: "  https://example.com/page \n")
        items.add(text: "   ")
        items.add(picture: try picture(), name: "Screenshot")
        items.add(file: Data("notes".utf8), name: "notes.txt", type: .plainText)
        items.add(file: Data([0, 1, 2]), name: "film.mov", type: .movie)
        #expect(items.texts == ["https://example.com/page"])
        #expect(items.attachments.map(\.displayName) == ["Screenshot", "notes.txt"])
        if case .image(_, let mime, _) = items.attachments.first?.block { #expect(mime == "image/jpeg") } else {
            Issue.record("the picture is not a picture")
        }
        #expect(items.refusals == [PhoneAttachment.file(Data([0, 1, 2]), name: "film.mov", type: .movie)
            .failureSentence])
    }

    @Test func aFileTooBigIsRefusedInThePaperclipsWords() {
        var items = SharedItems()
        let big = items.refuse(name: "big.log", size: 2_000_000, type: .plainText)
        let small = items.refuse(name: "small.log", size: 10, type: .plainText)
        #expect(big)
        #expect(!small)
        #expect(items.refusals == [PhoneAttachment.sizeRefusal(name: "big.log", size: 2_000_000, type: .plainText)?.sentence])
        #expect(items.note == items.refusals.first)
    }

    @Test func sharedWordsComeAfterWhatIsTyped() {
        let items = SharedItems(texts: ["one", "two"])
        #expect(items.text(after: "") == "one\n\ntwo")
        #expect(items.text(after: "Look at this:  ") == "Look at this:\n\none\n\ntwo")
    }

    @Test func severalSharesWaitInOrderAndAreTakenOnce() throws {
        let container = try scratch()
        defer { try? FileManager.default.removeItem(at: container) }
        #expect(!SharedInbox.hasItems(in: container))
        #expect(SharedInbox.add(SharedItems(), into: container) == .nothing)
        #expect(SharedInbox.add(SharedItems(texts: ["first"]), into: container) == nil)
        #expect(SharedInbox.add(SharedItems(texts: ["second"], refusals: ["no"]), into: container) == nil)
        #expect(SharedInbox.hasItems(in: container))
        let taken = try #require(SharedInbox.take(from: container))
        #expect(taken.texts == ["first", "second"])
        #expect(taken.refusals == ["no"])
        #expect(!SharedInbox.hasItems(in: container))
        #expect(SharedInbox.take(from: container) == nil)
    }

    @Test func aShareOverTheCountIsRefusedAndNothingWaitingGoes() throws {
        let container = try scratch()
        defer { try? FileManager.default.removeItem(at: container) }
        let texts = (0..<SharedInbox.most).map { "share \($0)" }
        for text in texts { #expect(SharedInbox.add(SharedItems(texts: [text]), into: container) == nil) }
        #expect(SharedInbox.add(SharedItems(texts: ["over"]), into: container) == .full)
        #expect(SharedInbox.take(from: container)?.texts == texts)
        #expect(SharedInbox.add(SharedItems(texts: ["over"]), into: container) == nil)
    }

    @Test func aShareOverTheSizeIsRefusedAndNothingWaitingGoes() throws {
        let container = try scratch()
        defer { try? FileManager.default.removeItem(at: container) }
        let big = String(repeating: "a", count: SharedInbox.bytesMost)
        let half = String(big.prefix(SharedInbox.bytesMost / 2))
        #expect(SharedInbox.add(SharedItems(texts: [big]), into: container) == .tooBig)
        #expect(SharedInbox.add(SharedItems(texts: [half]), into: container) == nil)
        #expect(SharedInbox.add(SharedItems(texts: [half]), into: container) == .full)
        #expect(SharedInbox.take(from: container)?.texts == [half])
    }

    @Test func aShareAgesOutAndTheNextTakeSaysSo() throws {
        let container = try scratch()
        defer { try? FileManager.default.removeItem(at: container) }
        let start = Date(timeIntervalSince1970: 1_000_000), over = start + SharedInbox.ageLimit + 1
        SharedInbox.add(SharedItems(texts: ["old"]), into: container, now: start)
        SharedInbox.add(SharedItems(texts: ["new"]), into: container, now: over - 60)
        SharedInbox.sweep(in: container, now: start + SharedInbox.ageLimit)
        SharedInbox.sweep(in: container, now: over)
        SharedInbox.sweep(in: container, now: over)
        let url = try #require(SharedInbox.url(in: container))
        #expect(!(try String(contentsOf: url, encoding: .utf8)).contains("\"old\""))
        let taken = try #require(SharedInbox.take(from: container, now: over))
        #expect(taken.texts == ["new"])
        #expect(taken.refusals == [SharedInbox.expiredWords(1)])
    }

    @Test func aFileFromBeforeTheBoundsIsKept() throws {
        let container = try scratch()
        defer { try? FileManager.default.removeItem(at: container) }
        let url = try #require(SharedInbox.url(in: container))
        let old = SharedItems(texts: ["one", "two"])
        try JSONEncoder().encode(old).write(to: url)
        #expect(SharedInbox.add(SharedItems(texts: ["three"]), into: container) == nil)
        #expect(SharedInbox.take(from: container)?.texts == ["one", "two", "three"])

        let big = SharedItems(texts: [String(repeating: "a", count: SharedInbox.bytesMost)])
        try JSONEncoder().encode(big).write(to: url)
        #expect(SharedInbox.add(SharedItems(texts: ["over"]), into: container) == .full)
        #expect(SharedInbox.take(from: container) == big)

        try JSONEncoder().encode(old).write(to: url)
        let late = Date() + SharedInbox.ageLimit + 60
        #expect(SharedInbox.take(from: container, now: late)?.note == SharedInbox.expiredWords(1))
    }

    @Test func forgettingThePairingClearsWhatWaits() throws {
        let container = try scratch()
        defer { try? FileManager.default.removeItem(at: container) }
        SharedInbox.add(SharedItems(texts: ["x"]), into: container)
        SharedInbox.clear(in: container)
        #expect(SharedInbox.take(from: container) == nil)
    }

    @Test func theStartLinkGoesBothWays() {
        #expect(SharedInbox.startURL.absoluteString == "agents://start")
        #expect(AttentionLink.parse(SharedInbox.startURL) == .start)
    }
}

private extension Result where Failure == PhoneAttachment.Refusal {
    var failureSentence: String? {
        if case .failure(let refused) = self { return refused.sentence }
        return nil
    }
}
#endif
