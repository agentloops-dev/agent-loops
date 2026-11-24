#if os(macOS)
import AgentsKitCore
import SwiftUI

/// Settings ▸ Agents ▸ Policy's Commit trailers group (#6373, epic #6365, the gate 2
/// wireframes): **Record** and **Spend**, a switch for each trailer with an example value,
/// the 🔒 cap line, **Kept, not used**, **Where this comes from**, a failed save's line, and
/// the **Preview** of the block. Mac + web (#2796): the Remote has no Policy pane for it.
/// Drawn only with the `commit-trailers` feature flag on (`FeatureFlag`).
struct CommitTrailersSection: View {
    let effective: DaemonAPI.EffectivePolicy
    /// Your layer, or nil while it is not read yet.
    let yours: DaemonAPI.PolicyRules?
    let editable: Bool
    /// The trailer whose last save failed, and the line that says why.
    let failure: (key: String, line: String)?
    /// When the machine was last read, for "Last update" (#5139).
    let readAt: Date?
    /// The folders whose own rule changes the block (#6534), by name.
    var folders: [CommitTrailerWords.FolderChange] = []
    /// Opens a folder's Project Settings ▸ Policy, where the window can.
    var openFolder: ((String) -> Void)?
    let set: (CommitTrailerWords.Trailer, Bool) -> Void

    var body: some View {
        Section {
            Text(CommitTrailerWords.intro).appText(.fine).foregroundStyle(.secondary)
        } header: {
            Text(CommitTrailerWords.title)
        }
        .paperListRow()
        ForEach(CommitTrailerWords.Kind.allCases, id: \.self) { kind in
            Section {
                ForEach(CommitTrailerWords.trailers(kind)) { trailer in
                    CommitTrailerRow(trailer: trailer, rule: rule(trailer),
                                     yours: yours.flatMap { CommitTrailerWords.yours(trailer, in: $0) },
                                     editable: editable,
                                     failure: failure?.key == trailer.key ? failure?.line : nil) { set(trailer, $0) }
                }
            } header: {
                Text("\(Text(kind.title)) \(Text("· \(kind.line)").foregroundStyle(.secondary).fontWeight(.regular))")
            }
            .paperListRow()
        }
        Section {
            Text(CommitTrailerWords.preview { CommitTrailerWords.isOn($0, rule($0)) }.joined(separator: "\n"))
                .appText(.fine)
                .monospaced()
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            if !folders.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    Text(CommitTrailerWords.foldersTitle).appText(.fine).foregroundStyle(.secondary)
                    ForEach(folders) { folder in
                        HStack(spacing: 4) {
                            if let openFolder {
                                Button(folder.name) { openFolder(folder.folder) }
                                    .buttonStyle(.borderless)
                            } else {
                                Text(folder.name)
                            }
                            Text(folder.changes).foregroundStyle(.secondary)
                        }
                        .appText(.fine)
                        .accessibilityElement(children: .contain)
                    }
                }
            }
        } header: {
            Text("\(Text(CommitTrailerWords.previewTitle)) \(Text("· \(CommitTrailerWords.previewScope)").foregroundStyle(.secondary).fontWeight(.regular))")
        } footer: {
            if let readAt { LastUpdateText(read: readAt) }
        }
        .paperListRow()
    }

    private func rule(_ trailer: CommitTrailerWords.Trailer) -> DaemonAPI.EffectiveRule? {
        effective.rules.first { $0.rule == trailer.rule }
    }
}

/// One trailer: its name, an example value in the mono font, and its switch, which shows the
/// value in force. A switch a layer above holds is dimmed and can't be changed.
private struct CommitTrailerRow: View {
    let trailer: CommitTrailerWords.Trailer
    let rule: DaemonAPI.EffectiveRule?
    let yours: Bool?
    let editable: Bool
    let failure: String?
    let set: (Bool) -> Void

    @State private var showsSource = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            LabeledContent {
                Toggle(trailer.key, isOn: Binding(get: { isOn }, set: { set($0) }))
                    .labelsHidden()
                    .toggleStyle(.switch)
                    .disabled(!editable || capped)
                    // A disabled switch kept its old drawing when a cap above changed its value:
                    // a new one for each value and cap draws the one in force.
                    .id("\(isOn)-\(capped)")
            } label: {
                VStack(alignment: .leading, spacing: 1) {
                    HStack(spacing: 6) {
                        Text(trailer.key)
                        if let rule, PolicyLayersWords.yoursIsKept(rule) {
                            Text(PolicyLayersWords.keptNotUsed)
                                .appText(.fine).foregroundStyle(.secondary)
                                .padding(.horizontal, 6)
                                .overlay(Capsule().strokeBorder(Paper.rule))
                        }
                    }
                    Text(trailer.example).appText(.fine).monospaced().foregroundStyle(.secondary).lineLimit(1)
                }
            }
            if capped, let line = rule?.capLine {
                Label(line, icon: "lock.fill")
                    .appText(.fine).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let failure {
                Text(failure).appText(.fine).tinted(.failure)
                    .fixedSize(horizontal: false, vertical: true)
                Button(CommitTrailerWords.tryAgain) { set(!isOn) }
                    .buttonStyle(.borderless).appText(.fine)
                    .disabled(!editable)
            }
            Button(PolicyLayersWords.whereFrom) { showsSource = true }
                .buttonStyle(.borderless)
                .appText(.fine)
                .disabled(rule == nil)
                .popover(isPresented: $showsSource) {
                    if let rule { PolicySourcePopover(row: trailer.row, rule: rule) }
                }
        }
        .accessibilityElement(children: .contain)
    }

    private var isOn: Bool { CommitTrailerWords.isOn(trailer, rule) }
    private var capped: Bool { CommitTrailerWords.isCapped(trailer, rule) }
}
#endif
