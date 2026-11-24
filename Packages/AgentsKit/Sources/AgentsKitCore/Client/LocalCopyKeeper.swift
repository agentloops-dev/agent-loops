import Foundation

/// Keeps a client's `LocalCopy` (#4791) from what its `AgentsModel` holds, and draws it
/// back. Shared by the Mac window and the Remote, so both keep and draw the same way.
///
/// - A machine's list is written a second after a host last listed it, so a reconnect's
///   several lists write once.
/// - A chat's end is written when its host's opening page lands and when the reader
///   leaves it, so what was added while it was read is kept too.
/// - Nothing drawn from the copy is written back.
@MainActor
public final class LocalCopyKeeper {
    public let copy: LocalCopy
    /// Chats fetched before they are opened (#4795): drawn before the copy on disk.
    public let prefetched = OpeningPrefetch()
    private weak var work: AgentsModel?
    private var pending: [MachineRef: Task<Void, Never>] = [:]
    /// The machines the control plane last listed; nil before it answers.
    private var listed: Set<MachineRef>?
    /// How long after a host's last list its machine is written.
    public var writeAfter: Duration = .seconds(1)

    public init(copy: LocalCopy, work: AgentsModel) {
        self.copy = copy
        self.work = work
        work.onListed = { [weak self] host in self?.listed(host) }
        work.onLeavingChat = { [weak self] transcript in self?.keep(transcript) }
    }

    /// Every machine kept, drawn at once: each one whose host has not listed it yet.
    public func drawMachines() async {
        let machines = await copy.machinesRead()
        guard let work else { return }
        for (machine, at) in machines where listed?.contains(machine.host) ?? true { work.takeCopy(machine, readAt: at) }
    }

    /// The control plane listed its machines: one kept that it no longer lists is gone (#6789), from the copy and from
    /// what was drawn from it. This Mac's own (`.mac`) is the client's to say, not the list's.
    public func listed(machines: some Sequence<MachineRef>) {
        let hosts = Set(machines).union([.mac])
        listed = hosts
        if let work {
            for host in work.drawnFromCopy.subtracting(hosts) {
                work.replaceFolders([], from: host)
                work.replaceAgents([], from: host)
            }
        }
        let copy = copy
        Task { await copy.dropMachines(except: hosts) }
    }

    /// The chat being opened, drawn from the copy while its host's page is on its way.
    /// Nothing when the copy holds none of it, or its host answered first.
    @discardableResult
    public func drawOpening(of session: UUID, load: Int) async -> Bool {
        guard let kept = await copy.transcript(of: session), let work else { return false }
        return work.takeCopiedOpening(kept, load: load)
    }

    /// The chat being opened, drawn at once from what was fetched for it before it was
    /// opened (#4795), with no wait on the disk. Whether there was one to draw.
    @discardableResult
    public func drawPrefetched(of session: UUID, load: Int) -> Bool {
        guard let fetched = prefetched.take(session), let work else { return false }
        return work.takeCopiedOpening(fetched, load: load)
    }

    /// A host's opening page landed: its end is kept. A page whose turns did not load is
    /// not, so the copy never holds a chat without its turns.
    public func opened(_ session: UUID, _ loaded: ChatOpening.Loaded) {
        guard loaded.turnsFailure == nil else { return }
        keep(LocalCopy.Transcript(session: session, turns: loaded.turns, page: loaded.page, ask: loaded.ask))
    }

    /// Writes nothing after this: a forget's delete is not followed by a late write.
    public func forget() async {
        for task in pending.values { task.cancel() }
        pending = [:]
        prefetched.forget()
        await copy.forget()
    }

    private func keep(_ transcript: LocalCopy.Transcript) {
        let copy = copy
        Task { await copy.keep(transcript) }
    }

    private func listed(_ host: MachineRef) {
        pending[host]?.cancel()
        let wait = writeAfter
        let readAt = Date()
        pending[host] = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled, let self, let work = self.work else { return }
            self.pending[host] = nil
            // Its folders came and its sessions are still the copy's: they are not written back as read now.
            guard !work.drawnFromCopy.contains(host) else { return }
            let machine = LocalCopy.Machine(host: host, folders: work.folders.filter { $0.host == host },
                                            agents: work.agents.filter { $0.host == host },
                                            point: work.listPoint(of: host))
            await self.copy.keep(machine, readAt: readAt)
        }
    }
}
