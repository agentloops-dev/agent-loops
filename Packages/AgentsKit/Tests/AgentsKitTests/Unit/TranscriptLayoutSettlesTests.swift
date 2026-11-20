import Foundation
import Testing
@testable import AgentsKitCore

/// The Mac chat's layout settles at the sizes and timings that looped it (#6189; #5189, #5947).
///
/// The window quit when a layout pass asked for one more, again and again: an animated top inset
/// (#5189, 1058x662, presses 0.3 to 0.6 s apart while the agent worked) and a toolbar built from a
/// measured width (#5947, 1470x834). Neither loop came back on a scratch window, and no test target
/// hosts the window, so this lays the transcript out as `TranscriptStack` and `ChatTranscript` do,
/// with the same `TranscriptGeometry`, frame by frame: the rows drawn are measured, then placed again,
/// and a frame is settled when a pass changes nothing. It fails when a frame takes more than a few
/// passes. The toolbar half of #5947 is `ToolbarLayoutLoopTests`' source scan.
@Suite("Transcript layout settles")
struct TranscriptLayoutSettlesTests {
    struct Window: CustomTestStringConvertible, Sendable {
        var width: Double
        var height: Double
        var testDescription: String { "\(Int(width))x\(Int(height))" }
    }

    static let windows = [Window(width: 1058, height: 662), Window(width: 1470, height: 834)]

    @Test(arguments: windows)
    func followingSettlesEachFrame(_ window: Window) {
        var chat = Chat(window: window)
        chat.run(seconds: 8)
        #expect(chat.unsettled == [], "frames that did not settle")
        #expect(chat.mostPasses <= Chat.passesAllowed)
        #expect(chat.geometry.anchor == nil, "following: no reader's place is held")
    }

    /// A reader's place holds while rows above them are measured for the first time, the top inset
    /// opens and closes, and the reply streams below: nothing on screen moves under them.
    @Test(arguments: windows)
    func aReadersPlaceHolds(_ window: Window) throws {
        var chat = Chat(window: window)
        chat.run(seconds: 1)
        chat.read(back: 6_000)
        let held = try #require(chat.geometry.anchor)
        let onScreen = try #require(chat.geometry.top(of: held.id)) - chat.offset
        chat.run(seconds: 7)
        #expect(chat.unsettled == [], "frames that did not settle")
        let now = try #require(chat.geometry.top(of: held.id)) - chat.offset
        #expect(abs(now - onScreen) < 0.5, "the row at the top of the screen moved by \(now - onScreen)")
    }

    /// A chat as the Mac window lays it out: the transcript under a toolbar and the top inset (the Now
    /// card), its rows' heights set by their text and the pane's width.
    struct Chat {
        static let passesAllowed = 3
        static let frame = 1.0 / 60
        /// The plan card's height closed and open, and how long a press took to open it, animated:
        /// the worse case, as before #5189's fix.
        static let insetClosed = 56.0
        static let insetOpen = 56.0 + 6 * 26
        static let animation = 0.35

        let geometry = TranscriptGeometry()
        let width: Double
        let windowHeight: Double
        var ids: [UUID] = []
        var characters: [Int] = []
        var offset = 0.0
        var following = true
        var drawnSpan: ClosedRange<Double> = 0...0
        var time = 0.0
        var inset = insetClosed
        var insetFrom = insetClosed
        var insetTo = insetClosed
        var pressedAt = -10.0
        var nextPress = 0.5
        var presses = 0
        var unsettled: [String] = []
        var mostPasses = 0
        var random: UInt64 = 6189

        init(window: Window) {
            // The sidebar takes 260 pt, the toolbar 52.
            width = window.width - 260
            windowHeight = window.height - 52
            geometry.reset(slack: TranscriptGeometry.reserve)
            for _ in 0..<400 { addRow() }
        }

        var pane: Double { windowHeight - inset - 64 }

        mutating func addRow() {
            ids.append(UUID())
            random = random &* 6364136223846793005 &+ 1442695040888963407
            characters.append(40 + Int(random >> 33) % 2_400)
        }

        /// A row's height at `width`: its lines of text, at 7 pt a character and 17 pt a line.
        func height(_ index: Int) -> Double {
            let perLine = max(1, Int((width - 48) / 7))
            return 20 + Double((characters[index] + perLine - 1) / perLine) * 17
        }

        mutating func read(back by: Double) {
            following = false
            offset -= by
            drawnSpan = (offset - pane)...(offset + 2 * pane)
            // As `ChatTranscript.moved`: the pane moved, so the row at the top holds the place
            // before the next layout.
            geometry.hold(from: offset, to: offset + pane)
            frame()
        }

        mutating func run(seconds: Double) {
            let end = time + seconds
            while time < end {
                time += Self.frame
                // The plan row pressed 0.3, then 0.6 s after the last press, again and again.
                if time >= nextPress {
                    presses += 1
                    insetFrom = inset
                    insetTo = insetTo == Self.insetClosed ? Self.insetOpen : Self.insetClosed
                    pressedAt = time
                    nextPress = time + (presses % 2 == 0 ? 0.6 : 0.3)
                }
                let progress = min(1, (time - pressedAt) / Self.animation)
                inset = insetFrom + (insetTo - insetFrom) * progress
                // The reply streams a few words every frame or two, and a turn ends every second.
                if Int(time / Self.frame) % 2 == 0 { characters[characters.count - 1] += 30 }
                if Int(time / Self.frame) % 60 == 0 { addRow() }
                frame()
            }
        }

        /// One frame: passes until one changes nothing, as AppKit lays the window out until it is
        /// done, then the pane moves as `ChatTranscript.moved` moves it.
        mutating func frame() {
            var passes = 0
            while true {
                passes += 1
                let placed = geometry.place(ids)
                let span = following ? (placed.height - pane * 1.5)...placed.height : drawnSpan
                for index in TranscriptPlacement.window(tops: placed.tops, heights: geometry.heights,
                                                        from: span.lowerBound, to: span.upperBound) {
                    geometry.measure(ids[index], height(index), width: width)
                }
                if geometry.place(ids) == placed { break }
                if passes > 20 { break }
            }
            mostPasses = max(mostPasses, passes)
            if passes > Self.passesAllowed { unsettled.append(String(format: "%.2f s: %d passes", time, passes)) }
            if geometry.shift > 0 {
                offset += geometry.shift
                drawnSpan = (drawnSpan.lowerBound + geometry.shift)...(drawnSpan.upperBound + geometry.shift)
                geometry.shift = 0
            }
            if following {
                offset = geometry.height - pane
            } else {
                geometry.hold(from: offset, to: offset + pane)
                if offset - pane / 2 < drawnSpan.lowerBound || offset + pane * 1.5 > drawnSpan.upperBound {
                    drawnSpan = (offset - pane)...(offset + 2 * pane)
                }
            }
        }
    }
}
