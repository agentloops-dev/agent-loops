import Foundation
import Testing
@testable import AgentsKitCore

/// Project Settings ▸ Autopilot (#5904): the lines, as the web page says them.
struct AutopilotWordsTests {
    @Test func turningItOnFromOffAsksOnce() {
        #expect(AutopilotWords.needsConfirm(from: .off, to: .on))
        #expect(AutopilotWords.needsConfirm(from: .off, to: .high))
        #expect(!AutopilotWords.needsConfirm(from: .high, to: .on))
        #expect(!AutopilotWords.needsConfirm(from: .on, to: .off))
    }

    @Test func theLastRunLineCountsWhatStartedAndWaits() {
        let ran = Date(timeIntervalSinceReferenceDate: 100)
        func row(_ state: DaemonAPI.AutopilotRow.State) -> DaemonAPI.AutopilotRow {
            .init(ref: "#1", kind: .lane, state: state, since: ran)
        }
        let status = DaemonAPI.AutopilotStatus(mode: .on, lastRunAt: ran, rows: [row(.started), row(.waiting), row(.waiting)])
        let now = ran.addingTimeInterval(120)
        #expect(AutopilotWords.lastRunLine(status, now: now) == "Last run 2 min ago · 1 started, 2 waiting")
        #expect(AutopilotWords.lastRunLine(.init(mode: .on), now: now) == AutopilotWords.never)
    }

    @Test func theStatusDecodesFromTheWire() throws {
        let json = ##"{"mode":"high","lastRunAt":100,"rows":[{"ref":"#7","kind":"lead","state":"paused","reason":"on hold","since":50}]}"##
        let status = try JSONDecoder().decode(DaemonAPI.AutopilotStatus.self, from: Data(json.utf8))
        #expect(status.mode == .high)
        #expect(status.rows.first?.state == .paused)
    }
}
