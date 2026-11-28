import AgentsKitCore
import SwiftUI

/// Settings ▸ Machine Providers ▸ ‹provider› ▸ Warm Pools (#5366, epic #5353, board 5): a card
/// per pool, with how many machines are ready of min and max, how many are being set up, the
/// last seven days' claims, what the idle ones cost and a line for each machine, and New Warm
/// Pool… and Edit… on the same form. Only a provider that makes machines has pools.
struct WarmPoolsSection: View {
    @Environment(AppModel.self) private var model
    let control: ControlSettingsModel
    let provider: DaemonAPI.ProviderSummary

    @State private var editing: PoolEdit?
    @State private var deleting: DaemonAPI.WarmPool?
    /// A pool that waits for root.sh (#7157): the review up, and why it could not be read.
    @State private var reviewing: PoolReview?
    @State private var reviewProblem: [String: String] = [:]

    struct PoolReview: Identifiable {
        let id = UUID()
        var pool: DaemonAPI.WarmPool
        var scripts: [DaemonAPI.SetupScript]
    }

    /// The form up: a new pool, or the one to change.
    struct PoolEdit: Identifiable {
        let id = UUID()
        var pool: DaemonAPI.WarmPool?
    }

    private var pools: [DaemonAPI.WarmPool] { control.pools.filter { $0.provider == provider.id } }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                Text("Warm Pools").appText(.supporting).fontWeight(.semibold).foregroundStyle(.secondary)
                if let read = control.poolsReadAt { LastUpdateText(read: read) }
                Spacer()
                Button("New Warm Pool…") { editing = PoolEdit() }
                    .buttonStyle(.paper)
                    .disabled(!provider.usable)
            }
            if pools.isEmpty {
                Text("A warm pool keeps machines ready: made, joined, with Agents, runtimes and the projects you choose, and signed in to nothing. New Machine takes one in seconds, and the pool makes another.")
                    .appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            ForEach(pools) { pool in card(pool) }
        }
        .padding(.leading, 12)
        .sheet(item: $editing) { edit in
            WarmPoolSheet(control: control, provider: provider, pool: edit.pool, projects: model.newMachineChoices.projects)
                .paperSheet()
        }
        .sheet(item: $reviewing) { review in
            SetupScriptReviewSheet(scripts: review.scripts, sender: model.hosts.label(.mac), action: "Approve",
                                   approve: { await model.approveSetupScripts($0) }) {
                reviewing = nil
                Task { await resume(review.pool) }
            }
            .paperSheet()
        }
        .confirmationDialog(deleting.map { "Delete \($0.name)?" } ?? "",
                            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            titleVisibility: .visible, presenting: deleting) { pool in
            Button("Delete", role: .destructive) { Task { await control.deletePool(pool.id) } }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("Its ready machines are deleted. Machines already claimed stay as they are.")
        }
    }

    private func card(_ pool: DaemonAPI.WarmPool) -> some View {
        ControlCard {
            ControlRow(dot: pool.state == .stopped ? .attention : .none, title: pool.name, chip: WarmPoolWords.chip(pool),
                       chipTone: pool.state == .stopped ? .attention : .source, detail: WarmPoolWords.summary(pool)) {
                Menu {
                    Button("Edit…") { editing = PoolEdit(pool: pool) }
                    Divider()
                    Button("Delete…") { deleting = pool }
                } label: {
                    Image(icon: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .accessibilityLabel("More for \(pool.name)")
            }
            Divider()
            HStack(alignment: .top, spacing: 24) {
                figure("Ready", value: pool.ready ?? 0, words: WarmPoolWords.readyLine(pool))
                figure("Setting up", value: pool.settingUp ?? 0, words: nil)
                VStack(alignment: .leading, spacing: 3) {
                    Text("Last 7 days").appText(.fine).foregroundStyle(.secondary)
                    Text(WarmPoolWords.claimsLine(pool.claims)).appText(.supporting)
                    Text(WarmPoolWords.idleCost(pool, hourly: hourly(pool))).appText(.supporting).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            if pool.review == "root" {
                reviewBox(pool)
            } else if let why = WarmPoolWords.stateLine(pool) {
                Text(why)
                    .appText(.supporting)
                    .foregroundStyle((pool.state == .stopped ? StateTint.failure : StateTint.none).style(or: .secondary))
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 10)
            }
            if let machines = pool.machines, !machines.isEmpty {
                Divider()
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(machines) { machine in
                            HStack(spacing: 8) {
                                Circle().frame(width: 8, height: 8).tinted(tint(machine.state))
                                Text(WarmPoolWords.machineLine(machine, now: context.date))
                                    .appText(.supporting).monospacedDigit()
                            }
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 12)
                }
            }
        }
    }

    /// *root.sh changed* (#7157, gate 2 board *pool waits*): the pool makes no machines until the
    /// person reviews it; after a yes it replaces its unclaimed machines.
    private func reviewBox(_ pool: DaemonAPI.WarmPool) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(WarmPoolWords.reviewLine).foregroundStyle(StateTint.attention.style(or: .primary))
            Text(WarmPoolWords.reviewAfter(pool)).appText(.fine).foregroundStyle(.secondary)
            if let why = reviewProblem[pool.id] {
                Text(why).appText(.fine).foregroundStyle(StateTint.failure.style(or: .secondary))
            }
            if model.carriedHere(provider) {
                Button("Review root.sh") { Task { await review(pool) } }
                    .buttonStyle(.borderedProminent)
                    .help("Look at what changed in root.sh, and approve it for new machines")
            } else {
                Text("Review it in Agent Loops on \(provider.machineName ?? "the machine that carries \(provider.name)").")
                    .appText(.fine).foregroundStyle(.secondary)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(StateTint.attention.color?.opacity(0.4) ?? .secondary))
        .padding(.horizontal, 16)
        .padding(.bottom, 10)
    }

    /// Reads root.sh live: one approved already (on New Machine) only starts the pool again.
    private func review(_ pool: DaemonAPI.WarmPool) async {
        reviewProblem[pool.id] = nil
        do {
            let root = try await model.readSetupScripts().scripts.filter { $0.name == "root" && $0.needsApprovalForMade }
            if root.isEmpty {
                await resume(pool)
            } else {
                reviewing = PoolReview(pool: pool, scripts: root)
            }
        } catch let error as JSONRPCError {
            reviewProblem[pool.id] = error.message
        } catch {
            reviewProblem[pool.id] = "\(model.hosts.label(.mac))’s root.sh can’t be read now."
        }
    }

    /// After the yes: the pool makes again, and replaces its machines from before.
    private func resume(_ pool: DaemonAPI.WarmPool) async {
        if let refused = await control.setPool(WarmPoolWords.reviewed(pool)) { reviewProblem[pool.id] = refused }
    }

    private func figure(_ title: String, value: Int, words: String?) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title).appText(.fine).foregroundStyle(.secondary)
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text("\(value)").appText(.title).fontWeight(.semibold).monospacedDigit()
                if let words { Text(words).appText(.supporting).foregroundStyle(.secondary) }
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func tint(_ state: DaemonAPI.WarmPoolMachine.State) -> StateTint {
        switch state {
        case .ready: .vouched
        case .settingUp: .attention
        case .failed: .failure
        }
    }

    /// The pool's size's price, from what the provider's machines of that size cost.
    private func hourly(_ pool: DaemonAPI.WarmPool) -> Double? {
        control.poolPrices[pool.provider]?[pool.size ?? ""]
    }
}

/// New Warm Pool… and Edit… (#5366, board 5, note 3): name, size, region, min ready and max
/// size, the age after which a ready machine is replaced, the hours it runs, and the projects
/// it sends, with what it costs. Save keeps it; the control plane makes or deletes at once.
struct WarmPoolSheet: View {
    @Environment(\.dismiss) private var dismiss
    let control: ControlSettingsModel
    let provider: DaemonAPI.ProviderSummary
    let pool: DaemonAPI.WarmPool?
    /// The project folders on this Mac.
    let projects: [URL]

    @State private var name = ""
    @State private var size = ""
    @State private var region = ""
    @State private var minReady = 1
    @State private var maxSize = 2
    @State private var replaceAfter: Double = 24 * 3600
    @State private var runs: Runs = .always
    @State private var from = WarmPoolSheet.time(8, 0)
    @State private var to = WarmPoolSheet.time(19, 0)
    @State private var sending: Set<String> = []
    @State private var sizes: [String] = []
    @State private var costs: [String: String] = [:]
    @State private var regions: [String] = []
    @State private var loading = true
    @State private var working = false
    @State private var problem: String?

    /// When a pool runs: all the time, or in working hours on every day or on weekdays.
    enum Runs: String, CaseIterable, Identifiable {
        case always = "All the time"
        case everyDay = "Every day"
        case weekdays = "Weekdays"
        var id: String { rawValue }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(pool == nil ? "New Warm Pool on \(provider.name)" : "Edit Warm Pool").appText(.title).fontWeight(.semibold)
            Form {
                TextField("Name", text: $name, prompt: Text("dev-large"))
                if loading {
                    ProgressView().controlSize(.small)
                } else {
                    if sizes.isEmpty {
                        TextField("Size", text: $size)
                    } else {
                        Picker("Size", selection: $size) {
                            ForEach(sizes, id: \.self) { choice in
                                Text(costs[choice].map { "\(choice) — \($0)" } ?? choice).tag(choice)
                            }
                        }
                    }
                    if regions.isEmpty {
                        TextField("Region", text: $region, prompt: Text("the provider’s default"))
                    } else {
                        Picker("Region", selection: $region) {
                            ForEach(regions, id: \.self) { Text($0).tag($0) }
                        }
                    }
                }
                Stepper("Min ready: \(minReady)", value: $minReady, in: 0...20)
                Stepper("Max size (ready or setting up): \(maxSize)", value: $maxSize, in: 1...20)
                Picker("Replace a ready machine after", selection: $replaceAfter) {
                    ForEach(WarmPoolWords.replaceChoices, id: \.seconds) { Text($0.label).tag($0.seconds) }
                }
                Picker("Runs", selection: $runs) {
                    ForEach(Runs.allCases) { Text($0.rawValue).tag($0) }
                }
                if runs != .always {
                    DatePicker("From", selection: $from, displayedComponents: .hourAndMinute)
                    DatePicker("To", selection: $to, displayedComponents: .hourAndMinute)
                    Text("In \(TimeZone.current.identifier). Outside these hours its ready machines are deleted and none are made.")
                        .appText(.fine).foregroundStyle(.secondary)
                }
                Section {
                    if projects.isEmpty {
                        Text("No projects on this Mac.").foregroundStyle(.secondary)
                    }
                    ForEach(projects, id: \.self) { project in
                        Toggle(project.lastPathComponent, isOn: Binding(
                            get: { sending.contains(project.path) },
                            set: { on in if on { sending.insert(project.path) } else { sending.remove(project.path) } }))
                    }
                } header: {
                    Text("Projects to send")
                } footer: {
                    Text("At their last commit, then each one’s .agents/setup.sh. A claim adds the rest.")
                        .appText(.fine).foregroundStyle(.secondary)
                }
            }
            .formStyle(.grouped)
            .disabled(working)
            VStack(alignment: .leading, spacing: 6) {
                // An empty name only keeps Save off: it is not said in red before anything is typed.
                if let problem = problem ?? WarmPoolWords.formProblem(name: name.isEmpty ? "-" : name, minReady: minReady,
                                                                      maxSize: maxSize) {
                    Text(problem).appText(.supporting).tinted(.failure).fixedSize(horizontal: false, vertical: true)
                }
                Text(WarmPoolWords.formCost(minReady: minReady, maxSize: maxSize, hourly: WarmPoolWords.hourly(costs[size])))
                    .appText(.supporting).foregroundStyle(.secondary)
            }
            HStack {
                Spacer()
                if working { ProgressView().controlSize(.small) }
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") { save() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(working || loading || WarmPoolWords.formProblem(name: name, minReady: minReady, maxSize: maxSize) != nil)
            }
        }
        .padding(28)
        .frame(width: 520)
        .task { await load() }
    }

    /// Reads the provider's sizes and regions, and puts the form on the pool or on what New
    /// Machine would make.
    private func load() async {
        defer { loading = false }
        if let pool {
            name = pool.name
            minReady = pool.minReady
            maxSize = pool.maxSize
            replaceAfter = WarmPoolWords.replaceChoices.map(\.seconds).min { abs($0 - (pool.replaceAfter ?? 86400)) < abs($1 - (pool.replaceAfter ?? 86400)) } ?? 86400
            sending = Set(pool.projects ?? [])
            if let hours = pool.hours {
                runs = Set(hours.days) == Set(1...5) ? .weekdays : .everyDay
                from = Self.time(hours.from) ?? from
                to = Self.time(hours.to) ?? to
            }
        }
        switch await control.providerOptions(provider.id) {
        case .success(let options):
            let fields = NewMachineSheet.Field.read(options.schema)
            let values = options.values?.objectValue ?? [:]
            if let field = fields.first(where: { $0.id.lowercased() == "size" }) ?? fields.first(where: { $0.kind == "enum" }) {
                sizes = field.choices
                costs = field.costs
                size = pool?.size ?? values[field.id]?.stringValue ?? field.choices.first ?? ""
            } else {
                size = pool?.size ?? ""
            }
            if let field = fields.first(where: { $0.id.lowercased() == "region" }) {
                regions = field.choices
                region = pool?.region ?? values[field.id]?.stringValue ?? field.choices.first ?? ""
            } else {
                region = pool?.region ?? ""
            }
        case .failure(let refusal):
            problem = refusal.message
            size = pool?.size ?? ""
            region = pool?.region ?? ""
        }
    }

    private func save() {
        let days: [Int]? = switch runs {
        case .always: nil
        case .everyDay: Array(0...6)
        case .weekdays: Array(1...5)
        }
        let hours = days.map {
            DaemonAPI.WarmPoolHours(days: $0, from: Self.hhmm(from), to: Self.hhmm(to), timezone: TimeZone.current.identifier)
        }
        let set = DaemonAPI.WarmPoolSet(id: pool?.id, name: name.trimmingCharacters(in: .whitespaces), scope: .user,
                                        provider: provider.id, size: size.isEmpty ? nil : size,
                                        region: region.isEmpty ? nil : region, runtimes: pool?.runtimes,
                                        projects: projects.map(\.path).filter(sending.contains) + (pool?.projects ?? []).filter { path in
                                            sending.contains(path) && !projects.contains { $0.path == path }
                                        },
                                        minReady: minReady, maxSize: maxSize, replaceAfter: replaceAfter, hours: hours)
        working = true
        problem = nil
        Task {
            let refused = await control.setPool(set)
            working = false
            if let refused { problem = refused } else { dismiss() }
        }
    }

    private static func time(_ hour: Int, _ minute: Int) -> Date {
        Calendar.current.date(bySettingHour: hour, minute: minute, second: 0, of: Date()) ?? Date()
    }

    private static func time(_ hhmm: String) -> Date? {
        let parts = hhmm.split(separator: ":").compactMap { Int($0) }
        guard parts.count == 2 else { return nil }
        return time(parts[0], parts[1])
    }

    private static func hhmm(_ date: Date) -> String {
        let parts = Calendar.current.dateComponents([.hour, .minute], from: date)
        return String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
    }
}
