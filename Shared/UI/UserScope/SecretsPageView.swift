import AgentsKitCore
import SwiftUI

/// Status ▸ Secrets (#5257, `specs/5254-secrets-accounts-environment/wireframes.md` sections 2, 2a and 6): every
/// secret of the person's on one page, where they find, see, add, edit and remove it. A search (never a value), chips
/// with counts, a Project menu and a sort over a list with the **Needs you** group on top, and the selected secret's
/// detail beside it. Under 720 pt wide the list is alone, and a press opens the detail with a back arrow. It replaces
/// the read-only Status list (#4161) and Settings ▸ You ▸ Secrets. The window and the Remote draw it; the web page draws
/// the same in `Web/src/views/SecretsPage.tsx`.
struct SecretsPageView<Source: LocalSecretsSource>: View {
    /// The control plane, or nil while it cannot be reached.
    let control: DaemonClient?
    let state: UserScopeState
    /// Where the secrets for one session and those on one machine come from: the host this client reaches.
    let source: Source
    /// Each of the person's machines' names, by id.
    let machineNames: [String: String]
    /// The host `source` reaches, as the rows say it.
    var hostName = "this Mac"
    /// Reveal and Copy (#5115): the window's; nil on the Remote (tier Mac + web), which draws neither.
    var show: SecretShowState?
    /// The item shown, by its id: the window keeps it, so a link can open one.
    @Binding var selection: String?
    /// A session's title by its id, for Recent use.
    var agentTitle: (UUID) -> String? = { _ in nil }
    /// Opens an account of Used by on its page.
    var openAccount: (DaemonAPI.OutsideAccount) -> Void = { _ in }

    @State private var search = ""
    @State private var chip: SecretsPage.Chip = .all
    @State private var project: String?
    @State private var sort: SecretsPage.Sort = .lastUsed
    @State private var sessions: SessionSecrets?
    @State private var local: LocalSecrets?
    @State private var problem: String?
    @State private var adding: Adding?
    @State private var replacing: DaemonAPI.UserSecretRow?
    @State private var removing: SecretsPage.Item?
    @State private var editing: String?
    @FocusState private var searchFocused: Bool

    /// Which Add sheet is open.
    enum Adding: String, Identifiable {
        case secret, card, session
        var id: String { rawValue }
    }

    private var items: [SecretsPage.Item] {
        SecretsPage.items(yours: state.secrets?.secrets ?? [], sessions: sessions?.secrets ?? [], local: local?.secrets ?? [],
                          accounts: state.accounts, machineNames: machineNames, hostName: hostName,
                          noCopy: show?.noCopy ?? [], clientName: hostName)
    }

    var body: some View {
        let items = items
        GeometryReader { geo in
            let wide = geo.size.width >= 720
            VStack(alignment: .leading, spacing: 0) {
                header(items, wide: wide)
                    .padding(.horizontal, 16)
                    .padding(.vertical, 10)
                Divider()
                if wide {
                    HStack(spacing: 0) {
                        list(items)
                            .frame(width: min(440, geo.size.width * 0.45))
                        Divider()
                        detailOrEmpty(items)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    }
                } else {
                    narrow(items)
                }
            }
        }
        .task(id: control != nil) { await reload() }
        .refreshable { await reload() }
        .onChange(of: state.secrets) { _, names in show?.secretsChanged(names?.secrets ?? []) }
        .onDisappear { show?.hide() }
        #if os(macOS)
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didResignActiveNotification)) { _ in show?.hide() }
        #endif
        .sheet(item: $adding) { kind in addSheet(kind) }
        .sheet(item: $replacing) { row in replaceSheet(row) }
        .confirmationDialog(removing.map { "Remove \($0.name)?" } ?? "",
                            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
                            titleVisibility: .visible, presenting: removing) { item in
            Button("Remove", role: .destructive) { Task { await remove(item) } }
            Button("Cancel", role: .cancel) {}
        } message: { item in
            switch item.place {
            case .yours: Text("It is taken out of the sealed store on every machine of yours.")
            case .session: Text(SessionSecretWords.removeMessage)
            case .machine: Text(LocalSecretWords.removeMessage)
            }
        }
    }

    // MARK: Header

    private func header(_ items: [SecretsPage.Item], wide: Bool) -> some View {
        let counts = SecretsPage.counts(items, search: search, project: project)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                HStack(spacing: 6) {
                    Image(icon: "magnifyingglass").foregroundStyle(.secondary)
                    TextField(SecretsPage.searchPrompt, text: $search)
                        .textFieldStyle(.plain)
                        .focused($searchFocused)
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.never)
                        #endif
                        .accessibilityLabel("Search secrets")
                        #if os(macOS)
                        .onExitCommand { search = "" }
                        #endif
                    if !search.isEmpty {
                        Button { search = "" } label: { Image(icon: "xmark.circle.fill") }
                            .buttonStyle(.borderless)
                            .accessibilityLabel(SecretsPage.clearSearch)
                    }
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 5)
                .frame(minWidth: 160)
                .paperWell(in: RoundedRectangle(cornerRadius: Paper.Radius.control))
                // ⌘F puts the cursor in the search (wireframes §2).
                Button("") { searchFocused = true }
                    .keyboardShortcut("f", modifiers: .command)
                    .hidden()
                    .frame(width: 0, height: 0)
                    .accessibilityHidden(true)
                addMenu
                // A narrow page gives the search the room: the line goes under it, after the chips.
                if wide, let read = state.readAt {
                    LastUpdateText(read: read)
                }
            }
            .frame(minHeight: 28)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(SecretsPage.Chip.allCases, id: \.self) { each in
                        chipButton(each, count: counts[each] ?? 0)
                    }
                    let projects = SecretsPage.projects(items)
                    if !projects.isEmpty || project != nil {
                        Menu {
                            Button(SecretsPage.projectAny) { project = nil }
                            Divider()
                            ForEach(projects, id: \.self) { name in Button(name) { project = name } }
                        } label: {
                            Text(project ?? SecretsPage.projectAny).appText(.fine)
                        }
                        .menuStyle(.borderlessButton)
                        .fixedSize()
                    }
                    Menu {
                        Picker(SecretsPage.sortLabel, selection: $sort) {
                            ForEach(SecretsPage.Sort.allCases, id: \.self) { Text($0.title).tag($0) }
                        }
                        .pickerStyle(.inline)
                    } label: {
                        Label(sort.title, icon: "arrow.up.arrow.down").appText(.fine)
                    }
                    .menuStyle(.borderlessButton)
                    .fixedSize()
                    .accessibilityLabel("\(SecretsPage.sortLabel): \(sort.title)")
                    if !wide, let read = state.readAt {
                        LastUpdateText(read: read)
                    }
                }
            }
        }
    }

    private func chipButton(_ each: SecretsPage.Chip, count: Int) -> some View {
        let on = chip == each
        return PageChip(title: each.title, count: count, warns: each == .needsYou, on: on) { chip = on && each != .all ? .all : each }
    }

    private var addMenu: some View {
        Menu {
            Button(SecretsPage.addSecret) { adding = .secret }
            Button(SecretsPage.addCard) { adding = .card }
            Button(SecretsPage.addForSession) { adding = .session }
        } label: {
            Label(SecretsPage.add, icon: "plus")
        }
        .fixedSize()
        .disabled(control == nil)
        .accessibilityLabel("Add")
    }

    // MARK: List

    private func list(_ items: [SecretsPage.Item]) -> some View {
        let groups = SecretsPage.groups(items, search: search, chip: chip, project: project, sort: sort)
        return List(selection: $selection) {
            if let problem = problem ?? state.problem {
                Text(problem).appText(.supporting).tinted(.failure)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let left = state.left {
                VStack(alignment: .leading, spacing: 4) {
                    Text(UserScopeWords.secretsLeft(left)).appText(.supporting).tinted(.warning)
                        .fixedSize(horizontal: false, vertical: true)
                    Button("OK") { state.left = nil }
                }
            }
            if let keyProblem = local?.keyProblem {
                // The machine's key does not read (#6632): said here once, not in the log at each delivery.
                Label(keyProblem, icon: "exclamationmark.triangle")
                    .appText(.supporting).tinted(.warning)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let only = SecretInfoWords.onlyMachine(state.secrets?.secrets ?? []) {
                Label(SecretInfoWords.onlyMachineLine(only, names: machineNames), icon: "exclamationmark.triangle")
                    .appText(.supporting).tinted(.warning)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if groups.isEmpty {
                emptyList(items)
            }
            ForEach(groups) { group in
                Section {
                    ForEach(group.items) { item in
                        row(item).tag(item.id)
                    }
                } header: {
                    Text("\(group.title) · \(group.items.count)")
                }
            }
            Text(items.count == 1 ? "1 secret" : "\(items.count) secrets")
                .appText(.fine).foregroundStyle(.secondary)
                .selectionDisabled()
        }
        .scrollContentBackground(.hidden)
    }

    @ViewBuilder private func emptyList(_ items: [SecretsPage.Item]) -> some View {
        if items.isEmpty {
            Text(SecretsPage.none).appText(.supporting).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .selectionDisabled()
        } else if !search.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Text(SecretsPage.noMatch(search)).appText(.supporting).foregroundStyle(.secondary)
                Button(SecretsPage.clearSearch) { search = "" }
            }
            .selectionDisabled()
        }
    }

    private func row(_ item: SecretsPage.Item) -> some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    if item.needs != nil {
                        Image(icon: "exclamationmark.triangle").tinted(.warning)
                    }
                    Text(item.name).appText(.code).lineLimit(1)
                    Spacer(minLength: 8)
                    Text(SecretsPage.rowHint(item)).appText(.code).foregroundStyle(.secondary)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(item.isCard ? "Card" : item.hint.map { "Ends in \($0)" } ?? "Value hidden")
                    if let age = SecretsPage.shortAge(item, now: context.date) {
                        Text(age).appText(.fine).foregroundStyle(.secondary).monospacedDigit()
                    }
                }
                Text(SecretsPage.subtitle(item)).appText(.fine)
                    .foregroundStyle(item.needs == nil ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
                    .tinted(item.needs == nil ? .none : .warning)
                    .lineLimit(1)
            }
            .padding(.vertical, 3)
        }
        .contentShape(Rectangle())
        #if os(iOS)
        .swipeActions(edge: .trailing) {
            if removable(item) {
                Button(role: .destructive) { removing = item } label: { Text("Remove") }
            }
        }
        #endif
    }

    // MARK: Narrow

    @ViewBuilder private func narrow(_ items: [SecretsPage.Item]) -> some View {
        #if os(iOS)
        list(items)
            .navigationDestination(item: $selection) { id in
                if let item = self.items.first(where: { $0.id == id }) {
                    ScrollView { detail(item) }
                        .navigationTitle(item.name)
                        .navigationBarTitleDisplayMode(.inline)
                } else {
                    Text(SecretsPage.selectOne).foregroundStyle(.secondary)
                }
            }
        #else
        if let id = selection, let item = items.first(where: { $0.id == id }) {
            VStack(alignment: .leading, spacing: 0) {
                Button { selection = nil } label: { Label(SecretsPage.title, icon: "chevron.left") }
                    .buttonStyle(.borderless)
                    .padding(.horizontal, 16)
                    .padding(.top, 8)
                    .keyboardShortcut(.cancelAction)
                ScrollView { detail(item) }
            }
        } else {
            list(items)
        }
        #endif
    }

    // MARK: Detail

    @ViewBuilder private func detailOrEmpty(_ items: [SecretsPage.Item]) -> some View {
        if let id = selection, let item = items.first(where: { $0.id == id }) {
            ScrollView { detail(item) }
        } else {
            VStack(spacing: 6) {
                Text(SecretsPage.selectOne).appText(.supporting).foregroundStyle(.secondary)
                let counts = SecretsPage.counts(items, search: "")
                Text("\(counts[.yours] ?? 0) yours · \(counts[.session] ?? 0) for a session · \(counts[.machine] ?? 0) on a machine")
                    .appText(.fine).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func detail(_ item: SecretsPage.Item) -> some View {
        let row = item.place == .yours ? state.secrets?.secrets.first { $0.name == item.name } : nil
        return SecretDetail(item: item, row: row, control: control, show: show,
                            accounts: state.accounts.filter { $0.secret == item.name || $0.card == item.name },
                            asksOffered: item.place == .yours || source.sessionHostAsksBeforeUse,
                            removable: removable(item), isEditing: editing == item.id, agentTitle: agentTitle, openAccount: openAccount,
                            edit: { editing = $0 ? item.id : nil },
                            replace: { if let row { replacing = row } },
                            remove: { removing = item },
                            setAsk: { on in await setAsk(item, on) },
                            save: { value, ask, description in
                                guard let row else { return "This secret is gone." }
                                return await state.setSecret(row.name, value: value, kind: row.kind, replacing: row,
                                                             askBeforeUse: ask, description: description, on: control)
                            })
            .id(item.id)
            .padding(16)
    }

    // MARK: Sheets

    @ViewBuilder private func addSheet(_ kind: Adding) -> some View {
        switch kind {
        case .secret:
            SecretSheet(name: nil, asks: true, description: nil) { name, value, ask, description in
                await added("yours/\(name)", await state.setSecret(name, value: value, askBeforeUse: ask, description: description, on: control))
            }
            .paperSheet()
        case .card:
            CardSheet(name: nil, asks: true, description: nil) { name, value, ask, description in
                await added("yours/\(name)", await state.setSecret(name, value: value, kind: CardSecret.kind, askBeforeUse: ask,
                                                                   description: description, on: control))
            }
            .paperSheet()
        case .session:
            SessionSecretSheet(sessions: source.sessionsForSecrets, offersAsk: source.sessionHostAsksBeforeUse) { request in
                switch await source.setSessionSecret(request) {
                case .success(let after):
                    sessions = after
                    return await added("session/\(request.agentID.uuidString)/\(request.name)", nil)
                case .failure(let error):
                    return UserScopeWords.refusal(error)
                }
            }
            .paperSheet()
        }
    }

    @ViewBuilder private func replaceSheet(_ row: DaemonAPI.UserSecretRow) -> some View {
        if row.isCard {
            CardSheet(name: row.name, asks: row.asksBeforeUse, description: row.description) { name, value, ask, description in
                await state.setSecret(name, value: value, kind: CardSecret.kind, replacing: row, askBeforeUse: ask,
                                      description: description, on: control)
            }
            .paperSheet()
        } else {
            SecretSheet(name: row.name, asks: row.asksBeforeUse, description: row.description) { name, value, ask, description in
                await state.setSecret(name, value: value, replacing: row, askBeforeUse: ask, description: description, on: control)
            }
            .paperSheet()
        }
    }

    /// After an Add: the new item is selected, so the detail shows it (wireframes §2a).
    private func added(_ id: String, _ problem: String?) async -> String? {
        if problem == nil {
            search = ""
            chip = .all
            project = nil
            selection = id
        }
        return problem
    }

    // MARK: Changes

    private func reload() async {
        await state.load(control)
        sessions = await source.sessionSecrets()
        do {
            local = try await source.localSecrets()
        } catch {
            // An empty list would hide what agents here still use (#6335): say why instead.
            local = nil
            problem = LocalSecretWords.didNotLoad(UserScopeWords.refusal(error))
        }
    }

    /// Whether Remove is offered: one of Yours and a session's always; one on a machine when its host takes it out (#6335).
    private func removable(_ item: SecretsPage.Item) -> Bool {
        item.place != .machine || source.localSecretsRemovable
    }

    /// Ask before use. A secret for one session or on one machine: its host seals it again. One of Yours is sealed with
    /// its value, so the detail opens Edit with the switch moved, for the value (`SecretDetail`).
    private func setAsk(_ item: SecretsPage.Item, _ on: Bool) async -> String? {
        switch item.place {
        case .yours:
            return nil
        case .session:
            guard let secret = sessions?.secrets.first(where: { $0.agentID == item.agentID && $0.name == item.name }) else { return nil }
            switch await source.setSessionSecretAsksBeforeUse(secret, on) {
            case .success(let after):
                if let after { sessions = after }
                return nil
            case .failure(let error):
                return UserScopeWords.refusal(error)
            }
        case .machine:
            guard let secret = local?.secrets.first(where: { $0.name == item.name }) else { return nil }
            switch await source.setLocalSecretAsksBeforeUse(secret, on) {
            case .success(let after):
                if let after { local = after }
                return nil
            case .failure(let error):
                return UserScopeWords.refusal(error)
            }
        }
    }

    private func remove(_ item: SecretsPage.Item) async {
        switch item.place {
        case .yours:
            problem = await state.removeSecret(item.name, on: control)
        case .session:
            guard let secret = sessions?.secrets.first(where: { $0.agentID == item.agentID && $0.name == item.name }) else { return }
            if let after = await source.removeSessionSecret(secret) { sessions = after }
        case .machine:
            guard let secret = local?.secrets.first(where: { $0.name == item.name }) else { return }
            switch await source.removeLocalSecret(secret) {
            case .success(let after): local = after
            case .failure(let error):
                problem = UserScopeWords.refusal(error)
                return
            }
        }
        if selection == item.id { selection = nil }
    }
}

/// The detail of one secret (wireframes §2): the value line, the kind, where it is kept, Ask before use, the projects,
/// Used by, the age line, Recent use, and Edit, Replace Value… and Remove…. Edit turns the description and Ask before use
/// into fields in place, with the value: both are sealed with it (#4016, #4167).
private struct SecretDetail: View {
    let item: SecretsPage.Item
    let row: DaemonAPI.UserSecretRow?
    let control: DaemonClient?
    let show: SecretShowState?
    let accounts: [DaemonAPI.OutsideAccount]
    let asksOffered: Bool
    /// Remove is offered (#6335).
    let removable: Bool
    let isEditing: Bool
    let agentTitle: (UUID) -> String?
    let openAccount: (DaemonAPI.OutsideAccount) -> Void
    let edit: (Bool) -> Void
    let replace: () -> Void
    let remove: () -> Void
    let setAsk: (Bool) async -> String?
    let save: (String, Bool, String?) async -> String?

    @State private var about = ""
    @State private var ask = false
    @State private var value = ""
    @State private var problem: String?
    @State private var saving = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Text(item.name).appText(.title).textSelection(.enabled)
                Spacer()
                if let show, let row {
                    SecretShowButtons(row: row, show: show, control: control)
                }
            }
            if isEditing {
                editor
            } else {
                Text(UserScopeWords.about(item.description)).appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                // An agent saved it on a machine (#5308): "Saved by an agent on devbox, 2 h ago".
                if let saved = SecretOfferWords.savedBy(row?.savedBy) {
                    Text(saved).appText(.fine).foregroundStyle(.secondary)
                }
                Divider()
                facts
            }
            if let problem {
                Text(problem).appText(.fine).tinted(.failure).fixedSize(horizontal: false, vertical: true)
            }
            if !isEditing { actions }
        }
        .frame(maxWidth: 560, alignment: .leading)
        .onAppear { reset() }
        .onChange(of: isEditing) { _, _ in reset() }
    }

    private func reset() {
        about = item.description ?? ""
        ask = item.asksBeforeUse
        value = ""
        problem = nil
    }

    private var facts: some View {
        VStack(alignment: .leading, spacing: 12) {
            grid
            SecretInfoLine(parts: SecretsPage.ageLine(item))
            VStack(alignment: .leading, spacing: 4) {
                Text(SecretsPage.recentUse.uppercased()).appText(.fine).foregroundStyle(.secondary)
                TimelineView(.periodic(from: .now, by: 60)) { context in
                    Text(SecretsPage.recentLine(item, now: context.date, agentTitle: agentTitle) ?? SecretsPage.noUse)
                        .appText(.supporting)
                }
            }
        }
    }

    private var grid: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 8) {
            GridRow {
                label("Value")
                VStack(alignment: .leading, spacing: 4) {
                    if let show, let row, let shown = show.shown, shown.name == row.name {
                        SecretShownValue(row: row, shown: shown, show: show, control: control)
                    } else {
                        Text(item.isCard ? "Card" : SecretInfoWords.hint(item.hint)).appText(.code)
                            .accessibilityElement(children: .ignore)
                            .accessibilityLabel(item.hint.map { "Ends in \($0)" } ?? "Value hidden")
                    }
                    if let show, let row { SecretShowLines(row: row, show: show) }
                }
            }
            GridRow { label("Kind"); Text(SecretsPage.kind(item)) }
            GridRow { label("Where"); Text(SecretsPage.whereLine(item)).fixedSize(horizontal: false, vertical: true) }
            if asksOffered {
                // A switch has no text baseline: the row lines up at the top, or the label rides up into Where's.
                GridRow(alignment: .top) {
                    label(AskBeforeUseWords.toggle).padding(.top, 4)
                    VStack(alignment: .leading, spacing: 2) {
                        Toggle(AskBeforeUseWords.toggle, isOn: Binding(get: { item.asksBeforeUse }, set: { on in
                            if item.place == .yours {
                                // Sealed with the value: Edit opens with the switch moved.
                                edit(true)
                                Task { @MainActor in ask = on }
                            } else {
                                Task { problem = await setAsk(on) }
                            }
                        }))
                        .labelsHidden()
                        .toggleStyle(.switch)
                        .disabled(control == nil && item.place == .yours)
                        Text(AskBeforeUseWords.hint(item.asksBeforeUse)).appText(.fine).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            if !item.projects.isEmpty {
                GridRow { label("Projects"); Text(item.projects.joined(separator: ", ")) }
            }
            if !accounts.isEmpty {
                GridRow {
                    label(SecretsPage.usedBy)
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(accounts) { account in
                            Button {
                                openAccount(account)
                            } label: {
                                Text(account.address.map { "\(account.service) (\($0))" } ?? account.service)
                            }
                            .buttonStyle(.borderless)
                        }
                    }
                }
            }
            PageNeedsYouRow(warning: item.needs)
        }
        .appText(.supporting)
    }

    private func label(_ text: String) -> some View { PageGridLabel(text) }

    @ViewBuilder private var actions: some View {
        HStack(spacing: 10) {
            if item.place == .yours {
                Button(SecretsPage.replaceValue, action: replace).disabled(control == nil)
                Button(SecretsPage.edit) { edit(true) }.disabled(control == nil)
            }
            Spacer()
            if removable {
                Button(SecretsPage.remove, role: .destructive, action: remove)
                    .disabled(item.place == .yours && control == nil)
            }
        }
        .padding(.top, 8)
    }

    /// Edit, in place: the description, Ask before use and the value they are sealed with.
    private var editor: some View {
        VStack(alignment: .leading, spacing: 10) {
            SecretDescriptionField(text: $about)
            AskBeforeUseToggle(isOn: $ask)
            SecureField("Value", text: $value, prompt: Text("Value"))
                .accessibilityLabel("Value")
            Text(SecretsPage.editNote).appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Cancel", role: .cancel) { edit(false) }
                Spacer()
                Button("Save") {
                    Task {
                        saving = true
                        problem = await save(value, ask, UserScopeWords.description(about))
                        saving = false
                        if problem == nil {
                            value = ""
                            edit(false)
                        }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving || value.isEmpty)
            }
        }
        .onDisappear { value = "" }
    }
}
