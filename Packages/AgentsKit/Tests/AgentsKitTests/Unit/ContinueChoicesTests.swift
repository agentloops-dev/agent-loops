import Foundation
import Testing
@testable import AgentsKitCore

/// The other accounts a chat whose allowance ran out can continue on (#7459), as the Mac and the
/// Remote offer them. `Web/test/runtimes.test.mjs` checks the same rule on the web page.
@Suite("Continue on another account")
struct ContinueChoicesTests {
    private let now = Date(timeIntervalSince1970: 1_790_000_000)

    private func row(_ account: String?, out: Bool = false) -> RuntimeAllowances.Row {
        let key = account.map { "claude:account:\($0)" } ?? "claude:sign-in"
        var state = AllowanceState(credentialKey: key, entryID: UUID(), since: now)
        if out { state.status = .out(until: nil, retryAfter: nil, why: .allowanceSpent) }
        return RuntimeAllowances.Row(credentialKey: key, state: state, accountID: account)
    }

    private var list: [AccountProfile] {
        [AccountProfile(id: "work", runtimeID: "claude", name: "Work", reach: .signIn(folder: "/w"), signedIn: true),
         AccountProfile(id: "personal", runtimeID: "claude", name: "Personal", reach: .signIn(folder: "/p"), signedIn: true),
         AccountProfile(id: "new", runtimeID: "claude", name: "New", reach: .signIn(folder: "/n")),
         AccountProfile(id: "spent", runtimeID: "claude", name: "Spent", reach: .signIn(folder: "/s"), signedIn: true)]
    }

    private var allowances: RuntimeAllowances {
        RuntimeAllowances(rows: [row(nil), row("work", out: true), row("personal"), row("new"), row("spent", out: true)], at: now)
    }

    @Test func offersTheOwnSignInAndEachUsableAccountItIsNotOn() {
        let choices = AccountChoices(runtimeID: "claude", profiles: list).continueChoices(from: "work", allowances: allowances)
        #expect(choices == [ContinueChoice(accountID: "default", title: "Claude · Your Own Sign-In"),
                            ContinueChoice(accountID: "personal", title: "Claude · Personal")])
        #expect(AccountWords.continueOn("Claude · Personal") == "Continue on Claude · Personal")
    }

    @Test func onItsOwnSignInOffersOnlyTheAddedOnes() {
        let choices = AccountChoices(runtimeID: "claude", profiles: list).continueChoices(from: nil, allowances: allowances)
        #expect(choices.map(\.accountID) == ["personal"])
    }

    @Test func nothingBeforeTheAllowancesAreKnown() {
        #expect(AccountChoices(runtimeID: "claude", profiles: list).continueChoices(from: "work", allowances: nil).isEmpty)
    }
}
