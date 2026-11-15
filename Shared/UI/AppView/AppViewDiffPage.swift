import AgentsKitCore
import CodeText
import SwiftUI

/// A view's `agents/open-diff` (#1341): a pull request's files, drawn with the Files changed
/// page's `DiffFilesView` (#5474), not in the view. Shown over the view, on the Mac, the iPad
/// and the iPhone alike.
struct AppViewDiffPage: View {
    let diff: AppViewDiff
    var openLink: (URL) -> Void = { _ in }

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            DiffFilesView(diff: diff, openLink: openLink)
                .paperGround()
                .navigationTitle(diff.title)
                #if os(iOS)
                .navigationBarTitleDisplayMode(.inline)
                #endif
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }.keyboardShortcut(.cancelAction)
                    }
                }
        }
        #if os(macOS)
        .frame(minWidth: 760, idealWidth: 1100, minHeight: 560, idealHeight: 760)
        #endif
    }

    /// The file's hunks as the diff's rows.
    static func rows(_ file: AppViewDiff.File) -> [DiffRow] {
        LineDiff.rows(hunks: file.hunks.map { hunk in
            LineDiff.Hunk(oldStart: hunk.oldStart, newStart: hunk.newStart, lines: hunk.lines.map { line in
                let kind: DiffRow.Kind = switch line.kind {
                case .added: .added
                case .removed: .removed
                case .context: .context
                }
                return (kind: kind, text: line.text)
            })
        })
    }
}
