import AgentsKitCore
import AppKit
import SwiftUI

/// The list's state (#3321), kept by the model while the window is open, so a doc's page goes back
/// to the list as it was left.
@MainActor @Observable
final class DocsListState {
    var lists: [DocsList.HostDocs]?
    var sort = DocsList.Sort.updated
    /// A source's key, or "" for All.
    var only = ""
    /// A project's id, or "" for All projects (#3408).
    var inProject = ""
    /// The organisation's projects' names, by id; none without a control plane.
    var projectNames: [String: String] = [:]
    var query = ""
    var results: [DocsList.Result]?
    var searching = false
    var asked = 0
    /// The machine each doc was listed on, so its page reads it there first.
    var listedOn: [String: MachineRef] = [:]
    /// The line a doc's window is asked to go to (#5481), until it has.
    var windowLine: [String: Int] = [:]

    var ask: [String] { [String(asked)] }

    /// Most pages of docs/listAll one read follows: the control plane asks the machines, at most 500 docs a page.
    static let mostPages = 4

    /// Every machine's docs in one call to the control plane (#5571), the next pages while there are some, up to
    /// mostPages, as the web page reads them.
    func load(model: AppModel) async {
        let asked = asked
        lists = nil
        var read: [DocsList.HostDocs] = []
        if model.docsMachines.isEmpty {
            // No machine reads docs: nothing to ask.
        } else if let control = model.controlRequests {
            do {
                var cursor: String?
                for _ in 0..<Self.mostPages {
                    let request = DaemonAPI.DocsListAllRequest(limit: 500, cursor: cursor)
                    // In this Mac's lock, the window asks each machine itself (#5996).
                    let got: DaemonAPI.DocsListAll
                    if let here = try await model.answerAllHere(DaemonAPI.Method.docsListAll, request,
                                                                returning: DaemonAPI.DocsListAll.self) {
                        got = here
                    } else {
                        got = try await control.call(DaemonAPI.Method.docsListAll, request, returning: DaemonAPI.DocsListAll.self)
                    }
                    read = DocsList.hostDocs(got, into: read, thisMac: model.thisMacHostID)
                    for d in got.docs where listedOn[d.doc.ref] == nil { listedOn[d.doc.ref] = d.machine }
                    cursor = got.cursor
                    if cursor == nil { break }
                }
            } catch {
                read = [.init(host: MachineRef(rawValue: ""), name: "Control plane", problem: error.localizedDescription)]
            }
        }
        await readProjectNames(model: model)
        // A newer Refresh wins.
        if asked == self.asked { lists = read }
    }

    /// The projects' names, from the control plane; a failed read keeps the last.
    func readProjectNames(model: AppModel) async {
        guard let control = model.controlRequests,
              let got = try? await control.call(DaemonAPI.Method.projectsList, DaemonAPI.Empty(),
                                                returning: DaemonAPI.ProjectList.self) else { return }
        projectNames = Dictionary(got.projects.map { ($0.id, $0.name) }, uniquingKeysWith: { a, _ in a })
    }

    /// The project of a machine's folder, by the folder's path, as its summary names it; a project
    /// not on the list is said by the folder's own name.
    func projectOf(model: AppModel) -> DocsList.ProjectOf {
        let folders = model.folders
        let names = projectNames
        return { host, folder in
            guard let folder else { return nil }
            let wanted = Folder.standardize(URL(fileURLWithPath: folder))
            guard let summary = folders.first(where: { $0.host == host && Folder.standardize($0.folder.folder) == wanted }),
                  let id = summary.project else { return nil }
            return DocsList.Project(id: id, name: names[id] ?? summary.name)
        }
    }

    /// The sources the list shows, each project's once.
    func rows(model: AppModel) -> [DocsList.SourceRow] {
        DocsList.sources(lists ?? [], projectOf: projectOf(model: model))
    }

    func search(model: AppModel) async {
        let words = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !words.isEmpty else { results = nil; searching = false; return }
        searching = true
        // The typing rests first; a newer search cancels this one.
        try? await Task.sleep(for: .milliseconds(250))
        guard !Task.isCancelled else { return }
        // One source is asked of each machine that has a folder of it; a connector of every machine.
        let rows = rows(model: model)
        let row = rows.first { $0.key == only }
        let shown = rows.filter { (only.isEmpty || $0.key == only) && (inProject.isEmpty || $0.project?.id == inProject) }
        var got: [(host: MachineRef, results: [DaemonAPI.DocsSearchResult])] = []
        for machine in model.docsMachines {
            let sources = row.map { DocsList.searchSources($0, on: machine.id) }
            if sources?.isEmpty == true { continue }
            let found = try? await model.docs(DaemonAPI.Method.docsSearch,
                                              DaemonAPI.DocsSearchRequest(query: words, sources: sources, limit: 50),
                                              returning: DaemonAPI.DocsSearch.self, on: machine.id)
            got.append((machine.id, found?.results ?? []))
        }
        guard !Task.isCancelled else { return }
        for list in got { for r in list.results where listedOn[r.doc.ref] == nil { listedOn[r.doc.ref] = list.host } }
        results = DocsList.results(DocsList.merged(got), in: shown)
        searching = false
    }
}

/// Docs (#3321, spec 3319), as the web page's Docs section: every machine's docs (`docs/listAll`, one call, #5571),
/// grouped by source (each project once, #3408), a folder's by its subfolders, with a Project filter; search (`docs/search`) with each match's line
/// and snippet; and a doc's page (`docs/read`), read only. The window's Back and Forward go along the places (#5784).
struct DocsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let place = model.docsHistory.current ?? DocsPlace()
        Group {
            if let ref = place.ref {
                DocPage(ref: ref, line: place.line).id(place)
            } else {
                DocsListPane(list: model.docsList)
            }
        }
        .modifier(WorkWindows())
        // Back and Forward are the window's, along every page (#5784).
        .navigationTitle("Docs")
    }
}

private struct DocsListPane: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWorkWindow) private var openWorkWindow
    @Bindable var list: DocsListState

    private var words: String { list.query.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        VStack(spacing: 0) {
            filters.padding(.horizontal, 16).padding(.vertical, 8)
            Divider()
            content
        }
        .task(id: list.ask) { await list.load(model: model) }
        .task(id: [list.query, list.only, list.inProject]) { await list.search(model: model) }
    }

    private var filters: some View {
        HStack(spacing: 12) {
            TextField("Search docs", text: $list.query)
                .textFieldStyle(.roundedBorder)
                .accessibilityLabel("Search docs")
            let rows = list.rows(model: model)
            let projects = DocsList.projects(rows)
            if !projects.isEmpty {
                Picker("Project", selection: $list.inProject) {
                    Text("All projects").tag("")
                    ForEach(projects) { Text($0.name).tag($0.id) }
                }
                .fixedSize()
                .onChange(of: list.inProject) { _, project in
                    // A source of another project is not in the list any more.
                    if !project.isEmpty, rows.first(where: { $0.key == list.only })?.project?.id != project { list.only = "" }
                }
            }
            Picker("Source", selection: $list.only) {
                Text("All").tag("")
                ForEach(rows.filter { list.inProject.isEmpty || $0.project?.id == list.inProject }) { Text($0.heading).tag($0.key) }
            }
            .fixedSize()
            if words.isEmpty {
                Picker("Sort", selection: $list.sort) {
                    ForEach(DocsList.Sort.allCases, id: \.self) { Text($0.title).tag($0) }
                }
                .fixedSize()
            }
            Button("Refresh") { list.asked += 1 }
                .linkStyle()
        }
    }

    @ViewBuilder
    private var content: some View {
        if model.docsMachines.isEmpty {
            message(DocsList.noDocsHere)
        } else if !words.isEmpty {
            results
        } else if let lists = list.lists {
            let groups = DocsList.groups(lists, sort: list.sort, only: list.only, projectOf: list.projectOf(model: model),
                                         project: list.inProject)
            List {
                ForEach(lists.compactMap { l in l.problem.map { "\(l.name): \($0)" } }, id: \.self) { line in
                    Label(line, icon: "exclamationmark.triangle").foregroundStyle(.secondary)
                }
                if groups.isEmpty {
                    Text(DocsList.noDocs).foregroundStyle(.secondary)
                }
                ForEach(groups) { group in
                    Section {
                        if group.row.source.kind == .folder, let problem = DocsList.problem(group.row.source) {
                            Text(problem).foregroundStyle(.secondary)
                        } else if group.row.source.state == .read, group.shelves.isEmpty {
                            Text("No docs.").appText(.fine).foregroundStyle(.secondary)
                        }
                        ForEach(group.shelves) { shelf in
                            if !shelf.dir.isEmpty {
                                Text("▸ \(shelf.dir)").appText(.supporting).foregroundStyle(.secondary)
                            }
                            ForEach(shelf.docs) { doc in
                                Button { open(DocsPlace(ref: doc.ref)) } label: {
                                    DocRow(doc: doc, name: DocsList.underShelf(doc, shelf.dir))
                                }
                                .buttonStyle(.plain)
                                .padding(.leading, shelf.dir.isEmpty ? 0 : 16)
                                .help(doc.ref)
                                .docWindowMenu(doc.ref)
                            }
                        }
                    } header: {
                        sourceHeading(group.row)
                    }
                }
            }
            .listStyle(.inset)
        } else {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func open(_ place: DocsPlace) { model.followDoc(place, window: openWorkWindow) }

    private func sourceHeading(_ row: DocsList.SourceRow) -> some View {
        HStack(spacing: 8) {
            Text(row.heading.uppercased()).appText(.fine).fontWeight(.semibold)
            if row.source.kind == .connector, let problem = DocsList.problem(row.source) {
                Text("— \(problem)").appText(.fine).foregroundStyle(.secondary)
            }
            if row.source.state == .notMapped, let folder = row.source.folder {
                Button("Map it…") {
                    _ = model.offerStart(AppViewStart(prompt: DocsList.mapItPrompt(row.source)),
                                         in: FolderKey(host: row.host, folder: URL(fileURLWithPath: folder)))
                }
                .linkStyle()
                .help("Start an agent that maps this docs server’s search and read tools")
            }
            Spacer(minLength: 0)
        }
    }

    @ViewBuilder
    private var results: some View {
        if let results = list.results, !results.isEmpty {
            List {
                ForEach(results) { found in
                    VStack(alignment: .leading, spacing: 4) {
                        Button { open(DocsPlace(ref: found.result.doc.ref)) } label: {
                            HStack(alignment: .firstTextBaseline, spacing: 6) {
                                Text(found.result.doc.title).fontWeight(.semibold).lineLimit(1)
                                if let path = found.result.doc.path {
                                    Text(path).appText(.fine).monospaced().foregroundStyle(.secondary).lineLimit(1)
                                }
                                Spacer(minLength: 0)
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .help(found.result.doc.ref)
                        .docWindowMenu(found.result.doc.ref)
                        ForEach(Array(found.result.matches.enumerated()), id: \.offset) { _, match in
                            Button { open(DocsPlace(ref: found.result.doc.ref, line: match.line)) } label: {
                                HStack(alignment: .firstTextBaseline, spacing: 8) {
                                    Text(match.line.map(String.init) ?? "")
                                        .monospacedDigit().foregroundStyle(.secondary)
                                        .frame(minWidth: 32, alignment: .trailing)
                                    Text(match.snippet).lineLimit(2).foregroundStyle(.tint)
                                    Spacer(minLength: 0)
                                }
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .padding(.leading, 16)
                            .docWindowMenu(found.result.doc.ref, line: match.line)
                        }
                    }
                    .padding(.vertical, 3)
                }
            }
            .listStyle(.inset)
        } else {
            message(list.searching || list.results == nil ? "Searching…" : DocsList.noResults)
        }
    }

    private func message(_ text: String) -> some View {
        Text(text)
            .appText(.supporting)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.center)
            .padding(24)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// A row: a folder doc's path and its title beside it, or a connector doc's title; when it changed.
private struct DocRow: View {
    let doc: DaemonAPI.Doc
    let name: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if doc.path != nil {
                Text(name).monospaced().lineLimit(1)
            } else {
                Text(name).fontWeight(.semibold).lineLimit(1)
            }
            if let beside = DocsList.titleBeside(doc) {
                Text(beside).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
            }
            Spacer(minLength: 8)
            Text(DocsList.updatedWords(doc)).appText(.fine).monospacedDigit().foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
        .contentShape(Rectangle())
    }
}

/// A doc's page: its title, ref and when it changed; Open in Files or in its vendor's app, Copy
/// link and Copy ref; and its Markdown, drawn as the Files pane draws a page, read only. A folder
/// doc's page follows the file: it reads it again every few seconds while it is shown.
struct DocPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.showMainWindow) private var showMainWindow
    @Environment(\.openURL) private var openURL
    let ref: String
    let line: Int?
    /// Set in a window of its own (#5481): a link to another doc opens there.
    var window: DocWindowModel?

    @State private var reading: (host: MachineRef, reading: DaemonAPI.DocsReading)?
    @State private var problem: String?
    @State private var copied: String?
    @State private var startProblem: String?

    /// How often a folder doc is read again while its page is shown.
    private static let followEvery: Duration = .seconds(5)

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header.padding(.horizontal, 16).padding(.vertical, 10)
            Divider()
            if let problem {
                Text(problem).foregroundStyle(.secondary).padding(16)
                Spacer()
            } else if let reading {
                if let next = reading.reading.next {
                    Text("Showing lines \(reading.reading.from)–\(reading.reading.to) of \(reading.reading.lines); the rest from line \(next) is not shown.")
                        .appText(.fine).foregroundStyle(.secondary).padding(.horizontal, 16).padding(.vertical, 6)
                }
                LivePage(text: reading.reading.text, url: pageURL(reading.reading.doc), line: line)
                    .environment(\.pageActions, pageActions(reading.reading.doc, on: reading.host))
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .navigationTitle(window == nil ? "Docs" : doc.map { $0.title.isEmpty ? DocsList.shortForm($0) : $0.title } ?? ref)
        .toolbar {
            if window == nil {
                ToolbarItem(placement: .primaryAction) { WorkWindowToolbarButton(key: .doc(ref)) }
            }
        }
        // A window opened again at launch is there before the machines are.
        .task(id: machinesKnown) {
            guard machinesKnown else { return }
            // The names, for the project the page says, while the doc is read.
            if model.docsList.projectNames.isEmpty { Task { await model.docsList.readProjectNames(model: model) } }
            await read()
        }
    }

    private var machinesKnown: Bool { !model.docsMachines.isEmpty || model.controlPlaneListed || model.controlPlaneHosts == nil }

    private var doc: DaemonAPI.Doc? { reading?.reading.doc }

    private var project: DocsList.Project? {
        guard let reading else { return nil }
        return model.docsList.projectOf(model: model)(reading.host, reading.reading.doc.folder)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(doc.map(DocsList.shortForm) ?? ref).appText(.title).lineLimit(2)
            // The project it is in (#3408), as an issue's or a PR's page says it.
            Text([project?.name ?? "", ref, doc.map { "updated " + DocsList.updatedWords($0) } ?? ""]
                .filter { !$0.isEmpty && $0 != "updated " }
                .joined(separator: " · "))
                .appText(.fine).foregroundStyle(.secondary).textSelection(.enabled).lineLimit(1).truncationMode(.middle)
            HStack(spacing: 8) {
                if let doc, let host = reading?.host { startMenu(doc, on: host) }
                if let doc, !DocsList.isFolderDoc(doc), let url = doc.url.flatMap(URL.init(string:)) {
                    Button("Open in \(DocsList.vendorName(ref))") { openURL(url) }
                } else if let doc, let place = filesPlace(doc) {
                    Button("Open in Files") { _ = openFile(place.path, folder: place.folder, on: place.host) }
                        .help("Open this file as the folder’s page")
                }
                Button(copied == "link" ? "Copied" : "Copy link") { copy("link", DocsList.windowAddress(ref)) }
                    .help(DocsList.windowAddress(ref))
                Button(copied == "ref" ? "Copied" : "Copy ref") { copy("ref", ref) }
                    .help(ref)
                Spacer(minLength: 0)
                Button("All docs") {
                    model.showDocs(DocsPlace())
                    showMainWindow?.run()
                }
                    .linkStyle()
            }
            if let startProblem {
                SheetNote(text: startProblem, reservesSpace: false)
            }
        }
    }

    /// Start Agent ▾ (#3322): a new session from this doc, in its folder; a connector doc's asks
    /// for the folder, as each start's own menu of this machine's folders.
    @ViewBuilder
    private func startMenu(_ doc: DaemonAPI.Doc, on host: MachineRef) -> some View {
        Menu("Start Agent") {
            ForEach(DocsList.Start.allCases, id: \.self) { start in
                if DocsList.isFolderDoc(doc) {
                    Button(start.title) { self.start(start, doc, in: doc.folder ?? "", on: host) }
                        .help(start.help)
                } else {
                    Menu(start.title) {
                        let folders = model.folders.filter { $0.host == host }
                        if folders.isEmpty { Text("No folder on this machine") }
                        ForEach(folders) { folder in
                            Button(folder.name) { self.start(start, doc, in: folder.folder.folder.path, on: host) }
                        }
                    }
                    .help(start.help)
                }
            }
        }
        .fixedSize()
        .help(DocsList.isFolderDoc(doc) ? "A new session in this doc’s folder, working from it"
            : "A new session working from this doc, in a folder you choose")
    }

    private func start(_ start: DocsList.Start, _ doc: DaemonAPI.Doc, in folder: String, on host: MachineRef) {
        startProblem = folder.isEmpty ? "This doc has no folder here." : model.offerStart(
            AppViewStart(prompt: start.prompt(doc.ref), labels: start.labels(doc)),
            in: FolderKey(host: host, folder: URL(fileURLWithPath: folder)))
        if startProblem == nil { showMainWindow?.run() }
    }

    private func openFile(_ path: String, folder: String, on host: MachineRef) -> Bool {
        guard model.showDocFile(path, folder: folder, on: host) else { return false }
        showMainWindow?.run()
        return true
    }

    /// A link in the window's doc: the page of the doc it names, in this window, or else the folder's
    /// page of the file, in the main one.
    private func openLinked(_ ref: String, else file: @escaping @MainActor () -> Bool, on host: MachineRef) {
        Task {
            let asked = DaemonAPI.DocsReadRequest(ref: ref, from: 1, to: 1)
            if let got = try? await model.docs(DaemonAPI.Method.docsRead, asked, returning: DaemonAPI.DocsReading.self, on: host) {
                model.docsList.listedOn[got.doc.ref] = host
                window?.go(DocsPlace(ref: got.doc.ref))
            } else {
                _ = file()
            }
        }
    }

    private func copy(_ what: String, _ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        copied = what
        Task {
            try? await Task.sleep(for: .seconds(1.5))
            if copied == what { copied = nil }
        }
    }

    /// Where Open in Files goes: the folder's own page of the file, as a pinned page opens.
    private func filesPlace(_ doc: DaemonAPI.Doc) -> (path: String, folder: String, host: MachineRef)? {
        guard let host = reading?.host, let folder = doc.folder, let path = doc.path else { return nil }
        return (path, folder, host)
    }

    /// Read only; a page in the doc's folder that a link names opens as the folder's page,
    /// as Open in Files does. Anything else is the host's to open.
    private func pageActions(_ doc: DaemonAPI.Doc, on host: MachineRef) -> PageActions {
        var actions = PageActions()
        guard let folder = doc.folder else { return actions }
        actions.openFile = { file in
            guard let relative = PinRules.relative(file.path(percentEncoded: false), in: URL(filePath: folder)),
                  PinRules.kind(relative) != nil else { return false }
            if window != nil, let next = DocsList.ref(of: doc, at: relative) {
                openLinked(next, else: { openFile(relative, folder: folder, on: host) }, on: host)
                return true
            }
            return openFile(relative, folder: folder, on: host)
        }
        return actions
    }

    /// The document's own place, so a picture beside a folder doc can be found.
    private func pageURL(_ doc: DaemonAPI.Doc) -> URL {
        if let folder = doc.folder, let path = doc.path {
            return URL(fileURLWithPath: folder).appendingPathComponent(path)
        }
        return URL(fileURLWithPath: "/").appendingPathComponent(doc.title.isEmpty ? "doc" : doc.title)
    }

    /// The machine that listed it first, then this Mac, then the others; and again every few seconds
    /// for a folder doc.
    private func read() async {
        let machines = model.docsMachines
        let first = model.docsList.listedOn[ref]
        let order = machines.filter { $0.id == first } + machines.filter { $0.id != first }
        var last: String?
        for machine in order {
            do {
                let got = try await model.docs(DaemonAPI.Method.docsRead, DaemonAPI.DocsReadRequest(ref: ref),
                                               returning: DaemonAPI.DocsReading.self, on: machine.id)
                reading = (machine.id, got)
                problem = nil
                await follow(on: machine.id)
                return
            } catch {
                if Task.isCancelled { return }
                last = error.localizedDescription
            }
        }
        problem = machines.isEmpty ? DocsList.noDocsHere : last ?? "No machine here has this doc."
    }

    private func follow(on host: MachineRef) async {
        guard let doc, DocsList.isFolderDoc(doc) else { return }
        while !Task.isCancelled {
            try? await Task.sleep(for: Self.followEvery)
            guard !Task.isCancelled else { return }
            if let got = try? await model.docs(DaemonAPI.Method.docsRead, DaemonAPI.DocsReadRequest(ref: ref),
                                               returning: DaemonAPI.DocsReading.self, on: host),
               got.text != reading?.reading.text {
                reading = (host, got)
            }
        }
    }
}
