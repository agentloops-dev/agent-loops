import AgentsKitCore
import Foundation
import Observation

/// Send Set-up's sends that wait (#7155, gate 2 board *page sender offline*): one a machine, tried
/// again less often each time, from 15 s to 5 min, for 2 hours (#7547), or until Cancel Send. Kept
/// while the window runs.
@MainActor @Observable
final class SetupSendState {
    struct Pending: Equatable {
        /// The machine it waits for: this Mac (the sender) or the target.
        var waitsFor: String
        /// Why the tries stopped with nothing sent; nil while they go on.
        var ended: String?
        var note: String { ended == nil ? MachineScriptWords.sendWaitNote(waitsFor) : MachineScriptWords.sendEndedNote }
    }

    private(set) var pending: [MachineRef: Pending] = [:]
    @ObservationIgnored private var tries: [MachineRef: Task<Void, Never>] = [:]

    /// Waits for `waitsFor`, and tries `send` again until it says it went, or why it cannot, or
    /// the tries stop; the last two stay in `pending` with why.
    func wait(_ host: MachineRef, for waitsFor: String, send: @escaping @MainActor () async -> SendResult) {
        tries[host]?.cancel()
        pending[host] = Pending(waitsFor: waitsFor)
        tries[host] = Task { [weak self] in
            var n = 1
            while let pause = MachineScriptWords.sendPause(before: n) {
                n += 1
                try? await Task.sleep(for: pause)
                guard !Task.isCancelled else { return }
                switch await send() {
                case .waits(let who): self?.pending[host] = Pending(waitsFor: who)
                case .sent: self?.done(host); return
                case .refused(let why): self?.end(host, why: why); return
                }
            }
            guard !Task.isCancelled, let self else { return }
            end(host, why: MachineScriptWords.sendGaveUpLine(pending[host]?.waitsFor ?? waitsFor))
        }
    }

    func cancel(_ host: MachineRef) {
        tries[host]?.cancel()
        done(host)
    }

    private func done(_ host: MachineRef) {
        tries[host] = nil
        pending[host] = nil
    }

    private func end(_ host: MachineRef, why: String) {
        tries[host] = nil
        pending[host]?.ended = why
    }

    enum SendResult { case sent, waits(String), refused(String) }
}

extension AppModel {
    /// The person's scripts on this Mac, the machine that sends them (#7155).
    func readSetupScripts() async throws -> DaemonAPI.SetupScripts {
        try await client(for: .mac).call(DaemonAPI.Method.setupScripts, DaemonAPI.Empty(), returning: DaemonAPI.SetupScripts.self)
    }

    /// Approve and Send (#7155): signs an approval of `script` as it is for `host` only, with this
    /// Mac's lock key, and has this Mac send it. Nil once it went, or waits for a machine to be
    /// online; else why it did not.
    func sendSetup(_ host: MachineRef, script: DaemonAPI.SetupScript) async -> String? {
        guard let hash = script.hash, let size = script.size,
              let content = try? JSONEncoder().encode(DaemonAPI.SetupScriptApproval(hash: hash, size: size)) else {
            return "setup.sh can’t be read on \(hosts.label(.mac)) now."
        }
        guard let approval = lock.signRecord(kind: "setup.script", subject: "setup", scope: "m:" + host.rawValue,
                                             seq: script.nextSeq, content: content) else {
            return "Scripts need your machine lock: a device of your lock signs each one. Set up the lock first."
        }
        let send = DaemonAPI.SetupSend(host: host.rawValue, approval: approval)
        // A send whose tries stopped is replaced by this one.
        setupSends.cancel(host)
        switch await trySetupSend(host, send) {
        case .sent: return nil
        case .refused(let why): return why
        case .waits(let who):
            setupSends.wait(host, for: who) { [weak self] in
                await self?.trySetupSend(host, send) ?? .refused("")
            }
            return nil
        }
    }

    private func trySetupSend(_ host: MachineRef, _ send: DaemonAPI.SetupSend) async -> SetupSendState.SendResult {
        do {
            _ = try await client(for: .mac).call(DaemonAPI.Method.setupSend, send, returning: DaemonAPI.Empty.self)
            return .sent
        } catch let error as JSONRPCError {
            if error.code == DaemonAPI.Failure.hostOffline { return .waits(hosts.label(host)) }
            return .refused(error.message)
        } catch {
            // Not a wait: this Mac's daemon did not answer at all (#7547).
            return .refused("\(hosts.label(.mac)) can’t be asked now: \(error.localizedDescription)")
        }
    }

    /// Approve and Make, and a warm pool's Review root.sh (#7157): signs an approval of each
    /// script as it is now, for machines as they are made, with this Mac's lock key, and has this
    /// Mac keep it. Nil once each is kept; else why not.
    func approveSetupScripts(_ scripts: [DaemonAPI.SetupScript], scope: String = "made") async -> String? {
        for script in scripts {
            guard let hash = script.hash, let size = script.size,
                  let content = try? JSONEncoder().encode(DaemonAPI.SetupScriptApproval(hash: hash, size: size)) else {
                return "\(script.file) can’t be read on \(hosts.label(.mac)) now."
            }
            guard let approval = lock.signRecord(kind: "setup.script", subject: script.name, scope: scope,
                                                 seq: script.nextSeq, content: content) else {
                return "Scripts need your machine lock: a device of your lock signs each one. Set up the lock first."
            }
            do {
                _ = try await client(for: .mac).call(DaemonAPI.Method.setupApprove,
                                                     DaemonAPI.SetupApprove(script: script.name, approval: approval),
                                                     returning: DaemonAPI.SetupScript.self)
            } catch let error as JSONRPCError {
                return error.code == DaemonAPI.Failure.setupScriptChanged
                    ? "\(script.file) changed again while you read it. Cancel, and look at it again."
                    : error.message
            } catch {
                return "\(hosts.label(.mac)) can’t keep the approval now: \(error.localizedDescription)"
            }
        }
        return nil
    }

    /// Whether this Mac carries the provider, and so has the scripts its makes use (#7157).
    func carriedHere(_ provider: DaemonAPI.ProviderSummary) -> Bool {
        guard let carrier = provider.machine else { return false }
        return carrier == .mac || isOnThisMac(carrier)
    }

    /// New Machine's *Your scripts* (#7157), behind the flag, when this Mac carries the provider
    /// and so sends the scripts; nil else, and the sheet draws none.
    func newMachineScripts(_ provider: ProviderID?) -> NewMachineScripts? {
        guard flags.isOn(.machineScripts), self.host(.mac, has: Capability.hostMachineSetup2) else { return nil }
        let summary = provider.flatMap { id in machineProviders.first { $0.id == id } } ?? machineProviders.first
        guard let summary, carriedHere(summary) else { return nil }
        return NewMachineScripts(sender: hosts.label(.mac), canSign: lock.canSign,
                                 read: { [weak self] in
                                     guard let self else { return .failure(NewMachineRefusal("")) }
                                     do { return .success(try await self.readSetupScripts().scripts) } catch let error as JSONRPCError {
                                         return .failure(NewMachineRefusal(error.message))
                                     } catch {
                                         return .failure(NewMachineRefusal("\(self.hosts.label(.mac))’s scripts can’t be read now."))
                                     }
                                 },
                                 approve: { [weak self] scripts in
                                     // Nil is a yes: an absent model is the refusal.
                                     guard let self else { return "The window closed before the scripts were approved." }
                                     return await self.approveSetupScripts(scripts)
                                 })
    }

    /// A machine's failed Root set-up or Your set-up, behind the flag (#7558): a new session
    /// starts there all the same, with a warning, and does not wait for the set-up.
    func failedSetupScript(_ host: MachineRef) -> DaemonAPI.MachineSetupStep? {
        guard flags.isOn(.machineScripts) else { return nil }
        return MachineSetupWords.failedScript(hosts.controlRows[host]?.setup)
    }

    /// Run Again, and Try Again of a failed Your set-up (#7155): the machine runs its copy again.
    func runSetupAgain(_ host: MachineRef) async {
        do {
            _ = try await client(for: host).call(DaemonAPI.Method.setupRun, DaemonAPI.Empty(), returning: DaemonAPI.Empty.self)
        } catch let error as JSONRPCError {
            show(problem: error.message)
        } catch {
            show(problem: "\(hosts.label(host)) can’t be asked now: \(error.localizedDescription)")
        }
    }

    /// "Your scripts" on a machine's page: behind its flag, on a machine that takes a sent script.
    func showsMachineScripts(_ host: MachineRef) -> Bool {
        flags.isOn(.machineScripts) && host != .mac && self.host(host, has: Capability.hostMachineSetup2)
            && self.host(.mac, has: Capability.hostMachineSetup2)
    }
}

// MARK: Settings ▸ Machines ▸ Machine scripts (#7512)

extension AppModel {
    /// The Machine scripts page in Settings ▸ Machines: behind its flag, on a Mac whose host
    /// reads the scripts.
    var showsMachineScriptsSettings: Bool {
        flags.isOn(.machineScripts) && host(.mac, has: Capability.hostMachineSetup2)
    }

    /// New root.sh and New setup.sh: writes the example into `folder` when there is no file of
    /// that name, and never over one. Nil once written; else why not.
    nonisolated static func writeExampleScript(_ name: String, folder: String) -> String? {
        let dir = URL(fileURLWithPath: folder, isDirectory: true)
        let file = dir.appendingPathComponent(name + ".sh")
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true,
                                                    attributes: [.posixPermissions: 0o700])
            // .withoutOverwriting: a file made since the read stays as it is.
            // store-bounds: not a store: one of the person's two scripts, a fixed example, written once
            try Data(MachineScriptWords.example(name).utf8).write(to: file, options: .withoutOverwriting)
            return nil
        } catch CocoaError.fileWriteFileExists {
            return "\(name).sh is there already."
        } catch {
            return "\(name).sh can’t be written: \(error.localizedDescription)"
        }
    }
}
