import AgentsKitCore
import CodeText
import SwiftUI

/// A pull request's files changed as a page (#5474, `specs/pr-files-changed-page/spec.md`): a
/// line of what changed, then each file under a header that stays at the top while its lines
/// scroll by, and that folds the file away. On a wide page a list of the files is beside the
/// diff, each with its counts; on a narrow one *Files ▾* is in the line instead. Each file is
/// drawn with `FileDiffView`: added lines green, removed lines red, the + and − signs kept.
///
/// The page of the Mac window's Files changed (#5478) and of the view's `agents/open-diff`
/// sheet (#1341); the Remote's page (#5479) draws the same. It scrolls itself.
struct DiffFilesView: View {
    let diff: AppViewDiff
    var openLink: (URL) -> Void = { _ in }

    @AppStorage(DisplayPreference.reviewDiffLayout, store: WindowDefaults.store) private var layout = FileDiffView.Layout.unified
    @State private var rows: [String: [DiffRow]] = [:]
    @State private var collapsed: Set<String> = []
    @State private var chosen: String?
    @State private var width: CGFloat = 0
    #if os(iOS)
    /// The Remote's *Files ▾* (#5479): the file list in a sheet, as a menu of long paths is cut.
    @State private var filesSheet = false
    #endif

    /// The file list's width, and the page's width from which it shows.
    static let listWidth: CGFloat = 260
    static let listFrom: CGFloat = 900
    /// Wide enough for the old file beside the new, as on the Review page.
    static let sideBySideFrom: CGFloat = 820

    private var showsList: Bool { width >= Self.listFrom && diff.files.count > 1 }
    private var diffWidth: CGFloat { showsList ? width - Self.listWidth : width }
    private var sideBySide: Bool { diffWidth >= Self.sideBySideFrom }
    /// Under this, the line at the top takes two lines: a phone's width.
    static let narrowUnder: CGFloat = 600
    private var narrow: Bool { width > 0 && width < Self.narrowUnder }

    var body: some View {
        ScrollViewReader { proxy in
            HStack(alignment: .top, spacing: 0) {
                if showsList {
                    fileList(proxy)
                        .frame(width: Self.listWidth)
                    Divider()
                }
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0, pinnedViews: [.sectionHeaders]) {
                        summary(proxy).padding(.bottom, 12)
                        // As the web page's empty state (#5477): a pull request with no files.
                        if diff.files.isEmpty {
                            Text("No files changed.")
                                .appText(.reading)
                                .foregroundStyle(.secondary)
                                .padding(.vertical, 40)
                                .frame(maxWidth: .infinity)
                        }
                        ForEach(diff.files) { file in
                            Section {
                                if !collapsed.contains(file.path) { fileBody(file) }
                                Color.clear.frame(height: 16)
                            } header: {
                                header(file)
                            }
                            .id(file.path)
                        }
                    }
                    .padding(20)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
            #if os(iOS)
            .sheet(isPresented: $filesSheet) { filesSheetPage(proxy) }
            #endif
            .task {
                rows = Dictionary(diff.files.map { ($0.path, AppViewDiffPage.rows($0)) }, uniquingKeysWith: { a, _ in a })
                if let path = diff.path {
                    try? await Task.sleep(for: .milliseconds(100))
                    go(to: path, proxy)
                }
            }
        }
    }

    private func go(to path: String, _ proxy: ScrollViewProxy) {
        chosen = path
        collapsed.remove(path)
        withAnimation(.easeOut(duration: 0.2)) { proxy.scrollTo(path, anchor: .top) }
    }

    // MARK: The line at the top

    private func summary(_ proxy: ScrollViewProxy) -> some View {
        let added = diff.files.reduce(0) { $0 + $1.additions }, removed = diff.files.reduce(0) { $0 + $1.deletions }
        let changed = HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text("\(diff.files.count) \(diff.files.count == 1 ? "file" : "files") changed")
                .appText(.supporting)
            Self.counts(added: added, removed: removed)
        }
        return VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                if !showsList, diff.files.count > 1 {
                    filesMenu(proxy)
                }
                // A phone's width (#5479): what changed goes on a line of its own, so Open on GitHub keeps one line.
                if !narrow { changed }
                Spacer(minLength: 0)
                if sideBySide {
                    Picker("Show", selection: $layout) {
                        ForEach(FileDiffView.Layout.allCases) { Text($0.title).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .fixedSize()
                }
                if let url = diff.url {
                    Button("Open on \(diff.site)") { openLink(url) }.buttonStyle(.paper).appText(.fine).fixedSize()
                }
            }
            if narrow { changed }
            if diff.cut {
                Text("The diff is too long to show every file here. A file with no lines below is on \(diff.site).")
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    /// *Files ▾* on a narrow page: each file, to go to it. A menu on the Mac; on the Remote a
    /// sheet with the file list's rows (#5479).
    private func filesMenu(_ proxy: ScrollViewProxy) -> some View {
        #if os(iOS)
        Button { filesSheet = true } label: {
            HStack(spacing: 3) {
                Text("Files")
                Image(icon: "chevron.down").appText(.fine).accessibilityHidden(true)
            }
        }
        .buttonStyle(.paper)
        .fixedSize()
        .accessibilityLabel("Files")
        .accessibilityHint("Shows the files, to go to one")
        #else
        Menu {
            ForEach(diff.files) { file in
                Button {
                    go(to: file.path, proxy)
                } label: {
                    Text("\(file.path)  +\(file.additions) −\(file.deletions)")
                }
            }
        } label: {
            Text("Files")
        }
        .fixedSize()
        .help("Go to a file")
        #endif
    }

    #if os(iOS)
    /// The sheet of *Files ▾*: a row per file, as the wide page's list; a tap closes the sheet
    /// and goes to the file.
    private func filesSheetPage(_ proxy: ScrollViewProxy) -> some View {
        NavigationStack {
            List(diff.files) { file in
                Button {
                    filesSheet = false
                    Task {
                        // After the sheet has gone, so the scroll is seen.
                        try? await Task.sleep(for: .milliseconds(350))
                        go(to: file.path, proxy)
                    }
                } label: { listRow(file) }
                .buttonStyle(.plain)
                .listRowInsets(EdgeInsets(top: 2, leading: 12, bottom: 2, trailing: 12))
                .accessibilityLabel("\(file.path), \(file.change), \(file.additions) added, \(file.deletions) removed")
            }
            .listStyle(.plain)
            .navigationTitle("\(diff.files.count) files")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { filesSheet = false }
                }
            }
        }
        .presentationDetents([.medium, .large])
        // Solid, so the diff's colours do not show through the rows.
        .presentationBackground(Paper.ground)
    }
    #endif

    /// *+12 −3*: the signs and the numbers, in the forge's green and red.
    static func counts(added: Int, removed: Int) -> some View {
        HStack(spacing: 4) {
            Text("+\(added)").foregroundStyle(Paper.forgeOpen)
            Text("−\(removed)").foregroundStyle(Paper.forgeClosed)
        }
        .appText(.fine)
        .fontWeight(.semibold)
        .monospacedDigit()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(added) lines added, \(removed) removed")
    }

    // MARK: The file list

    private func fileList(_ proxy: ScrollViewProxy) -> some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 2) {
                Text("Files").appText(.fine).fontWeight(.semibold).foregroundStyle(.secondary)
                    .padding(.horizontal, 8).padding(.bottom, 4)
                ForEach(diff.files) { file in
                    Button { go(to: file.path, proxy) } label: { listRow(file) }
                        .buttonStyle(.plain)
                        .help(file.path)
                        .accessibilityLabel("\(file.path), \(file.change), \(file.additions) added, \(file.deletions) removed")
                }
            }
            .padding(12)
        }
    }

    private func listRow(_ file: AppViewDiff.File) -> some View {
        let name = (file.path as NSString).lastPathComponent
        let folder = (file.path as NSString).deletingLastPathComponent
        return HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(icon: Self.symbol(file.change))
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 1) {
                Text(name).appText(.supporting).lineLimit(1).truncationMode(.middle)
                if !folder.isEmpty {
                    Text(folder).appText(.fine).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Self.counts(added: file.additions, removed: file.deletions)
        }
        .padding(.horizontal, 8).padding(.vertical, 5)
        .contentShape(Rectangle())
        .background(RoundedRectangle(cornerRadius: Paper.Radius.control).fill(chosen == file.path ? Paper.well : .clear))
    }

    static func symbol(_ change: String) -> String {
        switch change {
        case "added": "plus.square"
        case "removed", "deleted": "minus.square"
        case "renamed": "arrow.right.square"
        default: "dot.square"
        }
    }

    // MARK: A file

    /// The file's header, pinned at the top while its lines scroll: the fold, the path, what
    /// happened to it when not a change, and its counts.
    private func header(_ file: AppViewDiff.File) -> some View {
        let open = !collapsed.contains(file.path)
        return Button {
            if open { collapsed.insert(file.path) } else { collapsed.remove(file.path) }
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(icon: open ? "chevron.down" : "chevron.right")
                    .foregroundStyle(.secondary)
                    .frame(width: 14)
                    .accessibilityHidden(true)
                Text(file.previousPath.map { "\($0) → \(file.path)" } ?? file.path)
                    .appText(.fine)
                    .fontWeight(.semibold)
                    .lineLimit(1)
                    .truncationMode(.head)
                if file.change != "modified" {
                    Text(file.change).appText(.fine).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
                Self.counts(added: file.additions, removed: file.deletions)
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(Paper.raised)
        // An open file's header and its lines are one card: the header has its top corners.
        .clipShape(Self.top(open))
        .overlay(Self.top(open).strokeBorder(Paper.rule, lineWidth: 1))
        // The page shows under a pinned header's corners: the ground fills them.
        .background(Paper.ground)
        .help(open ? "Fold this file" : "Show this file")
        .accessibilityLabel(file.path)
        .accessibilityValue(open ? "Shown" : "Folded")
        .accessibilityHint(open ? "Folds the file" : "Shows the file")
    }

    /// The header's shape: its top corners round when its lines hang under it, all four when folded.
    static func top(_ open: Bool) -> UnevenRoundedRectangle {
        let r = Paper.Radius.card
        return UnevenRoundedRectangle(topLeadingRadius: r, bottomLeadingRadius: open ? 0 : r,
                                      bottomTrailingRadius: open ? 0 : r, topTrailingRadius: r)
    }

    /// The lines' shape, under the header: the bottom corners round.
    static let bottom = UnevenRoundedRectangle(bottomLeadingRadius: Paper.Radius.card, bottomTrailingRadius: Paper.Radius.card)

    @ViewBuilder
    private func fileBody(_ file: AppViewDiff.File) -> some View {
        Group {
            if let rows = rows[file.path], !rows.isEmpty {
                FileDiffView(rows: rows, path: file.path, layout: sideBySide ? layout : .unified)
            } else {
                Text(file.note ?? "No lines to show.")
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Paper.well)
            }
        }
        .clipShape(Self.bottom)
        .overlay(Self.bottom.strokeBorder(Paper.rule, lineWidth: 1))
        // Over the header's bottom edge, so the two edges are one line.
        .padding(.top, -1)
    }
}
