import AgentsKitCore
import Observation
import SwiftUI

/// Work's changes with Work's cache on (#6354, epic #6348): a change shows at once in every list and on the item's
/// page, because it changes the kept item before the forge answers. The write's answer (#6351) replaces the value;
/// when the forge refuses it, the value goes back, the row has a short red wash, and the footer says what and why,
/// with Try again. One store for the window, so every list and page draws the same.
@MainActor @Observable
final class WorkSaves {
    /// A change sent to a forge: what it is, on which item, by which machine.
    struct Save: Equatable {
        let node: DaemonAPI.LineageNode
        let change: WorkChange
        let host: MachineRef
    }

    /// The changes whose writes run now, the oldest first.
    private(set) var saving: [Save] = []
    /// The last change the forge refused, with its words; nil after Try again or a later change.
    private(set) var failed: (save: Save, words: String)?
    /// The items that went back just now: their rows have a short red wash.
    private(set) var washed: Set<String> = []
    /// Goes up each time a kept item changes, so every list and page draws it again.
    private(set) var revision = 0

    /// How long a row that went back keeps its red wash.
    static let washFor: Duration = .milliseconds(1600)

    /// The footer's line: "Saving #6311…" while a write runs, else what the forge refused.
    var line: (words: String, failed: Bool)? {
        if let last = saving.last { return (WorkChange.savingWords(last.node.ref), false) }
        if let failed { return (failed.words, true) }
        return nil
    }

    static func key(_ node: DaemonAPI.LineageNode) -> String { "\(node.kind) \(node.ref)" }

    func isWashed(_ node: DaemonAPI.LineageNode) -> Bool { washed.contains(Self.key(node)) }

    /// Shows the change at once, then asks the forge. The forge's item replaces it; a refusal puts it back.
    func save(_ change: WorkChange, on node: DaemonAPI.LineageNode, host: MachineRef, model: any WorkModel,
              cache: any WorkCaching) {
        let save = Save(node: node, change: change, host: host)
        failed = nil
        saving.append(save)
        Task {
            let old = await cache.item(node)
            await cache.edit(node) { change.apply(to: &$0) }
            revision += 1
            WorkCacheLog.note("saving \(node.kind) \(node.ref): \(change.words(node.ref))")
            do {
                let done = try await model.forge(change.method, change.request(node), returning: DaemonAPI.ForgeDone.self, on: host)
                // The forge's item replaces the optimistic value when it is newer; one as old (a read that did not
                // see the write yet) does not undo it. Then a later read replaces it as usual.
                if let item = done.item { await cache.keep(items: [item]) }
                await cache.settle(node)
                WorkCacheLog.note("saved \(node.kind) \(node.ref)\(done.item == nil ? " (no item in the answer)" : "")")
            } catch {
                if let old { await cache.edit(node) { change.restore(from: old, into: &$0) } }
                await cache.settle(node)
                let said = WorkList.failureWords(error)
                failed = (save, change.failedWords(node.ref, said: said))
                WorkCacheLog.note("could not save \(node.kind) \(node.ref): \(said); changed back")
                wash(node)
            }
            if let at = saving.firstIndex(of: save) { saving.remove(at: at) }
            revision += 1
        }
    }

    /// Try again: the refused change, sent again.
    func retry(model: any WorkModel, cache: any WorkCaching) {
        guard let save = failed?.save else { return }
        self.save(save.change, on: save.node, host: save.host, model: model, cache: cache)
    }

    private func wash(_ node: DaemonAPI.LineageNode) {
        let key = Self.key(node)
        washed.insert(key)
        Task {
            try? await Task.sleep(for: Self.washFor)
            washed.remove(key)
        }
    }
}

/// The footer's save line (#6354): "Saving #6311…" with a spinner, or what the forge refused with Try again.
struct WorkSaveLine: View {
    let line: (words: String, failed: Bool)
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 6) {
            if line.failed {
                Image(icon: "exclamationmark.triangle.fill").tinted(.failure)
                Text(line.words).tinted(.failure).textSelection(.enabled)
                Button(WorkLook.tryAgain, action: retry).buttonStyle(.paper)
            } else {
                ProgressView().controlSize(.mini)
                Text(line.words)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
    }
}

extension View {
    /// A row that went back (#6354): a short red wash behind it.
    func workWashed(_ on: Bool) -> some View {
        listRowBackground(on ? StateTint.failure.color?.opacity(0.14) : nil)
            .animation(.easeOut(duration: 0.4), value: on)
    }
}
