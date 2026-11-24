import AgentsKitCore
import SwiftUI

/// Why the provider's form could not be read, in the app's words.
struct NewMachineRefusal: Error {
    var message: String
    init(_ message: String) { self.message = message }
}

/// What New Machine makes, as its sheet has it (#4634).
struct NewMachineDraft: Equatable, Sendable {
    var provider: ProviderID
    var name: String
    var values: [String: JSONValue]
    /// Clone these folders (#1122): git URLs. Only Settings' Add a Machine… asks for them.
    var clones: [String] = []
    /// Stop when idle, in minutes (0 is never), for a sheet that asks (#5358); nil when it does not.
    var stopWhenIdleMinutes: Int?
    /// The project folders on this Mac to send to it once it has joined (#5358).
    var projects: [URL] = []
    /// Start from a warm pool (#5366): claim a ready machine of this provider and size.
    var fromPool = false
}

/// What the Mac's New Machine asks besides the provider's size (#5358): a sleep time and
/// the projects to send. The Remote's sheet has none of it and draws the provider's form.
struct NewMachineChoices {
    /// The project folders on this Mac.
    var projects: [URL]
    /// Whether a project has uncommitted changes, which go with it.
    var hasChanges: @MainActor (URL) async -> Bool
    /// The person's warm pools (#5366), for Start from; none on a control plane before pools.
    var pools: @MainActor () async -> [DaemonAPI.WarmPool] = { [] }
}

/// The New Machine sheet up (#4634): for a provider (nil for the default one), or opened
/// again on the choices of a make the provider refused, with its reason.
struct NewMachineAsk: Identifiable, Equatable {
    let id = UUID()
    var provider: ProviderID?
    var again: NewMachineDraft?
    var problem: String?
}

/// New Machine (#691 US5, #4634): a form drawn from the provider's `providers/options`, with
/// every field already on what New Machine would make (the options used last on the
/// provider, else its first choices) and the next `machine-N`. Make is the confirmation:
/// nothing is made until it is pressed (US5 AS3), and Cancel makes nothing. Drawn by the
/// window and the Remote, as the web page's NewMachineSheet.
struct NewMachineSheet: View {
    typealias Refusal = NewMachineRefusal

    @Environment(\.dismiss) private var dismiss
    /// The providers New Machine can use: a picker when there are two or more.
    let providers: [DaemonAPI.ProviderSummary]
    /// Clone these folders: Settings' Add a Machine… has it; the sidebar's New Machine does
    /// not (you add a folder later, the wireframe).
    var showsClones = false
    /// The Mac's three choices (#5358): when set, the form shows only Size, Stop when idle and
    /// Projects to send; name and every other field come from the provider's defaults.
    var choices: NewMachineChoices?
    /// The provider's form, with the values to start with; nil for the default provider.
    let load: @MainActor (ProviderID?) async -> Result<DaemonAPI.ProviderOptions, Refusal>
    /// Makes it: nil when the sheet may close, or the provider's reason, shown in the sheet.
    let make: @MainActor (NewMachineDraft) async -> String?

    @State private var provider: ProviderID?
    @State private var schema: [Field] = []
    @State private var loading = true
    @State private var name = ""
    @State private var values: [String: JSONValue] = [:]
    @State private var clones = ""
    @State private var idleMinutes = WorkspaceLink.defaultIdleMinutes
    @State private var sending: Set<URL> = []
    @State private var changed: Set<URL> = []
    @State private var working = false
    @State private var problem: String?
    /// The person's warm pools, and whether Start from is on a pool (#5366).
    @State private var pools: [DaemonAPI.WarmPool] = []
    @State private var fromPool = true
    /// A make that failed opens the sheet again on its choices and its reason.
    private let again: NewMachineDraft?

    init(providers: [DaemonAPI.ProviderSummary], provider: ProviderID? = nil, again: NewMachineDraft? = nil,
         problem: String? = nil, showsClones: Bool = false, choices: NewMachineChoices? = nil,
         load: @escaping @MainActor (ProviderID?) async -> Result<DaemonAPI.ProviderOptions, Refusal>,
         make: @escaping @MainActor (NewMachineDraft) async -> String?) {
        self.providers = providers
        self.showsClones = showsClones
        self.choices = choices
        self.load = load
        self.make = make
        self.again = again
        _provider = State(initialValue: again?.provider ?? provider)
        _problem = State(initialValue: problem)
    }

    /// One property of the schema: a string, integer, boolean or enum, with a title.
    struct Field: Identifiable {
        var id: String
        var title: String
        var kind: String
        var choices: [String]
        var costs: [String: String]
        /// Each choice as a person reads it (`x-titles`, #6073), such as San Francisco 3.
        var titles: [String: String] = [:]
        var placeholder: String?

        static func read(_ schema: JSONValue) -> [Field] {
            let required = Set(schema["required"]?.arrayValue?.compactMap(\.stringValue) ?? [])
            let properties = schema["properties"]?.objectValue ?? [:]
            return properties.keys.sorted { a, b in
                // The schema's own order is lost in an object: required first, then by name.
                required.contains(a) != required.contains(b) ? required.contains(a) : a < b
            }.compactMap { key in
                guard let property = properties[key] else { return nil }
                let choices = property["enum"]?.arrayValue?.compactMap { $0.stringValue ?? $0.intValue.map(String.init) } ?? []
                return Field(id: key, title: property["title"]?.stringValue ?? key.capitalized,
                             kind: choices.isEmpty ? (property["type"]?.stringValue ?? "string") : "enum",
                             choices: choices,
                             costs: (property["x-costs"]?.objectValue ?? [:]).compactMapValues(\.stringValue),
                             titles: (property["x-titles"]?.objectValue ?? [:]).compactMapValues(\.stringValue),
                             placeholder: property["description"]?.stringValue)
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            #if os(macOS)
            Text(title).appText(.title).fontWeight(.semibold)
            #endif
            Form {
                if providers.count > 1 {
                    Picker("Provider", selection: Binding(get: { provider ?? providers[0].id },
                                                          set: { chosen in
                                                              guard chosen != provider else { return }
                                                              provider = chosen
                                                              Task { await reload() }
                                                          })) {
                        ForEach(providers) { Text($0.name).tag($0.id) }
                    }
                    .disabled(working)
                }
                if choices == nil {
                    labeled("Name") {
                        TextField("Name", text: $name, prompt: Text("machine-1"))
                            .autocorrectionDisabled()
                            #if os(iOS)
                            .textInputAutocapitalization(.never)
                            #endif
                    }
                }
                if choices != nil, !loading, let pool = matchingPool {
                    startFrom(pool)
                }
                if loading {
                    ProgressView().controlSize(.small)
                } else {
                    ForEach(shownFields) { field in fieldView(field) }
                    if choices != nil, !pools.filter({ $0.provider == provider }).isEmpty {
                        Text("A pool has one size: choosing another size starts a new machine.")
                            .appText(.fine).foregroundStyle(.secondary)
                    }
                }
                if let choices {
                    Picker("Stop when idle", selection: $idleMinutes) {
                        ForEach(MachineProviderWords.idleChoices, id: \.minutes) { Text($0.label).tag($0.minutes) }
                    }
                    Section("Projects to send") {
                        if choices.projects.isEmpty {
                            Text("No projects on this Mac.").foregroundStyle(.secondary)
                        }
                        ForEach(choices.projects, id: \.self) { project in
                            Toggle(isOn: Binding(get: { sending.contains(project) },
                                                 set: { on in
                                                     if on { sending.insert(project) } else { sending.remove(project) }
                                                 })) {
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(project.lastPathComponent)
                                    if let words = projectLine(project) {
                                        Text(words).appText(.fine).foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                    }
                }
                if showsClones {
                    TextField("Clone these folders", text: $clones,
                              prompt: Text("https://github.com/you/folder.git"), axis: .vertical)
                        .lineLimit(1...5)
                }
                #if os(iOS)
                // The notes under the form, as a footer, where iOS puts a form's words.
                Section { } footer: { notes }
                #endif
            }
            .formStyle(.grouped)
            .disabled(working)
            #if os(macOS)
            notes
            HStack {
                if !cost.isEmpty {
                    Text(cost).appText(.supporting).foregroundStyle(.secondary)
                }
                Spacer()
                if working { ProgressView().controlSize(.small) }
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Make") { press() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!ready)
            }
            #endif
        }
        #if os(macOS)
        .padding(28)
        .frame(width: 480)
        #else
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            ToolbarItem(placement: .confirmationAction) {
                if working { ProgressView() } else { Button("Make") { press() }.disabled(!ready) }
            }
        }
        #endif
        .task { await reload(keeping: again) }
        .task {
            guard let choices else { return }
            for project in choices.projects where await choices.hasChanges(project) { changed.insert(project) }
        }
        .task {
            guard let choices, again == nil || again?.fromPool == true else { return }
            pools = await choices.pools()
        }
    }

    /// The pool Start from offers (#5366, board 1): one of this provider and size, with a
    /// ready machine if any has one. Nil when there is none, and Start from is not drawn.
    private var matchingPool: DaemonAPI.WarmPool? {
        WarmPoolWords.matching(pools, provider: provider, size: chosenSize).first
    }

    /// The size the form has chosen, as a pool names it.
    private var chosenSize: String? {
        shownFields.first.flatMap { chosen[$0.id]?.stringValue }
    }

    /// Whether Make claims from the pool: it is chosen, and has a ready machine.
    private var claimsFromPool: Bool {
        guard fromPool, let pool = matchingPool else { return false }
        return (pool.ready ?? 0) > 0
    }

    /// Start from (#5366, board 1, note 1): the pool, with how many are ready and how soon,
    /// or a new machine.
    @ViewBuilder
    private func startFrom(_ pool: DaemonAPI.WarmPool) -> some View {
        let ready = (pool.ready ?? 0) > 0
        Picker("Start from", selection: Binding(get: { ready && fromPool }, set: { fromPool = $0 })) {
            Text("Warm pool “\(pool.name)” · \(WarmPoolWords.startFromLine(pool))").tag(true)
                .selectionDisabled(!ready)
            Text("A new machine · \(WarmPoolWords.newMachineLine)").tag(false)
        }
        #if os(macOS)
        .pickerStyle(.radioGroup)
        #else
        .pickerStyle(.inline)
        #endif
    }

    /// The line under a project: what the pool holds of it, and its uncommitted changes.
    private func projectLine(_ project: URL) -> String? {
        var parts: [String] = []
        if claimsFromPool, matchingPool?.projects?.contains(project.path) == true { parts.append("in the pool") }
        if changed.contains(project) { parts.append("uncommitted changes go with it") }
        guard !parts.isEmpty else { return nil }
        let line = parts.joined(separator: " · ")
        return line.prefix(1).uppercased() + line.dropFirst()
    }

    /// The provider's refusal, what happens once it is made, and on iOS what it costs.
    @ViewBuilder
    private var notes: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let problem {
                Text(problem).appText(.supporting).tinted(.failure).fixedSize(horizontal: false, vertical: true)
            }
            Text(showsClones
                 ? "It joins as a machine by itself once it has started, and is linked to it. Then it clones the folders you list, one a line, with this Mac’s git access when you allow it."
                 : "It joins as a machine by itself once it has started. The provider charges for it from when it is made.")
                .appText(.supporting).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            #if os(iOS)
            if !cost.isEmpty {
                Text(cost).appText(.supporting).foregroundStyle(.secondary)
            }
            #endif
        }
    }

    /// A text field with its title beside it: iOS's form shows only a field's prompt.
    @ViewBuilder
    private func labeled(_ title: String, @ViewBuilder field: () -> some View) -> some View {
        #if os(iOS)
        LabeledContent(title) { field().multilineTextAlignment(.trailing) }
        #else
        field()
        #endif
    }

    /// The form's fields: all of the provider's, or with the Mac's choices only its size.
    private var shownFields: [Field] {
        guard choices != nil else { return schema }
        let size = schema.first { $0.id.lowercased() == "size" || $0.title.lowercased() == "size" }
            ?? schema.first { $0.kind == "enum" }
        return size.map { [$0] } ?? []
    }

    private var title: String {
        guard let provider, let named = providers.first(where: { $0.id == provider }) else { return "New Machine" }
        return providers.count > 1 ? "New Machine" : "New Machine on \(named.name)"
    }

    @ViewBuilder
    private func fieldView(_ field: Field) -> some View {
        switch field.kind {
        case "enum":
            Picker(field.title, selection: binding(field, default: field.choices.first ?? "")) {
                ForEach(field.choices, id: \.self) { choice in
                    // A size's title has its price already (#6577).
                    Text(field.titles[choice] ?? field.costs[choice].map { "\(choice) — \($0)" } ?? choice).tag(choice)
                }
            }
        case "boolean":
            Toggle(field.title, isOn: Binding(get: { values[field.id]?.boolValue ?? false },
                                              set: { values[field.id] = .bool($0) }))
        case "integer", "number":
            labeled(field.title) {
                TextField(field.title, text: Binding(get: { values[field.id].map { $0.intValue.map(String.init) ?? "" } ?? "" },
                                                     set: { values[field.id] = Int($0).map(JSONValue.int) }),
                          prompt: field.placeholder.map(Text.init))
                    #if os(iOS)
                    .keyboardType(.numberPad)
                    #endif
            }
        default:
            labeled(field.title) {
                TextField(field.title, text: binding(field, default: ""), prompt: field.placeholder.map(Text.init))
            }
        }
    }

    private func binding(_ field: Field, default value: String) -> Binding<String> {
        Binding(get: { values[field.id]?.stringValue ?? value },
                set: { values[field.id] = $0.isEmpty ? nil : .string($0) })
    }

    private var trimmedName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var ready: Bool { !loading && !working && provider != nil && AddMachine.validName(trimmedName) }

    /// What the chosen options cost, when the provider names it (`x-costs`).
    private var cost: String {
        schema.compactMap { field in chosen[field.id]?.stringValue.flatMap { field.costs[$0] } }.joined(separator: " · ")
    }

    /// The choices with each enum's first value where none was picked, as the form shows it.
    private var chosen: [String: JSONValue] {
        var all = values
        for field in schema where field.kind == "enum" && all[field.id] == nil {
            if let first = field.choices.first { all[field.id] = .string(first) }
        }
        return all
    }

    /// Reads the provider's form, and puts every field on what New Machine would make, or on
    /// the choices a make that failed had.
    private func reload(keeping kept: NewMachineDraft? = nil) async {
        loading = true
        defer { loading = false }
        switch await load(provider) {
        case .success(let options):
            provider = options.id ?? provider ?? providers.first?.id
            schema = Field.read(options.schema)
            if let kept {
                name = kept.name
                values = kept.values
                clones = kept.clones.joined(separator: "\n")
                idleMinutes = kept.stopWhenIdleMinutes ?? idleMinutes
                sending = Set(kept.projects)
            } else {
                // A name typed already stays when the provider changes.
                if name.isEmpty || name.hasPrefix("machine-") { name = options.name ?? name }
                values = options.values?.objectValue ?? [:]
            }
        case .failure(let refusal):
            problem = refusal.message
        }
    }

    private func press() {
        guard let provider else { return }
        working = true
        problem = nil
        let urls = clones.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        var draft = NewMachineDraft(provider: provider, name: trimmedName, values: chosen, clones: urls)
        if let choices {
            draft.stopWhenIdleMinutes = idleMinutes
            draft.projects = choices.projects.filter(sending.contains)
            draft.fromPool = claimsFromPool
        }
        Task {
            let refused = await make(draft)
            working = false
            if let refused { problem = refused } else { dismiss() }
        }
    }
}
