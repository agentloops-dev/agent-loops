import Foundation
import Testing
@testable import AgentsKitCore

/// A warm pool's card, its form and New Machine's Start from, in words (#5366, boards 1, 4 and 5).
@Suite("Warm pools, in words")
struct WarmPoolWordsTests {
    private let now = Date(timeIntervalSince1970: 2_000_000)

    private func pool(ready: Int = 2, settingUp: Int = 1, state: DaemonAPI.WarmPoolState = .keeping, size: String? = "large",
                      median: Double? = 22, claims: Int = 41, empty: Int = 3) -> DaemonAPI.WarmPool {
        .init(id: "p", name: "dev-large", provider: ProviderID(rawValue: "do"), size: size, region: "fra1",
              runtimes: ["claude", "codex"], projects: ["/Users/me/Agents", "/Users/me/website"], minReady: 2, maxSize: 4,
              state: state, ready: ready, settingUp: settingUp,
              claims: .init(count: claims, empty: empty, medianSeconds: median))
    }

    @Test func theCardSaysWhatThePoolHolds() {
        #expect(WarmPoolWords.chip(pool()) == "Healthy")
        #expect(WarmPoolWords.chip(pool(ready: 1)) == "Filling")
        #expect(WarmPoolWords.chip(pool(state: .stopped)) == "Stopped")
        #expect(WarmPoolWords.summary(pool()) == "large · fra1 · claude, codex · projects Agents, website · Agents kept at the latest release")
        #expect(WarmPoolWords.readyLine(pool()) == "ready (min 2, max 4)")
        #expect(WarmPoolWords.claimsLine(pool().claims) == "41 claims · median 22 s · 3 found it empty")
        #expect(WarmPoolWords.claimsLine(nil) == "No claims yet")
        #expect(WarmPoolWords.stateLine(pool()) == nil)
        var waiting = pool()
        waiting.waitingBecause = "Mac is offline, and it has app"
        #expect(WarmPoolWords.stateLine(waiting) == "Waiting to make a machine: Mac is offline, and it has app.")
        #expect(WarmPoolWords.stateLine(pool(state: .offHours))?.hasPrefix("Outside its hours") == true)
    }

    @Test func eachMachineIsALine() {
        let made = now.addingTimeInterval(-(2 * 3600 + 38 * 60))
        #expect(WarmPoolWords.machineLine(.init(name: "pool-a", state: .ready, madeAt: made), now: now) == "pool-a · ready 2 h 38 min")
        #expect(WarmPoolWords.machineLine(.init(name: "pool-c", state: .settingUp, madeAt: now.addingTimeInterval(-41),
                                                detail: "installing Claude"), now: now) == "pool-c · installing Claude · 41 s")
        #expect(WarmPoolWords.machineLine(.init(name: "pool-d", state: .failed, madeAt: made, detail: "quota"), now: now)
            == "pool-d · failed: quota")
    }

    @Test func costsComeFromTheProvidersPrice() {
        #expect(WarmPoolWords.hourly("$0.19/hour") == 0.19)
        #expect(WarmPoolWords.hourly("about $1.5 an hour") == 1.5)
        #expect(WarmPoolWords.hourly(nil) == nil)
        #expect(WarmPoolWords.idleCost(pool(), hourly: 0.19) == "Idle ones cost about $0.57 an hour")
        #expect(WarmPoolWords.idleCost(pool(), hourly: nil) == "Cost not known")
        #expect(WarmPoolWords.formCost(minReady: 2, maxSize: 4, hourly: 0.19)
            == "Min 2: about $0.38 an hour · at the max of 4: $0.76 an hour")
    }

    @Test func theFormRefusesMinAboveMax() {
        #expect(WarmPoolWords.formProblem(name: "dev", minReady: 3, maxSize: 2) == "Min ready (3) is above max size (2).")
        #expect(WarmPoolWords.formProblem(name: " ", minReady: 1, maxSize: 2) == "A warm pool needs a name.")
        #expect(WarmPoolWords.formProblem(name: "dev", minReady: 0, maxSize: 0) == "Max size is at least 1.")
        #expect(WarmPoolWords.formProblem(name: "dev", minReady: 0, maxSize: 1) == nil)
    }

    @Test func startFromOffersPoolsOfTheSameSizeReadyFirst() {
        let empty = { var p = pool(ready: 0); p.id = "e"; return p }()
        let all = [empty, pool(), pool(size: "small"), pool(state: .stopped)]
        let got = WarmPoolWords.matching(all, provider: ProviderID(rawValue: "do"), size: "large")
        #expect(got.map(\.id) == ["p", "e"])
        #expect(WarmPoolWords.matching(all, provider: ProviderID(rawValue: "aws"), size: "large").isEmpty)
        #expect(WarmPoolWords.startFromLine(pool()) == "2 ready · about 22 s")
        #expect(WarmPoolWords.startFromLine(pool(median: nil)) == "2 ready · in seconds")
        #expect(WarmPoolWords.startFromLine(empty) == "none ready now · a new machine instead")
    }

    @Test func stepsDoneInThePoolSaySoAndWhen() {
        let made = now.addingTimeInterval(-(2 * 3600 + 38 * 60))
        let create = DaemonAPI.MachineSetupStep(id: "create", state: .done, startedAt: made.addingTimeInterval(-40), endedAt: made,
                                                fromPool: true)
        #expect(MachineSetupWords.line(create, now: now).hasPrefix("In the pool since "))
        #expect(MachineSetupWords.line(create, now: now).hasSuffix("(2 h 38 min ago)"))
        #expect(MachineSetupWords.time(create, now: now) == "pool")
        let join = DaemonAPI.MachineSetupStep(id: "join", state: .done, fromPool: true)
        #expect(MachineSetupWords.line(join, now: now) == "In the pool")
        let runtimes = DaemonAPI.MachineSetupStep(id: "runtimes", state: .done, detail: "Claude 2.1.9", fromPool: true)
        #expect(MachineSetupWords.line(runtimes, now: now) == "Claude 2.1.9")
    }

    @Test func aPoolsMachineIsKnownByItsName() {
        #expect(WarmPoolWords.isPoolMachine("dev-large-f00196", pools: ["dev-large"]))
        #expect(WarmPoolWords.isPoolMachine("dev-large-f00196", pools: ["Dev Large!"]))
        #expect(WarmPoolWords.isPoolMachine("pool-2026-a1b2c3", pools: ["2026"]))
        #expect(!WarmPoolWords.isPoolMachine("dev-large-1", pools: ["dev-large"]))
        #expect(!WarmPoolWords.isPoolMachine("machine-3-abcdef", pools: ["dev-large"]))
        #expect(WarmPoolWords.slug(String(repeating: "a", count: 23) + " b") == String(repeating: "a", count: 23))
    }
}
