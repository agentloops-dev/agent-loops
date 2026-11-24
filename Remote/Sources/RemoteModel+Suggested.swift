import AgentsKitCore
import Foundation

/// The remote's state, part of `RemoteModel` (#5383): see `RemoteModel.swift`.
extension RemoteModel {
    // MARK: New Session's Suggested section (#5460, epic #5457)

    /// A folder's suggestions as last read: from its host now (`fresh`), or from this device's
    /// copy, which says when it was read.
    struct SuggestionsRead: Equatable {
        var value: DaemonAPI.WorkSuggestLeads
        var at: Date
        var fresh: Bool
    }

    /// What the copy keys a folder's suggestions by.
    private static func suggestionsAsked(_ key: FolderKey) -> String { "\(key.host.rawValue)|\(key.folder.path)" }

    /// The folder's host can list them: it says `work.suggest-leads/1` (or says no list), and answers.
    func suggestsLeads(on host: MachineRef) -> Bool {
        // A host with no list is asked, and one that does not know the method draws no section.
        // Asked only with the Suggested flag on (#6572).
        flags.isOn(.suggested) && hostClient(host) != nil && !isStale(on: host) && self.host(host, has: Capability.workSuggestLeads1)
    }

    /// Read the folder's suggestions: the device's copy at once, then its host's answer in its
    /// place. A read that fails keeps what was there, so its "Last update" says how old it is.
    func loadSuggestions(in key: FolderKey) async {
        guard flags.isOn(.suggested) else { return }
        if startSuggestions[key] == nil,
           let kept = await keptWorkRead(DaemonAPI.WorkSuggestLeads.self, .suggestions, Self.suggestionsAsked(key)),
           startSuggestions[key] == nil {
            startSuggestions[key] = SuggestionsRead(value: kept.value, at: kept.at, fresh: false)
        }
        guard suggestsLeads(on: key.host) else {
            // Not asked: what is held is an earlier read, and says so.
            startSuggestions[key]?.fresh = false
            return
        }
        do {
            let read = try await forge(DaemonAPI.Method.workSuggestLeads,
                                       DaemonAPI.WorkSuggestLeadsRequest(folder: key.folder.path),
                                       returning: DaemonAPI.WorkSuggestLeads.self, on: key.host)
            let now = Date()
            startSuggestions[key] = SuggestionsRead(value: read, at: now, fresh: true)
            startSuggestionsFailed[key] = nil
            keepWork(read, .suggestions, Self.suggestionsAsked(key))
        } catch {
            note("start: suggestions for \(key.folder.lastPathComponent) not read: \(error.localizedDescription)")
            if var held = startSuggestions[key] {
                held.fresh = false
                startSuggestions[key] = held
            } else if let why = SuggestedLeads.failure(error) {
                // No read to show: say why in one line (#5790).
                startSuggestionsFailed[key] = why
            }
        }
    }

    /// Dismiss: the row goes at once, the host keeps it hidden until the epic changes or a lead of
    /// it ends, and the list is read again for the one that takes its place. Why not, or nil.
    func dismissSuggestion(_ suggestion: DaemonAPI.WorkLeadSuggestion, in key: FolderKey) async -> String? {
        let before = startSuggestions[key]
        if var held = before {
            held.value.suggestions.removeAll { $0.node == suggestion.node }
            startSuggestions[key] = held
        }
        do {
            _ = try await forge(DaemonAPI.Method.workDismissLead, DaemonAPI.WorkDismissLeadRequest(node: suggestion.node),
                                returning: DaemonAPI.ForgeDone.self, on: key.host)
        } catch {
            startSuggestions[key] = before
            return "Couldn’t dismiss it: \(error.localizedDescription)"
        }
        await loadSuggestions(in: key)
        return nil
    }
}
