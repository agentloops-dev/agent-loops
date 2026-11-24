import Foundation
import Testing
@testable import AgentsKitCore

/// A host lost after the Mac slept is quiet for a moment, then says the sleep (#6570).
@Suite("After a sleep")
struct AfterSleepTests {
    let woke = Date(timeIntervalSince1970: 1_000_000)

    @Test func noWakeIsNotASleep() {
        #expect(AfterSleep().verdict(downSince: woke, now: woke) == .awake)
    }

    @Test func aDropJustAfterAWakeIsQuietThenSaysTheSleep() {
        var sleep = AfterSleep()
        sleep.woke(at: woke)
        let down = woke.addingTimeInterval(3)
        #expect(sleep.verdict(downSince: down, now: woke.addingTimeInterval(10)) == .quiet)
        #expect(sleep.verdict(downSince: down, now: woke.addingTimeInterval(31)) == .wasAsleep(woke: woke))
    }

    @Test func aDropFarFromTheWakeIsNotTheSleep() {
        var sleep = AfterSleep()
        sleep.woke(at: woke)
        let later = woke.addingTimeInterval(600)
        #expect(sleep.verdict(downSince: later, now: later) == .awake)
        let before = woke.addingTimeInterval(-600)
        #expect(sleep.verdict(downSince: before, now: woke) == .awake)
    }

    @Test func theClocksSeeADarkWake() {
        var sleep = AfterSleep()
        // The first read only sets where to count from.
        let first = sleep.look(asleepSoFar: .seconds(100), now: woke)
        #expect(!first)
        #expect(sleep.wokeAt == nil)
        // A clock's own jitter is not a sleep.
        let jitter = sleep.look(asleepSoFar: .seconds(101), now: woke)
        #expect(!jitter)
        let dark = woke.addingTimeInterval(900)
        let slept = sleep.look(asleepSoFar: .seconds(990), now: dark)
        #expect(slept)
        #expect(sleep.wokeAt == dark)
    }

    @Test func anOlderWakeDoesNotReplaceANewerOne() {
        var sleep = AfterSleep()
        sleep.woke(at: woke)
        sleep.woke(at: woke.addingTimeInterval(-5))
        #expect(sleep.wokeAt == woke)
    }

    @Test func theQuietEndsThirtySecondsAfterTheWake() {
        var sleep = AfterSleep()
        #expect(sleep.quietEnds(now: woke) == nil)
        sleep.woke(at: woke)
        #expect(sleep.quietEnds(now: woke.addingTimeInterval(1)) == woke.addingTimeInterval(AfterSleep.quiet))
        #expect(sleep.quietEnds(now: woke.addingTimeInterval(AfterSleep.quiet)) == nil)
    }

    @Test func theClocksStartWithNoSleep() {
        #expect(SleepClocks().asleepSoFar < AfterSleep.leastSleep)
    }
}
