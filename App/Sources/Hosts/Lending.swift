import AgentsKitCore
import Foundation

/// A server asked for a sign-in this Mac relays (058, T091): asked of the person, once per
/// server and runtime, before the control plane lets the server reach this Mac's relay.
struct SignInLendAsk: Identifiable {
    let id = UUID()
    let runtimeName: String
    let label: String
    let answer: CheckedContinuation<Bool, Never>
}

/// How a lend of this Mac's sign-in ended. `notLent` says why, when the person should know.
enum SignInLend: Equatable {
    case lent
    case notLent(String?)
}

extension AppModel {
    /// The first git ask no window here has answered yet (#1122). Any client may answer it;
    /// the control plane takes the first answer.
    var gitLendAsk: GitLendAsk? {
        GitLendAsk.first(in: controlPlaneHosts ?? [], thisMac: thisMacHostID, answered: gitAsksAnswered)
    }

    /// Approve or Deny (#1122): said to the control plane, which answers the waiting git.
    func answerGitLend(_ ask: GitLendAsk, allowed: Bool) {
        gitAsksAnswered.insert(ask.id)
        lendGit(host: ask.host, from: ask.from, server: ask.server, allowed: allowed)
    }

    /// A machine's page (#3975): a host stops using a Mac's git access for a server.
    func lendGit(host: MachineRef, from: MachineRef, server: String, allowed: Bool) {
        guard let control = controlPlaneClient() else { return }
        Task {
            defer { Task { await control.disconnect() } }
            try? await control.connect(startIfNeeded: false, timeout: .seconds(5))
            _ = try? await control.call(DaemonAPI.Method.hostsLendGit,
                                        DaemonAPI.LendGit(host: host, from: from, server: server, allowed: allowed))
        }
    }

    /// A server of the control plane wants a sign-in this Mac relays (T091). This Mac's
    /// host starts the relay first, which fails when this Mac is not signed in to the
    /// runtime the way a server can use, so the person is asked only about a sign-in this
    /// Mac can lend (#6855). Then it asks the person the first time, the control plane
    /// lets the server tunnel to this Mac's host, and the server is told where its gate
    /// goes. The token never leaves this Mac (056 D1). `notLent` with no words when this is not
    /// such a case, or the person said no; with words when a lend they allowed failed (#7108).
    func relaySignInThroughControl(_ runtime: String, to id: MachineRef) async -> SignInLend {
        guard reachesThroughControl(id), ToolPolicyCatalog.policy(for: runtime).relay != nil,
              let lender = thisMacHostID, lender != id, let control = controlPlaneClient() else { return .notLent(nil) }
        defer { Task { await control.disconnect() } }
        guard let grant = try? await client(for: lender).call(DaemonAPI.Method.relayGrant,
                                                              DaemonAPI.RelayGrantRequest(runtime: runtime),
                                                              returning: DaemonAPI.RelayGrantReply.self) else { return .notLent(nil) }
        let allowed = controlPlaneHosts?.first { $0.id == id }?.signInFrom?[runtime] == lender
        if !allowed {
            let name = RuntimeCatalog.runtime(id: runtime)?.name ?? runtime
            let yes = await withCheckedContinuation { answer in
                signInLendAsk = SignInLendAsk(runtimeName: name, label: hosts.label(id), answer: answer)
            }
            guard yes else { return .notLent(nil) }
            do {
                try await control.connect(startIfNeeded: false, timeout: .seconds(5))
                _ = try await control.call(DaemonAPI.Method.hostsLendSignIn,
                                           DaemonAPI.LendSignIn(host: id, from: lender, runtime: runtime, allowed: true))
            } catch {
                return .notLent(Self.lendFailed(error))
            }
        }
        do {
            _ = try await client(for: id).call(DaemonAPI.Method.relayOffer, DaemonAPI.RelayOffer(
                runtime: runtime, socketPath: "tunnel", caCertificate: grant.caCertificate, standIn: grant.standIn))
            return .lent
        } catch {
            return .notLent(Self.lendFailed(error))
        }
    }

    private static func lendFailed(_ error: any Error) -> String {
        "This Mac’s sign-in could not be lent: \((error as? JSONRPCError)?.message ?? error.localizedDescription)"
    }

    /// The person answered the lend ask: the one the window showed, which a second ask
    /// may since have replaced (#101). The second is still asked. Once per ask: its
    /// answer is a continuation.
    func finishSignInLendAsk(_ ask: SignInLendAsk, allowed: Bool) {
        if signInLendAsk?.id == ask.id { signInLendAsk = nil }
        ask.answer.resume(returning: allowed)
    }

    /// What a server may be lent from here, sent on every connect (043). Names only. No
    /// runtime takes a pasted key now that Gemini is retired (#3043), so it names none.
    func credentialOffer() -> DaemonAPI.CredentialsOffer {
        let ownOnly = hosts.host()?.ownSignInOnly ?? false
        // The window relays no sign-in and lends no sign-in file: Agents Host does, on its
        // Mac (T091).
        return DaemonAPI.CredentialsOffer(runtimes: [], ownSignInOnly: ownOnly, notRelayed: nil, signIns: nil)
    }

    /// A server's daemon wants a credential to start a runtime (043, R6): a sign-in this
    /// Mac relays, to a server of the control plane (T091). No runtime takes a pasted key
    /// now that Gemini, the last one that did, is retired (#3043), so there is no other way
    /// to answer one.
    func answerCredentialWanted(_ wanted: DaemonAPI.CredentialWanted, on id: MachineRef) async -> Bool {
        await lendSignIn(wanted.runtime, to: id) == .lent
    }

    /// Lend this Mac's sign-in for a runtime to a server (056, T091): when a runtime there
    /// wants one, and in step 7 of the server's set-up (#6855). "Use this server's own
    /// sign-in only" still wins (056 D6).
    func lendSignIn(_ runtime: String, to id: MachineRef) async -> SignInLend {
        guard id != .mac, !(hosts.host()?.ownSignInOnly ?? false) else { return .notLent(nil) }
        return await relaySignInThroughControl(runtime, to: id)
    }
}
