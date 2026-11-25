import AgentsKitCore
import Foundation

/// Step 7 of a machine's set-up (#5361, epic #5353, "Sign-in", board 2 note 4): each runtime that
/// is not signed in on a new machine signs in with the first thing that works. First this Mac's own
/// sign-in, lent through the relay when this Mac is signed in, with no ask (056, T091,
/// #6855): the token stays on this Mac. Then a key the person has in Secrets,
/// written on the machine as its own user. Else the runtime's device-code sign-in, shown as a card
/// with the code and the link to its page, while the step says it waits for the person. A login is
/// never copied from this Mac.
///
/// It runs while the machine's step 7 is running or waits for the person, from the window that
/// hears of the machine, so the keys are written whether or not the page is open. The sign-ins a
/// card waits on are cancelled on the machine when the step ends.
@MainActor
@Observable
final class MachineSignInDriver {
    /// One runtime that a key did not sign in: what to do instead.
    struct Card: Identifiable, Equatable {
        var id: String { runtimeID }
        let runtimeID: String
        let runtimeName: String
        /// The runtime's own sign-in, with its code and page.
        var step: DaemonAPI.RuntimeSignInStep?
        /// Why no key was used, or what the person does, in a line.
        var note: String?
        /// Why the last try failed.
        var problem: String?
        var busy = false
    }

    private(set) var cards: [MachineRef: [Card]] = [:]
    @ObservationIgnored private var loops: [MachineRef: Task<Void, Never>] = [:]
    @ObservationIgnored private var tried: [MachineRef: Set<String>] = [:]

    /// The runtimes that sign in on a machine from a client: the ones the machine's daemon has a
    /// way for (runtimes/signInKey and runtimes/signInStart).
    static let clientSignIn: Set<String> = ["claude", "codex"]

    /// Called on each list of the control plane's machines: starts the loop for a machine that is at
    /// step 7.
    func follow(_ host: MachineRef, model: AppModel) {
        guard loops[host] == nil, Self.active(model.hosts.controlRows[host]?.setup) else { return }
        loops[host] = Task { [weak self, weak model] in
            guard let self, let model else { return }
            await self.run(host, model: model)
        }
    }

    /// Whether step 7 is on: running, or waiting for the person.
    static func active(_ setup: DaemonAPI.MachineSetup?) -> Bool {
        guard let step = setup?.steps.first(where: { $0.id == "signIn" }) else { return false }
        return step.state == .running || step.state == .needsYou
    }

    private func run(_ host: MachineRef, model: AppModel) async {
        defer {
            loops[host] = nil
            tried[host] = nil
        }
        while !Task.isCancelled, Self.active(model.hosts.controlRows[host]?.setup) {
            let listed = (try? await model.client(for: host).call(DaemonAPI.Method.runtimesList, Optional<String>.none,
                                                                   returning: [RuntimeStatus].self)) ?? []
            let waiting = listed.filter(\.needsSignIn)
            let gone = Set(cards[host, default: []].map(\.runtimeID)).subtracting(waiting.map(\.runtime.id))
            if !gone.isEmpty { cards[host]?.removeAll { gone.contains($0.runtimeID) } }
            for status in waiting where !tried[host, default: []].contains(status.runtime.id) {
                tried[host, default: []].insert(status.runtime.id)
                await attempt(status, on: host, model: model)
            }
            try? await Task.sleep(for: .seconds(3))
        }
        // The step is over: a sign-in that nobody finished is stopped on the machine.
        for card in cards[host] ?? [] {
            if let step = card.step {
                _ = try? await model.client(for: host).call(
                    DaemonAPI.Method.runtimesSignInFinish, DaemonAPI.RuntimeSignInFinish(signInID: step.signInID, cancel: true),
                    returning: RuntimeStatus.self)
            }
        }
        cards[host] = nil
    }

    /// This Mac's sign-in lent first, then a key, then the runtime's own sign-in.
    private func attempt(_ status: RuntimeStatus, on host: MachineRef, model: AppModel) async {
        let id = status.runtime.id, name = status.runtime.name
        var card = Card(runtimeID: id, runtimeName: name)
        if Self.clientSignIn.contains(id) {
            switch await model.lendSignIn(id, to: host) {
            case .lent: return
            // Said above its own sign-in, which is the other way in (#7108).
            case .notLent(let why): card.note = why
            }
        }
        let found = try? await model.macClient.call(DaemonAPI.Method.secretsSignInKey,
                                                    DaemonAPI.SecretSignInKeyRequest(runtimeID: id),
                                                    returning: DaemonAPI.SecretSignInKey.self)
        if let key = found?.value, let secret = found?.name {
            if Self.clientSignIn.contains(id) {
                do {
                    try await model.runtimeSignInKey(id, key: key, on: host)
                    return
                } catch {
                    card.note = "\(secret) from Secrets was not taken: \(Self.words(error))"
                }
            } else {
                card.note = "\(secret) is in Secrets, but \(name) cannot take it from here yet."
            }
        } else if let refused = found?.refused.first {
            card.note = card.note ?? "\(refused) has Ask before use, so it is not used here."
        } else if let looked = found?.looked.first {
            card.note = card.note ?? "No key in Secrets. Put \(looked) there to sign in without a code."
        }
        if Self.clientSignIn.contains(id) {
            do {
                card.step = try await model.runtimeSignInStart(id, on: host)
            } catch {
                card.problem = Self.words(error)
            }
        } else {
            card.problem = "\(name) signs in on the machine from a terminal."
        }
        cards[host, default: []].append(card)
    }

    /// Done on a card: Claude's code, or Codex's once the code was typed there.
    func finish(_ runtimeID: String, on host: MachineRef, code: String?, model: AppModel) async {
        guard let at = cards[host]?.firstIndex(where: { $0.runtimeID == runtimeID }),
              let step = cards[host]?[at].step else { return }
        cards[host]?[at].busy = true
        cards[host]?[at].problem = nil
        do {
            try await model.runtimeSignInFinish(
                .init(signInID: step.signInID, code: step.wantsCode ? code?.trimmingCharacters(in: .whitespacesAndNewlines) : nil),
                on: host)
            cards[host]?.removeAll { $0.runtimeID == runtimeID }
        } catch {
            guard let now = cards[host]?.firstIndex(where: { $0.runtimeID == runtimeID }) else { return }
            cards[host]?[now].busy = false
            cards[host]?[now].problem = Self.words(error)
            // Claude starts again after a code it did not take.
            if step.wantsCode { cards[host]?[now].step = nil }
        }
    }

    /// A new code for a card whose sign-in stopped.
    func restart(_ runtimeID: String, on host: MachineRef, model: AppModel) async {
        guard let at = cards[host]?.firstIndex(where: { $0.runtimeID == runtimeID }) else { return }
        cards[host]?[at].problem = nil
        do {
            let step = try await model.runtimeSignInStart(runtimeID, on: host)
            cards[host]?[at].step = step
        } catch {
            cards[host]?[at].problem = Self.words(error)
        }
    }

    private static func words(_ error: any Error) -> String {
        (error as? JSONRPCError)?.message ?? error.localizedDescription
    }
}
