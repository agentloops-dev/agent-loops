import AgentsKitCore
import Foundation

/// How a lend of this Mac's sign-in ended. `notLent` says why, when the person should know.
enum SignInLend: Equatable {
    case lent
    case notLent(String?)
}

extension AppModel {
    /// A server of the control plane wants a sign-in this Mac relays (T091). This Mac's
    /// host starts the relay first, which fails when this Mac is not signed in to the
    /// runtime the way a server can use (#6855). Then the control plane lets the server
    /// tunnel to this Mac's host, and the server is told where its gate goes. The person is
    /// not asked: the control plane lends only between machines of one user. The token
    /// never leaves this Mac (056 D1). `notLent` with no words when this is not such a case;
    /// with words when a lend failed (#7108).
    func relaySignInThroughControl(_ runtime: String, to id: MachineRef) async -> SignInLend {
        guard reachesThroughControl(id), ToolPolicyCatalog.policy(for: runtime).relay != nil,
              let lender = thisMacHostID, lender != id, let control = controlPlaneClient() else { return .notLent(nil) }
        defer { Task { await control.disconnect() } }
        guard let grant = try? await client(for: lender).call(DaemonAPI.Method.relayGrant,
                                                              DaemonAPI.RelayGrantRequest(runtime: runtime),
                                                              returning: DaemonAPI.RelayGrantReply.self) else { return .notLent(nil) }
        let allowed = controlPlaneHosts?.first { $0.id == id }?.signInFrom?[runtime] == lender
        if !allowed {
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
