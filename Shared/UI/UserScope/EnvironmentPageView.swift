import AgentsKitCore
import SwiftUI
#if os(macOS)
import AppKit
#else
import UIKit
#endif

/// A machine the Environment page can show (#5260).
struct EnvironmentMachine: Identifiable, Hashable {
    let id: MachineRef
    let name: String
}

/// A running session, for As a session sees it.
struct EnvironmentSession: Identifiable, Hashable {
    let id: UUID
    let title: String
}

/// Where the Environment page reads and writes (#5260): the window's model and the Remote's, each through the hosts it
/// reaches.
@MainActor
protocol EnvironmentSource {
    /// The machines whose files the page shows, this client's own first; only those whose host knows `env/1`.
    var environmentMachines: [EnvironmentMachine] { get }
    /// The client for a machine's host, or nil while it cannot be reached.
    func environmentClient(_ host: MachineRef) -> DaemonClient?
    /// The running sessions on a machine.
    func environmentSessions(_ host: MachineRef) -> [EnvironmentSession]
    /// The folders a machine has, as paths.
    func environmentFolders(_ host: MachineRef) -> [String]
}

/// Status ▸ Environment (#5260, `specs/5254-secrets-accounts-environment/wireframes.md` section 4): the plain variables
/// sessions get when their agent starts, on one page where the person finds, adds and edits them. Yours
/// (`~/.agents/env`, written to every machine) and each repo's (`.agents/env`), grouped, with **Needs you** on top, a
/// search over names, values and descriptions, chips, and the selected variable's detail beside the list (pushed under
/// 720 pt), as the Secrets page (#5257). The window and the Remote draw it; the web page draws the same in
/// `Web/src/views/EnvironmentPage.tsx`.
struct EnvironmentPageView<Source: EnvironmentSource>: View {
    /// The control plane, which keeps Yours, or nil while it cannot be reached.
    let control: DaemonClient?
    let state: UserScopeState
    let source: Source
    /// The item shown, by its id: the window keeps it, so a link can open one.
    @Binding var selection: String?
    /// Opens the Secrets page at a secret, after Save as a Secret Instead….
    var openSecret: (String) -> Void = { _ in }

    @State private var machine: MachineRef?
    @State private var files: [EnvFile] = []
    @State private var readAt: Date?
    @State private var problem: String?
    @State private var search = ""
    @State private var chip: EnvironmentPage.Chip = .all
    @State private var adding = false
    @State private var asSession = false
    @State private var removing: EnvironmentPage.Item?
    @State private var editing: String?
    @State private var secretNamed: SecretName?
    @FocusState private var searchFocused: Bool

    /// The name a new secret starts with.
    struct SecretName: Identifiable {
        let name: String
        var id: String { name }
    }

    private var shown: EnvironmentMachine? {
        let machines = source.environmentMachines
        return machines.first { $0.id == machine } ?? machines.first
    }

    private var items: [EnvironmentPage.Item] { EnvironmentPage.items(files) }

    var body: some View {
        GeometryReader { geo in
            let wide = geo.size.width >= 720
            VStack(alignment: .leading, spacing: 0) {
                header(wide: wide)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 10)
                Divider()
                if wide {
                    HStack(spacing: 0) {
                        list
                            .frame(width: min(440, geo.size.width * 0.45))
                        Divider()
                        detailOrEmpty
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    }
                } else {
                    narrow
                }
            }
        }
        .task(id: shown?.id) { await reload() }
        .refreshable { await reload() }
        .sheet(isPresented: $adding) { addSheet }
        .sheet(isPresented: $asSession) { sessionSheet }
        .sheet(item: $secretNamed) { named in
            SecretSheet(name: nil, asks: true, description: nil, suggested: named.name) { name, value, ask, description in
                let problem = await state.setSecret(name, value: value, askBeforeUse: ask, description: description, on: control)
                if problem == nil { openSecret(name) }
                return problem
            }
            .paperSheet()
        }
        .confirmationDialog(removing.map { "Remove \($0.name)?" } ?? "",
                            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
                            titleVisibility: .visible, presenting: removing) { item in
            Button("Remove", role: .destructive) { Task { await remove(item) } }
            Button("Cancel", role: .cancel) {}
        } message: { item in
            Text(EnvironmentPage.writeNote(item.layer) + " " + EnvironmentPage.nextStart)
        }
    }

    // MARK: Header

    private func header(wide: Bool) -> some View {
        let counts = EnvironmentPage.counts(items, search: search)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                HStack(spacing: 6) {
                    Image(icon: "magnifyingglass").foregroundStyle(.secondary)
                    TextField(EnvironmentPage.searchPrompt, text: $search)
                        .textFieldStyle(.plain)
                        .focused($searchFocused)
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.never)
                        #endif
                        .accessibilityLabel("Search variables")
                        #if os(macOS)
                        .onExitCommand { search = "" }
                        #endif
                    if !search.isEmpty {
                        Button { search = "" } label: { Image(icon: "xmark.circle.fill") }
                            .buttonStyle(.borderless)
                            .accessibilityLabel(EnvironmentPage.clearSearch)
                    }
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 5)
                .frame(minWidth: 160)
                .paperWell(in: RoundedRectangle(cornerRadius: Paper.Radius.control))
                Button("") { searchFocused = true }
                    .keyboardShortcut("f", modifiers: .command)
                    .hidden()
                    .frame(width: 0, height: 0)
                    .accessibilityHidden(true)
                Button { adding = true } label: { Label(EnvironmentPage.add, icon: "plus") }
                    .fixedSize()
                    .disabled(shown == nil)
                if wide, let readAt { LastUpdateText(read: readAt) }
            }
            .frame(minHeight: 28)
            Text(EnvironmentPage.whereLine).appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(EnvironmentPage.Chip.allCases, id: \.self) { each in
                        chipButton(each, count: counts[each] ?? 0)
                    }
                    let machines = source.environmentMachines
                    if machines.count > 1, let shown {
                        Menu {
                            ForEach(machines) { each in Button(each.name) { machine = each.id; selection = nil } }
                        } label: {
                            Text("\(EnvironmentPage.machineLabel): \(shown.name)").appText(.fine)
                        }
                        .menuStyle(.borderlessButton)
                        .fixedSize()
                    }
                    Button(EnvironmentPage.asSession) { asSession = true }
                        .appText(.fine)
                        .buttonStyle(.borderless)
                        .disabled(shown == nil)
                    if !wide, let readAt { LastUpdateText(read: readAt) }
                }
            }
        }
    }

    private func chipButton(_ each: EnvironmentPage.Chip, count: Int) -> some View {
        let on = chip == each
        return Button { chip = on && each != .all ? .all : each } label: {
            HStack(spacing: 4) {
                if each == .needsYou { Image(icon: "exclamationmark.triangle") }
                Text("\(each.title) \(count)")
            }
            .appText(.fine)
            .foregroundStyle(on ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary))
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(on ? AnyShapeStyle(.selection) : AnyShapeStyle(.clear), in: Capsule())
            .paperRaised(in: Capsule())
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(each.title), \(count)")
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    // MARK: List

    private var list: some View {
        let groups = EnvironmentPage.groups(files, search: search, chip: chip)
        let yours = files.first { $0.layer == .yours }
        return List(selection: $selection) {
            if shown == nil {
                Text(EnvironmentPage.noMachine).appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let problem {
                Text(problem).appText(.supporting).tinted(.failure).fixedSize(horizontal: false, vertical: true)
            }
            if let yours, yours.changedByHand == true {
                VStack(alignment: .leading, spacing: 6) {
                    Label(EnvironmentPage.changedByHand, icon: "exclamationmark.triangle")
                        .appText(.supporting).tinted(.warning).fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Button(EnvironmentPage.keepForAll) { Task { await keepForAll(yours) } }.disabled(control == nil)
                        Button(EnvironmentPage.discard) { Task { await discard() } }
                    }
                }
                .selectionDisabled()
            }
            if groups.isEmpty, shown != nil, problem == nil {
                if items.isEmpty {
                    Text(EnvironmentPage.none).appText(.supporting).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .selectionDisabled()
                } else if !search.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(EnvironmentPage.noMatch(search)).appText(.supporting).foregroundStyle(.secondary)
                        Button(EnvironmentPage.clearSearch) { search = "" }
                    }
                    .selectionDisabled()
                }
            }
            ForEach(groups) { group in
                Section {
                    if let why = group.problem {
                        Text(why).appText(.fine).tinted(.failure).fixedSize(horizontal: false, vertical: true)
                            .selectionDisabled()
                    }
                    ForEach(group.items) { item in
                        row(item, inNeeds: group.id == "needs").tag(item.id)
                    }
                } header: {
                    HStack(spacing: 6) {
                        Text(group.title)
                        if !group.file.isEmpty { Text(group.file).foregroundStyle(.secondary) }
                        if group.uncommitted {
                            Label(EnvironmentPage.notCommitted, icon: "pencil").tinted(.warning)
                        }
                    }
                }
            }
        }
        .scrollContentBackground(.hidden)
    }

    private func row(_ item: EnvironmentPage.Item, inNeeds: Bool) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                if item.needs != nil { Image(icon: "exclamationmark.triangle").tinted(.warning) }
                Text(item.name).appText(.code).lineLimit(1)
                Spacer(minLength: 8)
                Text(item.value).appText(.code).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
            if inNeeds {
                Text(EnvironmentPage.layerShort(item)).appText(.fine).foregroundStyle(.secondary).lineLimit(1)
                Text(item.needs ?? "").appText(.fine).tinted(.warning).lineLimit(2)
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
        #if os(iOS)
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) { removing = item } label: { Text("Remove") }
        }
        #endif
    }

    // MARK: Narrow

    @ViewBuilder private var narrow: some View {
        #if os(iOS)
        list
            .navigationDestination(item: $selection) { id in
                if let item = items.first(where: { $0.id == id }) {
                    ScrollView { detail(item) }
                        .navigationTitle(item.name)
                        .navigationBarTitleDisplayMode(.inline)
                } else {
                    Text(EnvironmentPage.selectOne).foregroundStyle(.secondary)
                }
            }
        #else
        if let id = selection, let item = items.first(where: { $0.id == id }) {
            VStack(alignment: .leading, spacing: 0) {
                Button { selection = nil } label: { Label(EnvironmentPage.title, icon: "chevron.left") }
                    .buttonStyle(.borderless)
                    .padding(.horizontal, 16)
                    .padding(.top, 8)
                    .keyboardShortcut(.cancelAction)
                ScrollView { detail(item) }
            }
        } else {
            list
        }
        #endif
    }

    // MARK: Detail

    @ViewBuilder private var detailOrEmpty: some View {
        if let id = selection, let item = items.first(where: { $0.id == id }) {
            ScrollView { detail(item) }
        } else {
            VStack(spacing: 6) {
                Text(EnvironmentPage.selectOne).appText(.supporting).foregroundStyle(.secondary)
                let counts = EnvironmentPage.counts(items, search: "")
                Text("\(counts[.yours] ?? 0) yours · \(counts[.repos] ?? 0) in repos")
                    .appText(.fine).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func detail(_ item: EnvironmentPage.Item) -> some View {
        EnvironmentDetail(item: item, also: EnvironmentPage.alsoSet(item, in: items), canWrite: canWrite(item.layer),
                          isEditing: editing == item.id,
                          edit: { editing = $0 ? item.id : nil },
                          remove: { removing = item },
                          saveAsSecret: { secretNamed = SecretName(name: item.name) },
                          save: { value, description in
                              await write(layer: item.layer, folder: item.folder, name: item.name, value: value,
                                          description: description)
                          })
            .id(item.id)
            .padding(16)
    }

    // MARK: Sheets

    private var repoFolders: [String] {
        guard let host = shown?.id else { return [] }
        let known = files.compactMap(\.folder)
        return Array(Set(known + source.environmentFolders(host))).sorted {
            EnvironmentPage.repoName($0).localizedStandardCompare(EnvironmentPage.repoName($1)) == .orderedAscending
        }
    }

    private var addSheet: some View {
        EnvironmentAddSheet(folders: repoFolders, canAddYours: control != nil,
                            saveAsSecret: { name in
                                adding = false
                                secretNamed = SecretName(name: name)
                            },
                            save: { layer, folder, name, value, description in
                                let problem = await write(layer: layer, folder: folder, name: name, value: value,
                                                          description: description)
                                if problem == nil {
                                    search = ""
                                    chip = .all
                                    selection = EnvironmentPage.id(layer: layer, folder: folder, name: name)
                                }
                                return problem
                            })
            .paperSheet()
    }

    @ViewBuilder private var sessionSheet: some View {
        if let shown {
            EnvironmentSessionSheet(machine: shown, folders: repoFolders, sessions: source.environmentSessions(shown.id),
                                    client: source.environmentClient(shown.id))
                .paperSheet()
        }
    }

    // MARK: Reads and writes

    private func canWrite(_ layer: EnvLayer) -> Bool {
        layer == .yours ? control != nil : shown.flatMap { source.environmentClient($0.id) } != nil
    }

    private func reload() async {
        guard let host = shown?.id else {
            files = []
            return
        }
        guard let client = source.environmentClient(host) else {
            problem = EnvironmentPage.cannotReach(shown?.name ?? "")
            return
        }
        do {
            files = try await client.call(DaemonAPI.Method.envList, EnvListRequest(), returning: EnvList.self).files
            readAt = Date()
            problem = nil
        } catch {
            problem = UserScopeWords.refusal(error)
        }
        if control != nil, state.settings == nil { await state.load(control) }
    }

    /// The text of Yours to change: the person's settings' part, else this machine's file as it reads it.
    private var yoursText: String {
        if let part = try? state.settings?.environment?.decode(UserEnvironment.self) { return part.text }
        return EnvFileText.text(of: files.first { $0.layer == .yours }?.variables ?? [])
    }

    /// Adds, changes (a value) or removes (nil) one variable: Yours through the person's settings, a repo's through
    /// its machine's host. Nil once done, else why not.
    private func write(layer: EnvLayer, folder: String?, name: String, value: String?, description: String?) async -> String? {
        if let value, let refused = EnvironmentPage.refusal(name: name, value: value) { return refused }
        if layer == .yours {
            let text = EnvFileText.edit(yoursText, name: name, value: value, description: description)
            if let problem = await state.set(.environment, to: try! JSONValue.encoding(UserEnvironment(text: text)), on: control) {
                return problem
            }
        } else {
            guard let folder, let host = shown?.id, let client = source.environmentClient(host) else {
                return EnvironmentPage.cannotReach(shown?.name ?? "")
            }
            do {
                _ = try await client.call(DaemonAPI.Method.envWrite,
                                          EnvWriteRequest(folder: folder, name: name, value: value, description: description))
            } catch {
                return UserScopeWords.refusal(error)
            }
        }
        await reload()
        return nil
    }

    private func remove(_ item: EnvironmentPage.Item) async {
        problem = await write(layer: item.layer, folder: item.folder, name: item.name, value: nil, description: nil)
        if problem == nil, selection == item.id { selection = nil }
    }

    /// Keep for All Machines: this machine's edited file becomes the person's settings, so every machine gets it.
    private func keepForAll(_ file: EnvFile) async {
        let text = EnvFileText.text(of: file.variables)
        problem = await state.set(.environment, to: try! JSONValue.encoding(UserEnvironment(text: text)), on: control)
        await reload()
    }

    /// Discard: this machine's file is written again from the person's settings.
    private func discard() async {
        guard let host = shown?.id, let client = source.environmentClient(host) else { return }
        do {
            _ = try await client.call(DaemonAPI.Method.envDiscard, DaemonAPI.Empty(), returning: DaemonAPI.Empty.self)
        } catch {
            problem = UserScopeWords.refusal(error)
        }
        await reload()
    }
}

/// The detail of one variable (wireframes §4a): the description, the value with Copy, the layer and its file, Also set,
/// why it needs you, "Sessions get it the next time their agent starts.", and Edit (in place) and Remove….
private struct EnvironmentDetail: View {
    let item: EnvironmentPage.Item
    let also: [String]
    let canWrite: Bool
    let isEditing: Bool
    let edit: (Bool) -> Void
    let remove: () -> Void
    let saveAsSecret: () -> Void
    let save: (String, String?) async -> String?

    @State private var value = ""
    @State private var about = ""
    @State private var problem: String?
    @State private var saving = false
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Text(item.name).appText(.title).textSelection(.enabled)
                Spacer()
                Button(copied ? "Copied" : EnvironmentPage.copy) { copy() }
                    .accessibilityLabel("Copy the value of \(item.name)")
            }
            if isEditing {
                editor
            } else {
                Text(item.description ?? "No description.").appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Divider()
                grid
                Text(EnvironmentPage.nextStart).appText(.fine).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let problem {
                Text(problem).appText(.fine).tinted(.failure).fixedSize(horizontal: false, vertical: true)
                if EnvironmentPage.refusedAsSecret(problem) {
                    Button(EnvironmentPage.saveAsSecret, action: saveAsSecret)
                }
            }
            if !isEditing {
                HStack(spacing: 10) {
                    Button(EnvironmentPage.edit) { edit(true) }.disabled(!canWrite)
                    Spacer()
                    Button(EnvironmentPage.remove, role: .destructive, action: remove).disabled(!canWrite)
                }
                .padding(.top, 8)
            }
        }
        .frame(maxWidth: 560, alignment: .leading)
        .onAppear { reset() }
        .onChange(of: isEditing) { _, _ in reset() }
    }

    private func reset() {
        value = item.value
        about = item.description ?? ""
        problem = nil
    }

    private func copy() {
        #if os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(item.value, forType: .string)
        #else
        UIPasteboard.general.string = item.value
        #endif
        copied = true
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(2))
            copied = false
        }
    }

    private var grid: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 8) {
            GridRow {
                label("Value")
                Text(item.value.isEmpty ? "(empty)" : item.value).appText(.code).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
            GridRow {
                label("Layer")
                VStack(alignment: .leading, spacing: 2) {
                    Text(EnvironmentPage.layerLine(item))
                    Text(item.path).appText(.fine).foregroundStyle(.secondary).textSelection(.enabled)
                    if item.layer == .repo && item.uncommitted {
                        Label(EnvironmentPage.notCommitted, icon: "pencil").appText(.fine).tinted(.warning)
                    }
                }
            }
            if !also.isEmpty {
                GridRow {
                    label("Also set")
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(also, id: \.self) { Text($0) }
                    }
                }
            }
            if let warning = item.needs {
                GridRow {
                    label("Needs you")
                    Label(warning, icon: "exclamationmark.triangle").tinted(.warning)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .appText(.supporting)
    }

    private func label(_ text: String) -> some View {
        Text(text).foregroundStyle(.secondary).gridColumnAlignment(.leading)
    }

    private var editor: some View {
        VStack(alignment: .leading, spacing: 10) {
            TextField("Value", text: $value, prompt: Text("Value"))
                .autocorrectionDisabled()
                #if os(iOS)
                .textInputAutocapitalization(.never)
                #endif
            TextField("Description", text: $about, prompt: Text("What it is for"))
            Text(EnvironmentPage.writeNote(item.layer)).appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Cancel", role: .cancel) { edit(false) }
                Spacer()
                Button("Save") {
                    Task {
                        saving = true
                        problem = await save(value, about.trimmingCharacters(in: .whitespaces))
                        saving = false
                        if problem == nil { edit(false) }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving)
            }
        }
    }
}

/// Add Variable (wireframes §4b): Name, Value, Description and Where (Yours, or a repo's file). A value that looks like
/// a secret is refused, with Save as a Secret Instead….
private struct EnvironmentAddSheet: View {
    let folders: [String]
    let canAddYours: Bool
    let saveAsSecret: (String) -> Void
    let save: (EnvLayer, String?, String, String, String?) async -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var value = ""
    @State private var about = ""
    @State private var layer: EnvLayer = .yours
    @State private var folder: String?
    @State private var problem: String?
    @State private var saving = false

    private var trimmed: String { name.trimmingCharacters(in: .whitespaces) }
    private var refusal: String? { problem ?? EnvironmentPage.refusal(name: trimmed, value: value) }

    var body: some View {
        Form {
            Section {
                TextField("Name", text: $name, prompt: Text("AWS_REGION"))
                    .autocorrectionDisabled()
                    #if os(iOS)
                    .textInputAutocapitalization(.characters)
                    #endif
                TextField("Value", text: $value, prompt: Text("eu-west-2"))
                    .autocorrectionDisabled()
                    #if os(iOS)
                    .textInputAutocapitalization(.never)
                    #endif
                TextField("Description", text: $about, prompt: Text("What it is for"))
                Picker("Where", selection: $layer) {
                    Text("Yours  ~/.agents/env, every machine").tag(EnvLayer.yours)
                    if !folders.isEmpty { Text("A repo  .agents/env").tag(EnvLayer.repo) }
                }
                .pickerStyle(.inline)
                if layer == .repo {
                    Picker("Repo", selection: Binding(get: { folder ?? folders.first }, set: { folder = $0 })) {
                        ForEach(folders, id: \.self) { Text(EnvironmentPage.repoName($0)).tag(Optional($0)) }
                    }
                    Text(EnvironmentPage.repoPublic).appText(.fine).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            } header: {
                Text(EnvironmentPage.add)
            } footer: {
                VStack(alignment: .leading, spacing: 6) {
                    Text(EnvironmentPage.nextStart)
                    if let refusal {
                        Label(refusal, icon: "exclamationmark.triangle").tinted(.warning)
                            .fixedSize(horizontal: false, vertical: true)
                        if EnvironmentPage.refusedAsSecret(refusal) {
                            Button(EnvironmentPage.saveAsSecret) { saveAsSecret(trimmed) }
                        }
                    }
                }
            }
        }
        .paperForm()
        .onChange(of: name) { _, _ in problem = nil }
        .onChange(of: value) { _, _ in problem = nil }
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            ToolbarItem(placement: .confirmationAction) {
                Button("Add") {
                    Task {
                        saving = true
                        let at = layer == .repo ? (folder ?? folders.first) : nil
                        problem = await save(layer, at, trimmed, value, about.trimmingCharacters(in: .whitespaces))
                        saving = false
                        if problem == nil { dismiss() }
                    }
                }
                .disabled(saving || trimmed.isEmpty || refusal != nil || (layer == .yours && !canAddYours))
            }
        }
        #if os(macOS)
        .frame(minWidth: 460, minHeight: 360)
        #endif
    }
}

/// As a session sees it (wireframes §4c): a folder on the machine, or a running session there, and the variables a
/// session gets, each with the layer that wins.
private struct EnvironmentSessionSheet: View {
    let machine: EnvironmentMachine
    let folders: [String]
    let sessions: [EnvironmentSession]
    let client: DaemonClient?

    enum Pick: Hashable {
        case folder(String)
        case session(UUID)
    }

    @Environment(\.dismiss) private var dismiss
    @State private var pick: Pick?
    @State private var resolved: EnvResolved?
    @State private var problem: String?

    var body: some View {
        Form {
            Section {
                Picker("Folder or session", selection: $pick) {
                    Text("Choose…").tag(Pick?.none)
                    ForEach(folders, id: \.self) { Text(EnvironmentPage.repoName($0)).tag(Pick?.some(.folder($0))) }
                    ForEach(sessions) { Text("Session: \($0.title)").tag(Pick?.some(.session($0.id))) }
                }
            } header: {
                Text(resolved.map { EnvironmentPage.asSessionTitle($0.folder, machine: machine.name) } ?? EnvironmentPage.asSession)
            }
            if let problem {
                Text(problem).appText(.supporting).tinted(.failure)
            }
            if let resolved {
                Section {
                    if resolved.variables.isEmpty {
                        Text("Neither file sets a variable here.").foregroundStyle(.secondary)
                    }
                    ForEach(resolved.variables) { variable in
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text(variable.name).appText(.code)
                            Text(variable.value ?? "—").appText(.code).foregroundStyle(.secondary).lineLimit(1)
                            Spacer()
                            Text(EnvironmentPage.resolvedSource(variable, repo: EnvironmentPage.repoName(resolved.folder)))
                                .appText(.fine)
                                .foregroundStyle(variable.value == nil ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary))
                                .tinted(variable.value == nil ? .warning : .none)
                        }
                    }
                }
            }
        }
        .paperForm()
        .task(id: pick) { await resolve() }
        .toolbar {
            ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
        }
        #if os(macOS)
        .frame(minWidth: 520, minHeight: 360)
        #endif
    }

    private func resolve() async {
        guard let pick else {
            resolved = nil
            return
        }
        guard let client else {
            problem = EnvironmentPage.cannotReach(machine.name)
            return
        }
        let request = switch pick {
        case .folder(let folder): EnvResolveRequest(folder: folder)
        case .session(let id): EnvResolveRequest(agentID: id)
        }
        do {
            resolved = try await client.call(DaemonAPI.Method.envResolve, request, returning: EnvResolved.self)
            problem = nil
        } catch {
            problem = UserScopeWords.refusal(error)
        }
    }
}
