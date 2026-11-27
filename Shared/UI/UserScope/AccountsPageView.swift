import AgentsKitCore
import SwiftUI
#if os(macOS)
import AppKit
#endif

/// Status ▸ Accounts (#5258, `specs/5254-secrets-accounts-environment/wireframes.md` sections 3 and 6): every outside
/// account of the person's (gitlab.com, AWS, Slack…) on one page, where they find, see, add, edit and remove it. The
/// accounts part of their settings, which the control plane keeps and sends to each machine of theirs (#3967, #1760);
/// a record holds no secret, only the name of one in Secrets. The Secrets page's frame (`SecretsPageView`): a search
/// (never a value), chips with counts, a Project and a Service menu and a sort over a list with **Needs you** on top and
/// the total a month at the foot, and the selected account's detail beside it, where **Edit** turns its fields into
/// fields in place. Under 720 pt wide the list is alone, and a press opens the detail with a back arrow. It replaces the
/// read-only Status list (#4161), Settings ▸ You ▸ Accounts and its Details… sheet (#5116). The window and the Remote
/// draw it; the web page draws the same in `Web/src/views/AccountsPage.tsx`.
struct AccountsPageView: View {
    /// The control plane, or nil while it cannot be reached.
    let control: DaemonClient?
    let state: UserScopeState
    /// Each of the person's machines' names, by id, for the not-backed-up line.
    let machineNames: [String: String]
    /// Reveal and Copy (#5116): the window's; nil on the Remote (tier Mac + web), which draws neither, nor Copy.
    var show: SecretShowState?
    /// The account shown, by its id: the client keeps it, so a link can open one.
    @Binding var selection: String?
    /// Opens a secret, by its name, on the Secrets page.
    var openSecret: (String) -> Void = { _ in }

    @State private var search = ""
    @State private var chip: AccountsPage.Chip = .all
    @State private var project: String?
    @State private var service: String?
    @State private var sort: AccountsPage.Sort = .service
    @State private var adding = false
    @State private var removing: DaemonAPI.OutsideAccount?
    @State private var editing: String?
    @State private var problem: String?
    /// The control plane's `control/status`, for its capabilities (#4190); nil until it comes.
    @State private var status: DaemonAPI.ControlStatus?

    private var secrets: [DaemonAPI.UserSecretRow] { state.secrets?.secrets ?? [] }

    private var items: [AccountsPage.Item] {
        AccountsPage.items(state.accounts, secrets: secrets, machineNames: machineNames)
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
                // An older control plane refuses the accounts part, so the page offers none (#4190).
                if let refusal = AccountsWords.refusal(status) {
                    Text(refusal).appText(.supporting).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(16)
                    Spacer()
                } else {
                    PageListAndDetail(wide: wide, width: geo.size.width) {
                        list(items)
                    } detail: {
                        detailOrEmpty(items)
                    } narrow: {
                        narrow(items)
                    }
                }
            }
        }
        .task(id: control != nil) { await reload() }
        .refreshable { await reload() }
        .hidesShownSecret(show, secrets: state.secrets)
        .onChange(of: selection) { _, _ in show?.hide() }
        .sheet(isPresented: $adding) {
            AccountSheet { account in
                let failed = await save(adding: account, replacing: nil)
                if failed == nil {
                    search = ""
                    chip = .all
                    project = nil
                    service = nil
                    selection = account.id
                }
                return failed
            }
            .paperSheet()
        }
        .confirmationDialog(removing.map { "Remove \(AccountsPage.name($0))?" } ?? "",
                            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
                            titleVisibility: .visible, presenting: removing) { account in
            Button("Remove", role: .destructive) {
                Task {
                    problem = await save(adding: nil, replacing: account)
                    if problem == nil, selection == account.id { selection = nil }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text(AccountsPage.removeMessage)
        }
    }

    // MARK: Header

    private func header(_ items: [AccountsPage.Item], wide: Bool) -> some View {
        let counts = AccountsPage.counts(items, search: search, project: project, service: service)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                PageSearchField(prompt: AccountsPage.searchPrompt, label: "Search accounts", clearLabel: AccountsPage.clearSearch,
                                text: $search)
                Button { adding = true } label: { Label(AccountsPage.add, icon: "plus") }
                    .fixedSize()
                    .disabled(control == nil || AccountsWords.refusal(status) != nil)
                if wide, let read = state.readAt {
                    LastUpdateText(read: read)
                }
            }
            .frame(minHeight: 28)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(AccountsPage.Chip.allCases, id: \.self) { each in
                        chipButton(each, count: counts[each] ?? 0)
                    }
                    menu(AccountsPage.projectAny, value: $project, AccountsPage.projects(items))
                    menu(AccountsPage.serviceAny, value: $service, AccountsPage.services(items))
                    Menu {
                        Picker(AccountsPage.sortLabel, selection: $sort) {
                            ForEach(AccountsPage.Sort.allCases, id: \.self) { Text($0.title).tag($0) }
                        }
                        .pickerStyle(.inline)
                    } label: {
                        Label(sort.title, icon: "arrow.up.arrow.down").appText(.fine)
                    }
                    .menuStyle(.borderlessButton)
                    .fixedSize()
                    .accessibilityLabel("\(AccountsPage.sortLabel): \(sort.title)")
                    if !wide, let read = state.readAt {
                        LastUpdateText(read: read)
                    }
                }
            }
        }
    }

    /// The Project or the Service menu: shown while there is one to choose, or one is chosen.
    @ViewBuilder private func menu(_ any: String, value: Binding<String?>, _ names: [String]) -> some View {
        if !names.isEmpty || value.wrappedValue != nil {
            Menu {
                Button(any) { value.wrappedValue = nil }
                Divider()
                ForEach(names, id: \.self) { name in Button(name) { value.wrappedValue = name } }
            } label: {
                Text(value.wrappedValue ?? any).appText(.fine)
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
        }
    }

    private func chipButton(_ each: AccountsPage.Chip, count: Int) -> some View {
        let on = chip == each
        return PageChip(title: each.title, count: count, warns: each == .needsYou, on: on) { chip = on && each != .all ? .all : each }
    }

    // MARK: List

    private func list(_ items: [AccountsPage.Item]) -> some View {
        let groups = AccountsPage.groups(items, search: search, chip: chip, project: project, service: service, sort: sort)
        let shown = groups.flatMap(\.items)
        return List(selection: $selection) {
            if let problem = problem ?? state.problem {
                Text(problem).appText(.supporting).tinted(.failure)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let only = SecretInfoWords.onlyMachine(secrets), state.accounts.contains(where: { AccountsWords.secretName($0) != nil }) {
                Label(SecretInfoWords.onlyMachineLine(only, names: machineNames), icon: "exclamationmark.triangle")
                    .appText(.supporting).tinted(.warning)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if groups.isEmpty { emptyList(items) }
            ForEach(groups) { group in
                if let title = group.title {
                    Section {
                        ForEach(group.items) { item in row(item).tag(item.id) }
                    } header: {
                        Text("\(title) · \(group.items.count)")
                    }
                } else {
                    Section {
                        ForEach(group.items) { item in row(item).tag(item.id) }
                    }
                }
            }
            Text(AccountsPage.foot(shown: shown, of: items.count))
                .appText(.fine).foregroundStyle(.secondary)
                .selectionDisabled()
        }
        .scrollContentBackground(.hidden)
    }

    @ViewBuilder private func emptyList(_ items: [AccountsPage.Item]) -> some View {
        if items.isEmpty {
            if status != nil {
                Text(AccountsPage.none).appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .selectionDisabled()
            }
        } else if !search.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Text(AccountsPage.noMatch(search)).appText(.supporting).foregroundStyle(.secondary)
                Button(AccountsPage.clearSearch) { search = "" }
            }
            .selectionDisabled()
        }
    }

    private func row(_ item: AccountsPage.Item) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                if item.needs != nil {
                    Image(icon: "exclamationmark.triangle").tinted(.warning)
                }
                Text(AccountsPage.name(item.account)).lineLimit(1)
                Spacer(minLength: 8)
                Text(AccountsWords.hint(item.account, rows: secrets)).appText(.code).foregroundStyle(.secondary)
            }
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(AccountsPage.subtitle(item)).appText(.fine)
                    .foregroundStyle(item.needs == nil ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
                    .tinted(item.needs == nil ? .none : .warning)
                    .lineLimit(1)
                Spacer(minLength: 8)
                if let cost = AccountsPage.shortCost(item.account) {
                    Text(cost).appText(.fine).foregroundStyle(.secondary).monospacedDigit()
                }
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
        #if os(iOS)
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) { removing = item.account } label: { Text("Remove") }
        }
        #endif
    }

    // MARK: Narrow

    @ViewBuilder private func narrow(_ items: [AccountsPage.Item]) -> some View {
        #if os(iOS)
        list(items)
            .navigationDestination(item: $selection) { id in
                if let item = self.items.first(where: { $0.id == id }) {
                    ScrollView { detail(item) }
                        .navigationTitle(AccountsPage.name(item.account))
                        .navigationBarTitleDisplayMode(.inline)
                } else {
                    Text(AccountsPage.selectOne).foregroundStyle(.secondary)
                }
            }
        #else
        if let id = selection, let item = items.first(where: { $0.id == id }) {
            VStack(alignment: .leading, spacing: 0) {
                Button { selection = nil } label: { Label(AccountsPage.title, icon: "chevron.left") }
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

    @ViewBuilder private func detailOrEmpty(_ items: [AccountsPage.Item]) -> some View {
        if let id = selection, let item = items.first(where: { $0.id == id }) {
            ScrollView { detail(item) }
        } else {
            VStack(spacing: 6) {
                Text(AccountsPage.selectOne).appText(.supporting).foregroundStyle(.secondary)
                if !items.isEmpty {
                    Text(AccountsPage.foot(shown: items, of: items.count)).appText(.fine).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func detail(_ item: AccountsPage.Item) -> some View {
        AccountDetail(item: item, rows: secrets, control: control, show: show, isEditing: editing == item.id,
                      openSecret: openSecret,
                      edit: { editing = $0 ? item.id : nil },
                      remove: { removing = item.account },
                      save: { changed in
                          let failed = await save(adding: changed, replacing: item.account)
                          if failed == nil { selection = changed.id }
                          return failed
                      })
            .id(item.id)
            .padding(16)
    }

    // MARK: Changes

    /// The control plane's capabilities first, then the person's settings.
    private func reload() async {
        if let control {
            status = try? await control.call(DaemonAPI.Method.controlStatus, returning: DaemonAPI.ControlStatus.self)
        }
        await state.load(control)
    }

    /// The accounts with `replacing` taken out and `adding` put in its place, set as the part.
    private func save(adding: DaemonAPI.OutsideAccount?, replacing: DaemonAPI.OutsideAccount?) async -> String? {
        var accounts = state.accounts
        let at = replacing.flatMap { old in accounts.firstIndex { $0.id == old.id } }
        if let at { accounts.remove(at: at) }
        if let adding { accounts.insert(adding, at: at ?? accounts.endIndex) }
        guard let value = try? JSONValue.encoding(DaemonAPI.OutsideAccounts(accounts: accounts)) else { return "The accounts could not be written." }
        return await state.set(.accounts, to: value, on: control)
    }
}

/// The detail of one account (wireframes §3): what Details… showed (#5116), beside the list. Each field, with Copy for
/// each plain one on the Mac; the secret and the card it names, with Reveal and Copy on the Mac and → to open it in
/// Secrets; Ask before use as a switch, saved at once (an account is not sealed); the age line; Edit, which turns the
/// fields into fields in place, and Remove….
private struct AccountDetail: View {
    let item: AccountsPage.Item
    let rows: [DaemonAPI.UserSecretRow]
    let control: DaemonClient?
    let show: SecretShowState?
    let isEditing: Bool
    let openSecret: (String) -> Void
    let edit: (Bool) -> Void
    let remove: () -> Void
    let save: (DaemonAPI.OutsideAccount) async -> String?

    @State private var fields: [String: String] = [:]
    @State private var problem: String?
    @State private var saving = false
    /// The plain field just copied, for its check mark.
    @State private var copied: String?

    private var account: DaemonAPI.OutsideAccount { item.account }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(AccountsPage.name(account)).appText(.title).textSelection(.enabled)
            if isEditing {
                editor
            } else {
                Text(AccountsWords.about(account)).appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Divider()
                grid
                SecretInfoLine(parts: AccountsWords.line(account))
            }
            PageProblemLine(problem: problem)
            if !isEditing { actions }
        }
        .pageDetail(isEditing: isEditing, reset: reset)
    }

    private func reset() {
        fields = AccountsWords.fields(account)
        problem = nil
    }

    private var grid: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 8) {
            plain("Service", account.service)
            plain("Address", account.address)
            plain("User name", account.username)
            if let plan = AccountsPage.planLine(account) {
                GridRow { label("Plan"); Text(plan) }
            }
            slot("Secret", AccountsWords.named(account.secret, rows: rows))
            if account.card?.trimmingCharacters(in: .whitespaces).isEmpty == false {
                slot("Card", AccountsWords.named(account.card, rows: rows))
            }
            GridRow { label("Two-factor"); Text(trimmed(account.twoFactor) ?? "None") }
            if let expiry = AccountsWords.tokenExpiry(account) {
                GridRow { label("Token expires"); Text(expiry) }
            }
            GridRow {
                label("Projects")
                Text((account.projects ?? []).isEmpty ? "All" : (account.projects ?? []).joined(separator: ", "))
            }
            // A switch has no text baseline: the row lines up at the top, or the label rides up into the one above.
            GridRow(alignment: .top) {
                label(AskBeforeUseWords.toggle).padding(.top, 4)
                VStack(alignment: .leading, spacing: 2) {
                    Toggle(AskBeforeUseWords.toggle, isOn: Binding(get: { AccountsWords.asks(account) }, set: { on in
                        var changed = account
                        changed.askBeforeUse = on ? nil : false
                        Task { problem = await save(changed) }
                    }))
                    .labelsHidden()
                    .toggleStyle(.switch)
                    .disabled(control == nil)
                    Text(AccountsWords.askHint(AccountsWords.asks(account))).appText(.fine).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            plain("Notes", account.notes)
            if let issue = trimmed(account.issue) {
                GridRow { label("Issue"); Text(issue) }
            }
            PageNeedsYouRow(warning: item.needs)
        }
        .appText(.supporting)
    }

    private func label(_ text: String) -> some View { PageGridLabel(text) }

    private func trimmed(_ s: String?) -> String? {
        let t = (s ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }

    /// A plain field, with Copy on the Mac: a copy of one is not audited (#5116).
    @ViewBuilder private func plain(_ name: String, _ value: String?) -> some View {
        if let value = trimmed(value) {
            GridRow {
                label(name)
                HStack(spacing: 8) {
                    Text(value).fixedSize(horizontal: false, vertical: true)
                    #if os(macOS)
                    if show != nil {
                        Button {
                            PlainClipboard.copy(value)
                            flash(name)
                        } label: {
                            Image(icon: copied == name ? "checkmark" : "doc.on.doc")
                        }
                        .buttonStyle(.borderless)
                        .help("Copy \(name)")
                        .accessibilityLabel("Copy \(name)")
                    }
                    #endif
                }
            }
        }
    }

    /// The secret or the card it names: its name and last 4, Reveal and Copy (the Mac), and → to open it in Secrets.
    @ViewBuilder private func slot(_ name: String, _ named: AccountsWords.Named) -> some View {
        GridRow {
            label(name)
            switch named {
            case .none:
                Text(name == "Secret" ? "—   " + AccountsWords.noSecretLine : "—").foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            case .elsewhere(let text):
                Text("\(text) · \(AccountsWords.notInSecrets)").foregroundStyle(.secondary)
            case .secret(let row):
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 8) {
                        Text(row.name).appText(.code)
                        if show?.shown?.name != row.name {
                            Text(SecretInfoWords.hint(row.hint)).appText(.code).foregroundStyle(.secondary)
                        }
                        if let show {
                            SecretShowButtons(row: row, show: show, control: control, account: AccountsWords.auditAccount(account))
                        }
                        Button { openSecret(row.name) } label: { Image(icon: "arrow.right") }
                            .buttonStyle(.borderless)
                            .help(AccountsPage.openSecret)
                            .accessibilityLabel("\(AccountsPage.openSecret): \(row.name)")
                    }
                    if let show, let shown = show.shown, shown.name == row.name {
                        SecretShownValue(row: row, shown: shown, show: show, control: control, account: AccountsWords.auditAccount(account))
                    }
                    if let show { SecretShowLines(row: row, show: show) }
                }
            }
        }
    }

    private func flash(_ name: String) {
        copied = name
        Task {
            try? await Task.sleep(for: .seconds(SecretRevealWords.copiedSeconds))
            if copied == name { copied = nil }
        }
    }

    private var actions: some View {
        HStack(spacing: 10) {
            Button(AccountsPage.edit) { edit(true) }.disabled(control == nil)
            Spacer()
            Button(AccountsPage.remove, role: .destructive, action: remove).disabled(control == nil)
        }
        .padding(.top, 8)
    }

    /// Edit, in place: the record's fields, each optional but the service.
    private var editor: some View {
        VStack(alignment: .leading, spacing: 10) {
            AccountFields(fields: $fields)
            Text(AccountsWords.noSecrets).appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Cancel", role: .cancel) { edit(false) }
                Spacer()
                Button("Save") {
                    Task {
                        saving = true
                        var changed = AccountsWords.account(fields)
                        // Kept by the control plane, not the fields: an edit does not lose them.
                        changed.edited = account.edited
                        changed.lastOp = account.lastOp
                        problem = await save(changed)
                        saving = false
                        if problem == nil { edit(false) }
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(saving || (fields["service"] ?? "").trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
    }
}

/// The record's fields, for Add Account… and an Edit in place: each optional but the service.
private struct AccountFields: View {
    @Binding var fields: [String: String]

    private static let all: [(label: String, key: String, prompt: String)] = [
        ("Service", "service", "gitlab.com"), ("Address", "address", "name@example.com"),
        ("User name", "username", "When it is not the address"), ("Description", "description", "What it is for, and who uses it"),
        ("Plan", "plan", "Free"), ("Monthly cost (USD)", "cost", "0"), ("Card", "card", "The card's name in Secrets"),
        ("Secret", "secret", "The token's name in Secrets"), ("Two-factor", "twoFactor", "TOTP, passkey, SMS"),
        ("Issue", "issue", "#12"), ("Made", "made", "YYYY-MM-DD"), ("Token expires", "tokenExpires", "YYYY-MM-DD"),
        ("Projects", "projects", "None is all, else names with commas"), ("Notes", "notes", "Where the password is"),
    ]

    var body: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 8) {
            ForEach(Self.all, id: \.key) { field in
                GridRow {
                    Text(field.label).appText(.supporting).foregroundStyle(.secondary).gridColumnAlignment(.leading)
                    input(field.label, field.key, field.prompt)
                }
            }
        }
        AccountAskToggle(fields: $fields)
    }

    private func input(_ label: String, _ key: String, _ prompt: String) -> some View {
        let text = Binding(get: { fields[key] ?? "" }, set: { fields[key] = $0 })
        // A description is one or two sentences: it wraps rather than scrolls (#4164).
        return TextField(label, text: text, prompt: Text(prompt), axis: key == "description" ? .vertical : .horizontal)
            .textFieldStyle(.roundedBorder)
            .autocorrectionDisabled()
            #if os(iOS)
            .textInputAutocapitalization(key == "notes" || key == "description" ? .sentences : .never)
            .keyboardType(key == "cost" ? .decimalPad : key == "address" ? .emailAddress : .default)
            #endif
            .accessibilityLabel(label)
    }
}

/// Ask before use (#4163) on Add Account… and Edit: on at first. As `AskBeforeUseToggle`, with an account's words under it.
private struct AccountAskToggle: View {
    @Binding var fields: [String: String]

    var body: some View {
        let isOn = Binding(get: { fields[AccountsWords.askField] != "off" },
                           set: { fields[AccountsWords.askField] = $0 ? nil : "off" })
        VStack(alignment: .leading, spacing: 2) {
            Toggle(AskBeforeUseWords.toggle, isOn: isOn)
                #if os(macOS)
                .toggleStyle(.checkbox)
                #endif
                .appText(.supporting)
            Text(AccountsWords.askHint(isOn.wrappedValue))
                .appText(.fine).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

/// Add Account…: the record's fields. The new account is selected when it is saved (wireframes §2a).
private struct AccountSheet: View {
    let save: (DaemonAPI.OutsideAccount) async -> String?

    @Environment(\.dismiss) private var dismiss
    @State private var fields: [String: String] = [:]
    @State private var problem: String?
    @State private var saving = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text("Add an Account").appText(.title)
                AccountFields(fields: $fields)
                Text(problem ?? AccountsWords.noSecrets).appText(.fine)
                    .tinted(problem == nil ? .none : .failure)
                    .fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button("Cancel", role: .cancel) { dismiss() }
                    Spacer()
                    Button("Add") {
                        Task {
                            saving = true
                            problem = await save(AccountsWords.account(fields))
                            saving = false
                            if problem == nil { dismiss() }
                        }
                    }
                    .keyboardShortcut(.defaultAction)
                    .disabled(saving || (fields["service"] ?? "").trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
            .padding(20)
        }
        #if os(macOS)
        .frame(minWidth: 460, minHeight: 520)
        #endif
    }
}

#if os(macOS)
/// The general pasteboard for a plain field a person copies (#5116): plain text, not audited and not cleared. A secret's
/// copy goes through `SecretShowState`, marked concealed.
@MainActor
enum PlainClipboard {
    static func copy(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }
}
#endif
