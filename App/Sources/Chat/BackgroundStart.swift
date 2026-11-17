import AgentsKitCore
import AppKit
import SwiftUI

/// File ▸ New Session in Background (⇧⌘N, #5798): a small window of its own that starts a
/// session, closes, and leaves the main window where it was. The new session shows in the
/// sidebar, as every new one does; nothing goes to it.
///
/// The form is New Session's own: the folder's name as a menu over the chat's `PromptBar`, with
/// its pills, in the mode it is in for a new chat. As in a folder's own window (#1089) there is
/// one draft, and it follows the window in front: this window takes it while it is in front,
/// and the main window takes it back when it comes to the front.
enum BackgroundStartScene {
    static let id = "background-start"
    static let title = "New Session in Background"
}

extension EnvironmentValues {
    /// In New Session in Background's window (#5798): the prompt bar is always a new session's.
    @Entry var startsInBackground = false
}

extension AppModel {
    /// The folder ⇧⌘N starts in at first: the open session's, else the selected one, else the first.
    var backgroundStartDefaultFolder: FolderKey? {
        let live = foldersInListOrder.map(\.key)
        if let agent = selectedAgent {
            let key = FolderKey(host: agent.host, folder: agent.folderPath)
            if live.contains(key) { return key }
        }
        if let key = selectedFolderKey, live.contains(key) { return key }
        return live.first
    }
}

/// The window: the folder's name as a menu, New Session's persona control (#5844), where it is,
/// and its prompt bar. Return starts; Esc or the close button closes it, and the prompt is kept for the next time.
struct BackgroundStartWindow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismissWindow) private var dismissWindow
    @Environment(\.appearsActive) private var appearsActive
    /// Its own, so the prompt takes the keyboard here and the main window's sheets stay there.
    @State private var requests = WindowRequests()

    private var key: FolderKey? { model.backgroundStartKey }
    private var summary: DaemonAPI.FolderSummary? { model.liveFolders.first { $0.key == key } }

    var body: some View {
        VStack(spacing: 6) {
            if let summary {
                folderMenu(summary)
                // New Session's own persona control, where New Session has it (#5844).
                PersonaChoice()
                    .multilineTextAlignment(.center)
                Text(FolderAgentsView.place(summary, hosts: model.hosts))
                    .appText(.supporting)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .help(summary.folder.folder.path)
                OfflineStrip(host: summary.host)
                PromptBar(folderIsFixed: true)
                    .padding(.top, 10)
            } else {
                EmptyState.noFolder
            }
        }
        .padding(.top, 18)
        .frame(width: 680)
        .fixedSize(horizontal: false, vertical: true)
        .paperGround()
        .navigationTitle(BackgroundStartScene.title)
        .environment(requests)
        .environment(\.windowFolder, key)
        .environment(\.startsInBackground, true)
        .onAppear {
            if key == nil || summary == nil { model.backgroundStartKey = model.backgroundStartDefaultFolder }
            adopt()
            requests.focusPrompt()
        }
        .onChange(of: key) { adopt() }
        // One draft for every window: it follows the one in front (#1089).
        .onChange(of: appearsActive) { _, active in
            if active { adopt(again: true) }
        }
        .onDisappear { leave() }
        .onExitCommand { close() }
    }

    /// The folder's name as a menu of the folders, as New Session's project name is; a pick
    /// changes this window's folder only.
    private func folderMenu(_ summary: DaemonAPI.FolderSummary) -> some View {
        Menu {
            ForEach(model.foldersInListOrder, id: \.key) { folder in
                Button {
                    model.backgroundStartKey = folder.key
                } label: {
                    if folder.key == summary.key {
                        Label(label(folder), icon: "checkmark")
                    } else {
                        Text(label(folder))
                    }
                }
            }
        } label: {
            TitleMenuLabel(title: label(summary))
        }
        .titleMenu()
        .help("Start the session in another folder")
        .accessibilityLabel("Folder: \(label(summary))")
    }

    /// The draft is this window's while it is in front: its folder, and a start opens nothing.
    private func adopt(again: Bool = false) {
        guard let key else { return }
        let window = model.composingWindow
        model.composingWindow = key
        model.composingInBackground = true
        model.startedInBackground = { _ in close() }
        guard model.draftCwd != key.folder || (again && window != key) else { return }
        model.draftCwd = key.folder
        Task { await model.loadDraftOptions() }
    }

    private func leave() {
        model.composingInBackground = false
        model.startedInBackground = nil
        if model.composingWindow == key { model.composingWindow = nil }
    }

    private func close() {
        dismissWindow(id: BackgroundStartScene.id)
    }

    private func label(_ folder: DaemonAPI.FolderSummary) -> String {
        SidebarOrder.label(folder) { model.hosts.label($0) }
    }
}
