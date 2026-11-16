import AgentsKitCore
import AppKit
import SwiftUI

/// File ▸ New Session in Background (⇧⌘N, #5798): a small window of its own that starts a
/// session, closes, and leaves the main window where it was. The new session shows in the
/// sidebar, as every new one does; nothing goes to it.
enum BackgroundStartScene {
    static let id = "background-start"
    static let title = "New Session in Background"
}

/// The background window's own form, apart from the main window's draft (`AppModel.composing`,
/// `draftWorktree`, `DraftKeeper`), so the two never change each other. Kept on the model, so a
/// prompt left by Esc or the close button is there at the next ⇧⌘N.
@MainActor
@Observable
final class BackgroundStartDraft {
    var prompt = ""
    var folder: FolderKey?
    /// Auto first, as the start form (#2658).
    var runtimeID = LevelWords.autoID
    var inWorktree = false
    /// Whether the chosen folder can make a new worktree; asked each time the folder changes.
    var canMakeWorktree = false
    /// Why the last start did not happen: said in the window, which keeps the prompt.
    var problem: String?
    var isStarting = false

    var canStart: Bool {
        folder != nil && !isStarting && !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// A start that went through: a clean form for the next one, in the same folder and runtime.
    func clear() {
        prompt = ""
        inWorktree = false
        problem = nil
    }
}

extension AppModel {
    /// The folder ⇧⌘N starts in at first: the open session's, else the selected one, else the first.
    var backgroundStartFolder: FolderKey? {
        let live = foldersInListOrder.map(\.key)
        if let agent = selectedAgent {
            let key = FolderKey(host: agent.host, folder: agent.folderPath)
            if live.contains(key) { return key }
        }
        if let key = selectedFolderKey, live.contains(key) { return key }
        return live.first
    }

    /// What the background window can start with on `host`: Auto, then each runtime that can start.
    func backgroundStartRuntimes(on host: MachineRef) -> [(id: String, name: String)] {
        let named = runtimes(on: host).filter(\.canStart).map { (id: $0.runtime.id, name: $0.runtime.name) }
        return named.isEmpty ? [] : [(id: LevelWords.autoID, name: "Auto")] + named
    }

    /// Whether `key`'s folder can make a new worktree, for the window's Worktree toggle.
    func backgroundStartCanMakeWorktree(_ key: FolderKey) async -> Bool {
        let answer = try? await client(for: key.host).callWithin(
            StartPageAsks.patience(for: DaemonAPI.Method.worktreesList), DaemonAPI.Method.worktreesList,
            DaemonAPI.WorktreesListRequest(folder: key.folder), returning: DaemonAPI.WorktreesListResponse.self)
        return answer?.canMakeNew ?? false
    }

    /// Starts the background window's session (#5798). The selection, the main window's draft and
    /// what it shows do not change. Nil when it started, else why not.
    func startInBackground(_ draft: BackgroundStartDraft) async -> String? {
        guard let key = draft.folder else { return "Choose a folder." }
        let prompt = draft.prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !prompt.isEmpty else { return nil }
        let request = DaemonAPI.StartRequest(runtimeID: draft.runtimeID, cwd: key.folder, prompt: prompt,
                                             worktree: draft.inWorktree && draft.canMakeWorktree ? .new : nil,
                                             requestID: UUID(),
                                             levels: Levels(model: draftLevels.model, permission: draftLevels.permission))
        do {
            _ = try await client(for: key.host).call(DaemonAPI.Method.agentsStart, request, returning: UUID.self)
        } catch let error as JSONRPCError where error.code == DaemonAPI.Failure.autoNoRuntime {
            return error.message
        } catch {
            return describe(error)
        }
        // The new row in the sidebar is what says it started; nothing is opened.
        if key.host == .mac {
            await refreshAgents()
        } else {
            await refreshServer(key.host)
        }
        return nil
    }
}

/// The window itself: Folder and Runtime, the prompt, Worktree and Start. Return starts, ⌥Return is
/// a new line, Esc and the close button close it and keep what was typed.
struct BackgroundStartWindow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismissWindow) private var dismissWindow
    @FocusState private var promptFocused: Bool

    var body: some View {
        let draft = model.backgroundStart
        @Bindable var bound = draft
        let folders = model.foldersInListOrder
        let runtimes = draft.folder.map { model.backgroundStartRuntimes(on: $0.host) } ?? []
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 16) {
                Picker("Folder", selection: $bound.folder) {
                    ForEach(folders, id: \.key) { summary in
                        Text(model.hosts.isEmpty ? summary.name : "\(summary.name) · \(model.hosts.label(summary.host))")
                            .tag(Optional(summary.key))
                    }
                }
                .fixedSize()
                Picker("Runtime", selection: $bound.runtimeID) {
                    ForEach(runtimes, id: \.id) { runtime in
                        Text(runtime.name).tag(runtime.id)
                    }
                }
                .fixedSize()
                .disabled(runtimes.isEmpty)
                Spacer(minLength: 0)
            }
            TextField("What should the agent do?", text: $bound.prompt, axis: .vertical)
                .lineLimit(4...10)
                .textFieldStyle(.plain)
                .padding(8)
                .background(RoundedRectangle(cornerRadius: 6).strokeBorder(.separator))
                .focused($promptFocused)
                .onSubmit { start() }
                .accessibilityLabel("Prompt")
            if let problem = draft.problem {
                Text(problem)
                    .appText(.fine)
                    .foregroundStyle(StateTint.failure.style(or: .secondary))
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if draft.canMakeWorktree {
                    Toggle("Worktree", isOn: $bound.inWorktree)
                        .toggleStyle(.checkbox)
                        .help("Start the session in a new worktree of this folder")
                }
                Spacer()
                Button("Cancel") { dismissWindow(id: BackgroundStartScene.id) }
                    .keyboardShortcut(.cancelAction)
                Button(draft.isStarting ? "Starting…" : "Start") { start() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!draft.canStart || runtimes.isEmpty)
            }
        }
        .padding(16)
        .frame(width: 520)
        .fixedSize(horizontal: false, vertical: true)
        .paperGround()
        .navigationTitle(BackgroundStartScene.title)
        .onAppear {
            if draft.folder == nil || !folders.contains(where: { $0.key == draft.folder }) || draft.prompt.isEmpty {
                draft.folder = model.backgroundStartFolder
            }
            promptFocused = true
        }
        .task(id: draft.folder) { await folderChanged() }
    }

    /// A new folder is a new question for the worktree, and maybe for the runtime.
    private func folderChanged() async {
        let draft = model.backgroundStart
        guard let key = draft.folder else {
            draft.canMakeWorktree = false
            return
        }
        let ids = model.backgroundStartRuntimes(on: key.host).map(\.id)
        if !ids.contains(draft.runtimeID) { draft.runtimeID = LevelWords.autoID }
        let can = await model.backgroundStartCanMakeWorktree(key)
        guard draft.folder == key else { return }
        draft.canMakeWorktree = can
        if !can { draft.inWorktree = false }
    }

    private func start() {
        let draft = model.backgroundStart
        guard draft.canStart else { return }
        draft.isStarting = true
        draft.problem = nil
        Task {
            let problem = await model.startInBackground(draft)
            draft.isStarting = false
            if let problem {
                // The window stays, with the prompt and why (#5798's decision).
                draft.problem = problem
            } else {
                draft.clear()
                dismissWindow(id: BackgroundStartScene.id)
            }
        }
    }
}
