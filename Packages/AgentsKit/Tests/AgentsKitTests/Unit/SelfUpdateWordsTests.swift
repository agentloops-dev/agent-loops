import AgentsKitCore
import Testing

/// A machine's self-update state (#2178, #6114), in the words of `Web/test/selfUpdate.test.mjs`.
@Suite struct SelfUpdateWordsTests {
    private func update(_ state: DaemonAPI.SelfUpdate.State, on: Bool = true, release: String? = "0.91.0",
                        message: String? = nil) -> DaemonAPI.SelfUpdate {
        DaemonAPI.SelfUpdate(autoUpdate: on, state: state, running: "0.90.0", release: release, message: message)
    }

    @Test func eachStateHasItsLine() {
        #expect(SelfUpdateWords.line(update(.upToDate)) == "Up to date.")
        #expect(SelfUpdateWords.line(update(.off)) == "Off. Your control plane runs 0.91.0.")
        #expect(SelfUpdateWords.line(update(.off, release: nil)) == "Off.")
        #expect(SelfUpdateWords.line(update(.downloading)) == "Downloading 0.91.0…")
        #expect(SelfUpdateWords.line(update(.waiting)) == "0.91.0 is ready. It installs when no agent is in a turn.")
        #expect(SelfUpdateWords.line(update(.restarting)) == "Installed 0.91.0. Restarting…")
        #expect(SelfUpdateWords.line(update(.failed, message: "No route.")) == "No route.")
        #expect(SelfUpdateWords.line(update(.cannot)) == "Agent Loops cannot update itself.")
    }

    @Test func theValueIsOnOnlyWhenItUpdatesItselfAndOnlyAFailureIsRed() {
        #expect(SelfUpdateWords.value(update(.upToDate)) == "On")
        #expect(SelfUpdateWords.value(update(.off, on: false)) == "Off")
        #expect(SelfUpdateWords.value(update(.cannot)) == "Off")
        #expect(SelfUpdateWords.failed(update(.failed)))
        #expect(!SelfUpdateWords.failed(update(.cannot)))
        #expect(SelfUpdateWords.noAnswer("devbox") == "devbox can’t be asked about its updates now.")
    }
}
