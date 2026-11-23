import AgentsKitCore
import SwiftUI

/// A pull request's Files changed page (#5478, `specs/pr-files-changed-page/spec.md`): pushed in
/// the pull request's place by its Files changed button, not a sheet, so Back goes to the pull
/// request. The diff (`forge/diff`) is read from the machine that read the pull request, else
/// from the first machine that answers, and drawn by `DiffFilesView`: the file list beside the
/// diff on a wide page, *Files ▾* on a narrow one, headers that stay and fold. On the Mac the
/// place, *Pull requests › #12 › Files changed*, and Refresh are the toolbar's; on the Remote
/// (#5479) the bar has ‹ *#12* and the page pulls to refresh, and *Files ▾* opens a sheet. The footer bar says
/// Loading…, Updating… or what failed (#5970); the diff is read each time the page opens and is never kept, so it has
/// no "Last update" line.
struct WorkFilesPage: View {
    let model: any WorkModel
    let place: WorkPlace

    @Environment(\.openURL) private var openURL
    @State private var diff: AppViewDiff?
    @State private var problem: String?
    @State private var reading = false
    @State private var asked = 0

    private var ref: String { place.ref ?? "" }
    private var node: DaemonAPI.LineageNode { .init(kind: place.kind.rawValue, ref: ref) }

    var body: some View {
        VStack(spacing: 0) {
            Group {
                if let diff {
                    // A read that runs or failed over this diff is in the footer; the diff stays at full opacity.
                    DiffFilesView(diff: diff, openLink: { openURL($0) })
                } else if let problem, !reading {
                    couldNotRead(problem)
                } else {
                    placeholders
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            WorkPageBar(state: WorkLook.pageFooterState(drawn: diff != nil, reading: reading, fromDevice: false,
                                                        lines: problem.map { [$0] } ?? []),
                        count: nil, read: nil) { asked += 1 }
        }
        #if os(macOS)
        .toolbar { toolbar }
        .toolbar(removing: .title)
        #else
        .refreshable { asked += 1 }
        .navigationTitle("Files changed")
        .navigationBarTitleDisplayMode(.inline)
        // ‹ #12 takes the split view's own back, as on the pull request's page.
        .navigationBarBackButtonHidden(true)
        .toolbar { barItems }
        #endif
        .workTask(id: asked) { await load() }
    }

    #if os(macOS)
    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .navigation) {
            WorkTrailView(model: model, parts: Self.trail(ref: ref)).fixedSize()
        }
        ToolbarSpacer(.flexible)
        ToolbarItemGroup(placement: .primaryAction) {
            WorkRefreshButton(running: reading) { asked += 1 }
        }
    }
    #endif

    #if os(iOS)
    /// ‹ *#12*: back to the pull request, the place before (#4402, as the Checks page).
    @ToolbarContentBuilder
    private var barItems: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            // An HStack, not a Label: the bar draws a Label's icon alone.
            Button { back() } label: {
                HStack(spacing: 4) {
                    Image(icon: "chevron.left").fontWeight(.semibold)
                    Text(WorkList.shortRef(ref))
                }
            }
            .accessibilityLabel(WorkList.shortRef(ref))
        }
    }

    private func back() {
        let pull = WorkPlace(kind: .pr, ref: ref)
        let history = model.workHistory
        if history.canGoBack, history.places[history.at - 1] == pull { model.workHistory.back() } else { model.showWork(pull) }
    }
    #endif

    /// *Pull requests › #12 › Files changed*: the pull request's number opens its page.
    static func trail(ref: String) -> [WorkTrail.Part] {
        var parts = WorkTrail.parts(kind: .pr, ref: ref)
        if var last = parts.popLast() {
            last.target = .item(.pr, ref: ref)
            parts.append(last)
        }
        parts.append(WorkTrail.Part(words: "Files changed"))
        return parts
    }

    /// Placeholder lines while the first read runs (#5970), as the other Work pages have: a file's header, then
    /// lines of code of varied length, with the slow shimmer.
    private var placeholders: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(0 ..< 3, id: \.self) { file in
                RoundedRectangle(cornerRadius: 3).frame(width: 260, height: 12).padding(.top, file == 0 ? 0 : 10)
                ForEach(0 ..< 6, id: \.self) { line in
                    RoundedRectangle(cornerRadius: 2).frame(height: 9)
                        .frame(maxWidth: CGFloat([520, 380, 460, 300, 560, 240][(line + file) % 6]))
                }
            }
        }
        .foregroundStyle(Paper.wash)
        .padding(20)
        .redacted(reason: .placeholder)
        .modifier(WorkShimmer())
        .accessibilityElement()
        .accessibilityLabel("Reading the files changed")
    }

    private func couldNotRead(_ words: String) -> some View {
        VStack(spacing: 8) {
            Image(icon: "exclamationmark.triangle")
                // The state's own glyph, larger than any text step: a marked decorative glyph.
                .font(.system(size: 28))
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text("Could not read the files of \(WorkList.shortRef(ref))").appText(.reading).fontWeight(.semibold)
            Text(words).appText(.supporting).foregroundStyle(.secondary).multilineTextAlignment(.center).textSelection(.enabled)
            HStack(spacing: 12) {
                Button(WorkLook.tryAgain) { asked += 1 }.buttonStyle(.paper)
                if let url = WorkList.url(node) {
                    Button(WorkList.openOnWords(ref)) { openURL(url) }.linkStyle()
                }
            }
            .padding(.top, 4)
        }
        .padding(.vertical, 60)
        .frame(maxWidth: .infinity)
    }

    private func load() async {
        let mine = asked
        let kept = await model.keptWorkRead(WorkItemPage.KeptItem.self, .item, ref)?.value
        // The machine that read the pull request first, then every machine at once (#5449).
        var hosts = model.forgeMachines.map(\.id)
        if let host = kept?.host, let at = hosts.firstIndex(of: host) {
            hosts.remove(at: at)
            hosts.insert(host, at: 0)
        }
        guard !hosts.isEmpty else { problem = WorkList.noForgeHere; return }
        reading = true
        // A Try again after nothing was read shows the placeholders, not the old error, while it runs.
        if diff == nil { problem = nil }
        defer { if asked == mine { reading = false } }
        let (model, node) = (model, node)
        func read(_ host: MachineRef) async -> Result<DaemonAPI.ForgeDiff, Error> {
            do {
                return .success(try await model.forge(DaemonAPI.Method.forgeDiff, DaemonAPI.ForgeNodeRequest(node: node),
                                                      returning: DaemonAPI.ForgeDiff.self, on: host))
            } catch {
                return .failure(error)
            }
        }
        var got: DaemonAPI.ForgeDiff?
        var last: String?
        if kept != nil, let first = hosts.first {
            switch await read(first) {
            case let .success(diff): got = diff
            case let .failure(error): last = WorkList.failureWords(error); hosts.removeFirst()
            }
        }
        if got == nil, !hosts.isEmpty {
            let rest = hosts
            (got, last) = await withTaskGroup(of: Result<DaemonAPI.ForgeDiff, Error>.self,
                                              returning: (DaemonAPI.ForgeDiff?, String?).self) { group in
                for host in rest { group.addTask { await read(host) } }
                var last = last
                for await result in group {
                    switch result {
                    case let .success(diff):
                        group.cancelAll()
                        return (diff, last)
                    case let .failure(error):
                        last = WorkList.failureWords(error)
                    }
                }
                return (nil, last)
            }
        }
        guard asked == mine else { return }
        if let got {
            let title = kept?.got.item.title ?? model.workLists[.pr].all.first { $0.node.ref == ref }?.title ?? ""
            diff = got.appViewDiff(title: "\(WorkList.shortRef(ref)) \(title)", site: WorkList.forgeName(ref))
            problem = nil
        } else {
            problem = last ?? WorkList.notReadHere
        }
    }
}
