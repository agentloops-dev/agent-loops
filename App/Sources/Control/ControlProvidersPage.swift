import AgentsKitCore
import SwiftUI

/// Settings ▸ Machine Providers (#691, layouts.md L3): each machine provider as a
/// card of its workspaces, which are hosts already or can be added as one. A machine of the
/// person's carries each provider: it runs its server and keeps its secrets (#3398, #3496).
struct ControlProvidersPage: View {
    let control: ControlSettingsModel
    @State private var adding = false
    @State private var removing: DaemonAPI.ProviderSummary?
    @State private var addingHost: ControlAddServerSheet.Prefill?
    /// New Machine… (US5, L9; was New Machine…).
    @State private var making: DaemonAPI.ProviderSummary?
    /// Edit… (#1932): a preset's name, and its token or keys again.
    @State private var editing: DaemonAPI.ProviderSummary?
    /// Start a Machine from a Repository… (#1809).
    @State private var startingMachine = false
    /// Link to <machine>: its host key, to trust first (#3379).
    @State private var trusting: LinkTrust?
    /// Delete… on a failed make's row (#6078).
    @State private var deletingFailed: FailedRow?
    /// The machines New Droplet made from this page, until each is ready (#6577, W5).
    @State private var madeByDroplet: Set<String> = []
    /// Reconnect… on an OAuth provider's card (#6074, W6).
    @State private var reconnecting: DaemonAPI.ProviderSummary?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let off = control.providersOff {
                Text(off)
                    .appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                ForEach(control.providers) { provider in
                    providerCard(provider)
                    // A provider that makes machines can keep some ready (#5366).
                    if control.poolsHere, provider.capabilities.contains("create") {
                        WarmPoolsSection(control: control, provider: provider)
                    }
                }
                HStack(spacing: 8) {
                    // Only an admin adds one (#1761).
                    if control.isAdmin {
                        Button("Add Provider…") { adding = true }.buttonStyle(.paper)
                    }
                    // A machine of its own for a repository (#1809), from a provider that makes them.
                    Button(MachineFromRepository.Words.menuItem) { startingMachine = true }
                        .buttonStyle(.paper)
                        .disabled(!control.providers.contains { $0.capabilities.contains("create") && $0.usable })
                }
                Text("A provider starts and stops your machines: Docker containers, DigitalOcean Droplets, GitHub Codespaces, Coder workspaces, Google Cloud Workstations and Compute Engine VMs, AWS EC2 instances, or anything with a command. One of your machines carries it: it runs there and keeps its tokens, and the control plane keeps none.")
                    .appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        // Lists every provider again when the page opens (FR-004).
        .task { await control.listMachinesAgain() }
        // The warm pools, read again every 30 s while the page is open: their machines set
        // themselves up and are claimed (#5366). The section says when it last read them.
        .task {
            while !Task.isCancelled {
                await control.loadPools()
                try? await Task.sleep(for: .seconds(30))
            }
        }
        .sheet(isPresented: $adding) { AddProviderSheet(control: control).paperSheet() }
        .sheet(isPresented: $startingMachine) {
            MachineFromRepositorySheet(calls: control.machineCalls, close: { startingMachine = false },
                                       sentence: ControlSettingsModel.refusal)
                .paperSheet()
        }
        .sheet(item: $editing) { provider in EditProviderSheet(control: control, provider: provider).paperSheet() }
        .sheet(item: $reconnecting) { provider in
            DigitalOceanConnect(control: control, reconnecting: provider, cancel: { reconnecting = nil }) { _ in reconnecting = nil }
                .padding(28).frame(width: 560).paperSheet()
        }
        .sheet(item: $addingHost) { prefill in ControlAddServerSheet(control: control, prefill: prefill).paperSheet() }
        // The same sheet as Settings ▸ Machines' Add a Machine…, at this provider (#2131).
        .sheet(item: $making) { provider in
            AddMachineSheet(control: control, start: .provider(provider.id)).paperSheet()
        }
        .confirmationDialog(removing.map { "Remove \($0.name)?" } ?? "",
                            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
                            titleVisibility: .visible, presenting: removing) { provider in
            Button("Remove", role: .destructive) { Task { await control.removeProvider(provider.id) } }
            Button("Cancel", role: .cancel) {}
        } message: { provider in
            Text(DigitalOceanConnectWords.isOAuth(provider) ? DigitalOceanConnectWords.removeNote
                 : "Its machines stay as they are, unlinked: nothing here can start or stop them any more.")
        }
        .confirmationDialog(deletingFailed.map { "Delete \($0.name)?" } ?? "",
                            isPresented: Binding(get: { deletingFailed != nil }, set: { if !$0 { deletingFailed = nil } }),
                            titleVisibility: .visible, presenting: deletingFailed) { row in
            Button("Delete", role: .destructive) {
                Task { await control.deleteFailedMake(row.provider, workspace: row.workspace) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { row in
            Text(Self.deleteFailedMessage(row.providerName))
        }
        .alert("Trust \(trusting?.name ?? "")’s host key?",
               isPresented: Binding(get: { trusting != nil }, set: { if !$0 { trusting = nil } }),
               presenting: trusting) { trust in
            Button("Trust and Link") {
                Task { await control.link(trust.host, to: trust.provider, workspace: trust.workspace, trust: trust.fingerprint) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { trust in
            Text(LinkWorkspaceSheet.trustNote(trust.fingerprint))
        }
    }

    /// A failed make's row to delete (#6078).
    struct FailedRow: Identifiable {
        var provider: ProviderID
        var providerName: String
        var workspace: String
        var name: String
        var id: String { workspace }
    }

    /// Delete's question on a failed make's row, as the web page's.
    static func deleteFailedMessage(_ provider: String) -> String {
        "Only its row goes: \(provider) never made it, so nothing is deleted there."
    }

    /// A link that waits on a host key to trust (#3379).
    struct LinkTrust: Identifiable {
        var host: MachineRef
        var name: String
        var provider: ProviderID
        var workspace: String
        var fingerprint: String
        var id: String { fingerprint }
    }

    private func link(_ host: DaemonAPI.ControlMachine, to provider: ProviderID, workspace: String) {
        Task {
            if let fingerprint = await control.link(host.id, to: provider, workspace: workspace) {
                trusting = LinkTrust(host: host.id, name: host.name, provider: provider, workspace: workspace, fingerprint: fingerprint)
            }
        }
    }

    private func providerCard(_ provider: DaemonAPI.ProviderSummary) -> some View {
        ControlCard {
            ControlRow(dot: .none, title: provider.name, chip: kindChip(provider), detail: providerLine(provider)) {
                HStack(spacing: 8) {
                // A DigitalOcean card has New Droplet and Change… below in its place (#6577).
                if provider.capabilities.contains("create"), !OneClickMake.offers(provider) {
                    Button("New Machine…") { making = provider }.buttonStyle(.paper)
                        .disabled(!provider.usable)
                        .help(provider.usable ? "" : GroupWords.mayNotUse(provider, groups: control.groups))
                }
                // W6 (#6074): connect again with the browser, on the machine that keeps it.
                if control.isAdmin, DigitalOceanConnectWords.isOAuth(provider) {
                    Button(DigitalOceanConnectWords.reconnect) { reconnecting = provider }.buttonStyle(.paper)
                }
                Menu {
                    // Who may start machines from it (#1761): an admin gives it to a group.
                    if control.isAdmin {
                        ProviderGroupPicker(provider: provider, groups: control.groups) { group in
                            Task { await control.setGroup(provider.id, group) }
                        }
                        .pickerStyle(.menu)
                        Divider()
                    }
                    // A preset's name, and an API one's token or keys, as on the web page (#1932).
                    if control.isAdmin, provider.preset != nil {
                        Button("Edit…") { editing = provider }
                    }
                    // The provider New Machine uses (#3979).
                    if provider.capabilities.contains("create"), provider.usable, provider.default != true {
                        Button("Make Default") { Task { await control.makeDefault(provider.id) } }
                    }
                    Button("List Again") { Task { await control.listMachinesAgain(provider.id) } }
                    if control.isAdmin {
                        Divider()
                        Button("Remove…") { removing = provider }
                    }
                } label: {
                    Image(icon: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .accessibilityLabel("More for \(provider.name)")
                }
                .fixedSize()
            }
            if OneClickMake.offers(provider) {
                Divider()
                NewDropletRow(control: control, provider: provider, change: { making = provider }, made: $madeByDroplet)
            }
            ForEach(provider.workspaces) { workspace in
                Divider()
                ControlRow(dot: dot(workspace), title: workspace.name, detail: workspaceLine(workspace)) {
                    if control.isJoining(provider.id, workspace) || (workspace.host == nil && workspace.joining == true) {
                        // Made here, installing on its first boot (US5).
                        HStack(spacing: 6) {
                            ProgressView().controlSize(.small)
                            Text("Joining…").appText(.supporting).foregroundStyle(.secondary)
                        }
                    } else if workspace.state == "failed" {
                        // A make that failed is no machine (#6078): its row offers only Delete.
                        Button("Delete…") {
                            deletingFailed = .init(provider: provider.id, providerName: provider.name,
                                                   workspace: workspace.id, name: workspace.name)
                        }
                        .buttonStyle(.paper)
                    } else if workspace.host == nil {
                        HStack(spacing: 8) {
                            linkToExistingHost(workspace, of: provider)
                            Button("Add as Machine…") {
                                addingHost = .init(destination: workspace.ssh ?? "", name: workspace.name,
                                                   tunnel: workspace.tunnel ?? false, provider: provider.id,
                                                   workspace: workspace.id)
                            }
                            .buttonStyle(.paper)
                            .disabled(workspace.ssh == nil || workspace.state != "running")
                            .help(workspace.ssh == nil ? "\(provider.name) gives no ssh destination for it: add it by command, then use Link to Provider… on it."
                                  : workspace.state == "running" ? "Installs Agent Loops on it over ssh, and links the machine to it."
                                  : "Start it first: it has to be running to be installed.")
                        }
                        .fixedSize()
                    }
                }
                // A make's steps on its row, from the press until it joins (#6577, W4).
                if workspace.host == nil, let setup = workspace.setup {
                    ProviderSetupSteps(setup: setup)
                }
            }
        }
    }

    /// Link to existing host (#976): a host added by hand under the workspace's ssh alias,
    /// such as `cws.devstack-d7539e59`, is linked to it rather than installed again.
    @ViewBuilder
    private func linkToExistingHost(_ workspace: DaemonAPI.MachineInfo, of provider: DaemonAPI.ProviderSummary) -> some View {
        let hosts = control.hostsThatMayBe(workspace)
        if hosts.count == 1, let host = hosts.first {
            Button("Link to \(host.name)") { link(host, to: provider.id, workspace: workspace.id) }
            .buttonStyle(.paper)
            .help("\(host.name) may already be this machine: links it, without installing again.")
        } else if !hosts.isEmpty {
            // The one machine picker (#5621).
            let byID = Dictionary(hosts.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            MachinePicker(label: "Link to Existing Machine", value: nil, placeholder: "Link to Existing Machine",
                          keep: { byID[$0.id] != nil }) { picked in
                if let host = picked.flatMap({ byID[$0] }) { link(host, to: provider.id, workspace: workspace.id) }
            }
            .fixedSize()
            .help("One of these machines may already be this one: links it, without installing again.")
        }
    }

    /// Its kind, after "default" on the one New Machine uses (#3979).
    private func kindChip(_ provider: DaemonAPI.ProviderSummary) -> String {
        // W6 (#6074): an OAuth connection says whether it still works.
        if let oauth = provider.oauth, DigitalOceanConnectWords.isOAuth(provider) {
            let words = oauth.disconnected ? DigitalOceanConnectWords.disconnectedChip : DigitalOceanConnectWords.connectedChip
            return provider.default == true ? "default · " + words : words
        }
        return provider.default == true ? "default · " + kindWords(provider) : kindWords(provider)
    }

    private func kindWords(_ provider: DaemonAPI.ProviderSummary) -> String {
        // An API preset needs only a token; a CLI one, its CLI signed in (#1119).
        if let preset = provider.preset {
            return "command provider · \(preset) · \(AddProviderSheet.Kind.byAPI(preset) ? "API" : "CLI")"
        }
        return "command provider"
    }

    private func providerLine(_ provider: DaemonAPI.ProviderSummary) -> String {
        var parts: [String] = []
        if let oauth = provider.oauth, DigitalOceanConnectWords.isOAuth(provider) {
            if oauth.disconnected { parts.append(DigitalOceanConnectWords.disconnected) }
            if let account = DigitalOceanConnectWords.accountLine(oauth) { parts.append(account) }
        }
        if let problem = provider.problem, !provider.current || provider.listedAt == nil, provider.oauth?.disconnected != true {
            parts.append("Couldn’t list: \(problem)")
        }
        if let at = provider.listedAt {
            parts.append("\(provider.current ? "Listed" : "Showing what it listed at") \(at.formatted(date: .omitted, time: .shortened))")
        }
        if provider.workspaces.isEmpty, provider.listedAt != nil { parts.append("No machines") }
        if let group = GroupWords.providerLine(provider, groups: control.groups) { parts.append(group) }
        if let tool = provider.sshToolMissing { parts.append("needs \(tool) for ssh") }
        // The machine that carries it (#3496), and when it is offline.
        if let offline = ProviderCarrier.offline(provider) {
            parts.append(offline)
        } else if let carried = ProviderCarrier.carriedBy(provider) {
            parts.append(carried)
        } else if control.isAdmin, provider.preset != nil {
            parts.append(ProviderCarrier.notCarried)
        }
        // Commands written for it that give no ssh (#975); a preset's own way in is its own.
        if provider.preset == nil, !provider.workspaces.isEmpty, provider.workspaces.allSatisfy({ $0.ssh == nil }) {
            parts.append("⚠ No ssh destination: Add as Machine… and starting agentsd over ssh won’t work for these machines. Add one by hand: `user@{field}`.")
        }
        return parts.joined(separator: " · ")
    }

    private func dot(_ workspace: DaemonAPI.MachineInfo) -> ControlDot {
        switch workspace.state {
        case "running": .online
        case "unknown": .attention
        default: .offline
        }
    }

    private func workspaceLine(_ workspace: DaemonAPI.MachineInfo) -> String {
        var parts = [workspace.state.capitalized]
        if let host = workspace.host {
            parts.append("linked to \(control.hosts.first { $0.id == host }?.name ?? "a machine")")
        } else if workspace.state == "failed", let reason = workspace.reason, !reason.isEmpty {
            // A make that failed says why (#4706), in place of "not joined".
            parts.append(reason)
        } else {
            parts.append("not joined")
        }
        if let cost = workspace.cost { parts.append(cost) }
        return parts.joined(separator: " · ")
    }
}

/// Add a Provider (#691, layouts.md L4, as Alex chose it): pick what kind first, then a short
/// form for just that kind, and the machine that carries it (#3496). A preset needs its CLI
/// signed in there. The MCP server kind and Write It for Me were removed (#6623).
struct AddProviderSheet: View {
    @Environment(\.dismiss) private var dismiss
    let control: ControlSettingsModel
    /// Inside Add a Machine (#2131): the provider added, to go on with, in place of closing.
    var onAdded: ((ProviderID?) -> Void)?

    enum Kind: String, CaseIterable, Identifiable {
        case docker, digitalOcean, codespaces, coder, workstations, compute, aws, commands
        var id: String { rawValue }

        var title: String {
            switch self {
            case .docker: "Docker Containers"
            case .digitalOcean: "DigitalOcean Droplets"
            case .codespaces: "GitHub Codespaces"
            case .coder: "Coder Workspaces"
            case .workstations: "Google Cloud Workstations"
            case .compute: "Google Compute Engine VMs"
            case .aws: "AWS EC2 Instances"
            case .commands: "Your commands"
            }
        }

        var symbol: String {
            switch self {
            case .docker: "shippingbox"
            case .digitalOcean: "drop"
            case .codespaces: "chevron.left.forwardslash.chevron.right"
            case .coder: "terminal"
            case .workstations: "cloud"
            case .compute: "cpu"
            case .aws: "server.rack"
            case .commands: "apple.terminal"
            }
        }

        /// The list's groups, in order: a person looks first for the place their machines are
        /// (#6069). The same words as the web page's providerGroups.
        enum Group: String, CaseIterable, Identifiable {
            case cloud = "Cloud"
            case development = "Development environments"
            case yourMachine = "On a machine you have"
            case custom = "Custom"
            var id: String { rawValue }

            var kinds: [Kind] {
                switch self {
                case .cloud: [.digitalOcean, .compute, .aws]
                case .development: [.codespaces, .coder, .workstations]
                case .yourMachine: [.docker]
                case .custom: [.commands]
                }
            }
        }

        /// The row's one line (#6069), as the web page's choiceDescription.
        var summary: String {
            switch self {
            case .digitalOcean: DigitalOceanConnectWords.rowSummary
            case .compute: "VMs with a label, in one zone."
            case .aws: "Instances with a tag, in one region."
            case .codespaces: "Your codespaces, or the codespaces of one repository."
            case .coder: "Workspaces on your Coder deployment."
            case .workstations: "Workstations in one cluster."
            case .docker: "Containers on a machine that has docker."
            case .commands: "Your own commands to list, start and stop machines."
            }
        }

        /// How to set up: its part of the docs, as the web page's choiceDocs.
        var docs: URL {
            let page = switch self {
            case .digitalOcean: "set-up-a-cloud-machine-provider/#digitalocean-droplets"
            case .compute: "set-up-a-cloud-machine-provider/#google-compute-engine-vms"
            case .aws: "set-up-a-cloud-machine-provider/#aws-ec2-instances"
            case .codespaces: "set-up-a-cloud-machine-provider/#github-codespaces"
            case .coder: "set-up-a-cloud-machine-provider/#coder-machines"
            case .workstations: "set-up-a-cloud-machine-provider/#google-cloud-workstations"
            case .docker, .commands: "start-machines-from-agents/#add-a-provider"
            }
            return URL(string: "how-to/\(page)", relativeTo: HelpSite.url)!.absoluteURL
        }

        /// What has no field of its own, through its CLI: one short line under the header (#6069).
        var needs: String {
            switch self {
            case .docker: "Needs docker on the machine that carries it."
            case .digitalOcean: "Needs doctl, signed in, on the machine that carries it."
            case .codespaces: "Needs gh, signed in, on the machine that carries it."
            case .coder: "Needs coder, signed in, and `coder config-ssh` run, on the machine that carries it."
            case .workstations: "Needs gcloud, signed in, on the machine that carries it."
            case .compute, .aws: "Nothing to install."
            case .commands: "The commands run on the machine that carries it, as you."
            }
        }

        /// Whether it can be reached through its API with a token, not only its CLI (#1119).
        var hasAPI: Bool { self == .digitalOcean || self == .codespaces || self == .coder }

        /// Whether a preset is an API one.
        static func byAPI(_ preset: String) -> Bool { preset.hasSuffix("-api") }

        /// The API preset's name and the secret its token goes in.
        var api: (preset: String, secret: String)? {
            switch self {
            case .digitalOcean: ("digitalocean-api", "DIGITALOCEAN_API_TOKEN")
            case .codespaces: ("codespaces-api", "GITHUB_TOKEN")
            case .coder: ("coder-api", "CODER_SESSION_TOKEN")
            default: nil
            }
        }

        /// Through its API, what has no field of its own (#6069), as the web page's kindNote.
        var apiNeeds: String? {
            switch self {
            case .digitalOcean: "Nothing to install."
            case .codespaces: "ssh still goes through gh on the machine that carries it."
            case .coder: "ssh still goes through coder on the machine that carries it."
            default: nil
            }
        }

        /// The help under the API token's field: the smallest scope that does the work.
        var tokenHelp: String? {
            switch self {
            case .digitalOcean: "Custom scopes: droplet read and update, and droplet create and delete to make machines."
            case .codespaces: "Fine-grained, with Codespaces lifecycle admin (read and write), or classic, with the codespace scope."
            case .coder: "Make one with `coder tokens create`."
            default: nil
            }
        }
    }

    @State private var kind: Kind?
    @State private var name = ""
    // Presets
    @State private var containers = ""
    @State private var image = ""
    @State private var ssh = ""
    @State private var tag = "agents"
    @State private var user = "root"
    @State private var repo = ""
    @State private var cluster = ""
    @State private var config = ""
    @State private var region = ""
    // Through the API (#1119): the default for a kind that has one.
    @State private var byCLI = false
    @State private var token = ""
    @State private var deployment = ""
    @State private var accessKey = ""
    @State private var secretKey = ""
    @State private var folder = ""
    @State private var sshKey = ""
    @State private var serviceKey = ""
    @State private var serviceAccount = ""
    @State private var choosingKey = false
    // Your commands
    @State private var workspace = ""
    @State private var start = ""
    @State private var stop = ""
    @State private var status = ""
    // Secrets: entered once, never shown again.
    @State private var secrets: [Secret] = []
    @State private var showsSecrets = false
    @State private var working = false
    @State private var problem: String?
    /// The machine that carries it (#3496).
    @State private var carrier: MachineRef?

    struct Secret: Identifiable {
        let id = UUID()
        var name = ""
        var value = ""
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(title).appText(.title).fontWeight(.semibold)
            if kind == .digitalOcean {
                // Guided (#6074): Connect with OAuth, or a token behind a disclosure.
                DigitalOceanConnect(control: control, back: { kind = nil }, cancel: { dismiss() }) { added in
                    if let onAdded { onAdded(added) } else { dismiss() }
                }
            } else if let kind {
                form(kind)
            } else {
                grid
            }
        }
        .padding(28)
        .frame(width: 560)
        .task { carrier = ProviderCarrier.start(nil, choices: control.carriers) }
    }

    private var title: String { kind.map { "Add \($0.title)" } ?? "Add a Provider" }

    // MARK: Pick a kind

    /// Step 1 (#6069): a grouped list, a row per kind with its line, as Internet Accounts and the
    /// "add a data source" pages draw it. A click opens its form.
    private var grid: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("A provider starts and stops the machines that agents run on.")
                .appText(.supporting).foregroundStyle(.secondary)
            Form {
                ForEach(Kind.Group.allCases) { group in
                    Section(group.rawValue) {
                        ForEach(group.kinds) { each in
                            Button { choose(each) } label: { kindRow(each) }
                                .buttonStyle(.plain)
                                .accessibilityLabel(each.title)
                                .accessibilityHint(each.summary)
                        }
                    }
                }
            }
            .formStyle(.grouped)
            // All four groups at once where the screen has room; it scrolls where it has not.
            .frame(minHeight: 420, idealHeight: 760)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            }
        }
    }

    private func kindRow(_ each: Kind) -> some View {
        HStack(spacing: 10) {
            // Decorative: the kind's name is beside it.
            Image(icon: each.symbol).font(.system(size: 17)).foregroundStyle(.secondary)
                .frame(width: 24).accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 1) {
                Text(each.title)
                Text(each.summary).appText(.supporting).foregroundStyle(.secondary)
            }
            Spacer()
            // W1 (#6074): the guided kind says so.
            if each == .digitalOcean {
                Text(DigitalOceanConnectWords.guidedChip).appText(.fine).foregroundStyle(.secondary)
                    .padding(.horizontal, 6).padding(.vertical, 1)
                    .overlay(Capsule().strokeBorder(.secondary.opacity(0.5)))
            }
            Image(icon: "chevron.right").foregroundStyle(.tertiary).accessibilityHidden(true)
        }
        .contentShape(Rectangle())
    }

    /// Step 2's header: the kind again, its one line and what has no field, and How to set up.
    private func kindHeader(_ kind: Kind) -> some View {
        HStack(alignment: .top, spacing: 10) {
            // Decorative: the kind's name is beside it.
            Image(icon: kind.symbol).font(.system(size: 17)).foregroundStyle(.secondary)
                .frame(width: 24).accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(kind.title).fontWeight(.semibold)
                Text("\(kind.summary) \(kind.hasAPI && !byCLI ? kind.apiNeeds ?? "" : kind.needs)")
                    .appText(.supporting).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
            Link("How to set up", destination: kind.docs).appText(.supporting)
        }
    }

    /// A field with its help under it (#6069), not one paragraph of needs under the form.
    private func field<Control: View>(_ title: String, help: String? = nil, @ViewBuilder _ control: () -> Control) -> some View {
        LabeledContent(title) {
            VStack(alignment: .leading, spacing: 3) {
                control().labelsHidden()
                if let help {
                    Text(help).appText(.fine).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private func choose(_ each: Kind) {
        kind = each
        problem = nil
        byCLI = false
        user = switch each {
        case .aws: "ec2-user"
        case .compute: "agents"
        default: "root"
        }
        if each == .compute { region = "" }
        name = switch each {
        case .docker: "Docker Containers on this Mac"
        case .commands: ""
        default: each.title
        }
    }

    // MARK: Its form

    @ViewBuilder
    private func form(_ kind: Kind) -> some View {
        kindHeader(kind)
        Form {
            if kind.hasAPI {
                Section { reachPicker }
            }
            Section("Provider") {
                TextField("Name", text: $name)
                CarrierPicker(control: control, carrier: $carrier)
            }
            switch kind {
            case .docker:
                Section("Machines") {
                    field("Containers", help: "Empty: the containers labelled agents.workspace.") {
                        TextField("Containers", text: $containers, prompt: Text("agents-devbox, other"))
                    }
                    TextField("ssh", text: $ssh, prompt: Text("alex@127.0.0.1 -p 2222"))
                    field("Image", help: "For New Machine… and Delete Machine…: an image that keeps running and has curl. Empty: it only lists.") {
                        TextField("Image", text: $image, prompt: Text("agents-devbox:latest"))
                            .disabled(!trimmed(containers).isEmpty)
                    }
                }
            case .digitalOcean:
                if !byCLI {
                    Section("Access") {
                        field("API token", help: kind.tokenHelp) { SecureField("API token", text: $token, prompt: Text("Required")) }
                    }
                }
                Section("Machines") {
                    TextField("Droplets tagged", text: $tag)
                    TextField("ssh as", text: $user)
                }
            case .codespaces:
                if !byCLI {
                    Section("Access") {
                        field("Token", help: kind.tokenHelp) { SecureField("Token", text: $token, prompt: Text("Required")) }
                    }
                }
                Section("Machines") {
                    field("Repository", help: "Empty: every codespace of yours.") {
                        TextField("Repository", text: $repo, prompt: Text("owner/repo"))
                    }
                }
            case .coder:
                if !byCLI {
                    Section("Access") {
                        TextField("Deployment", text: $deployment, prompt: Text(verbatim: "https://coder.example.com"))
                        field("Session token", help: kind.tokenHelp) { SecureField("Session token", text: $token, prompt: Text("Required")) }
                    }
                }
            case .workstations:
                Section("Machines") {
                    TextField("Cluster", text: $cluster)
                    TextField("Configuration", text: $config)
                    TextField("Region", text: $region)
                    TextField("Project", text: $folder, prompt: Text("gcloud’s default"))
                    field("ssh", help: "A host in your ssh config that reaches the workstation’s port 22 through `gcloud workstations start-tcp-tunnel`.") {
                        TextField("ssh", text: $ssh, prompt: Text("A host in your ssh config"))
                    }
                }
            case .compute:
                Section("Access") {
                    field("Service account key", help: "A role with compute.instances list, start and stop; New Machine… needs more (How to set up).") {
                        HStack {
                            Text(serviceAccount.isEmpty ? "None" : serviceAccount).foregroundStyle(.secondary)
                            Button("Choose Key File…") { choosingKey = true }
                        }
                    }
                }
                Section("Machines") {
                    TextField("Project", text: $folder)
                    TextField("Zone", text: $region, prompt: Text("us-west1-b"))
                    TextField("VMs labelled", text: $tag)
                    field("ssh as", help: "ssh goes to each VM’s external address: the network must let in port 22.") {
                        TextField("ssh as", text: $user)
                    }
                    TextField("ssh key for new VMs", text: $sshKey, prompt: Text(verbatim: "ssh-ed25519 AAAA…"))
                }
            case .aws:
                Section("Access") {
                    field("Access key ID", help: "An IAM user’s key. Its policy: ec2:DescribeInstances, and ec2:StartInstances and ec2:StopInstances on tagged instances only (How to set up).") {
                        TextField("Access key ID", text: $accessKey, prompt: Text("Required"))
                    }
                    SecureField("Secret access key", text: $secretKey, prompt: Text("Required"))
                }
                Section("Machines") {
                    TextField("Region", text: $region, prompt: Text("us-east-1"))
                    TextField("Instances tagged", text: $tag)
                    TextField("ssh as", text: $user)
                }
            case .commands:
                commandsForm
            }
            if !(kind.hasAPI && !byCLI), kind != .aws, kind != .compute { secretsSection }
        }
        .formStyle(.grouped)
        .fileImporter(isPresented: $choosingKey, allowedContentTypes: [.json]) { result in
            guard case .success(let file) = result else { return }
            readServiceKey(file)
        }
        if let problem {
            SheetNote(text: problem, reservesSpace: false)
        }
        // One foot (#6069): where the secrets go, in fine print, then Back, Cancel and Add.
        Text(ProviderCarrier.secretsFoot(control.carriers.first { $0.id == carrier }?.name))
            .appText(.fine).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        HStack {
            Button("Back") {
                self.kind = nil
                problem = nil
            }
            .disabled(working)
            Spacer()
            Button("Cancel") { dismiss() }
                .keyboardShortcut(.cancelAction)
            Button { add(kind) } label: {
                if working {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text("Checking…")
                    }
                } else {
                    Text("Add")
                }
            }
            .keyboardShortcut(.defaultAction)
            .disabled(working || name.trimmingCharacters(in: .whitespaces).isEmpty || carrier == nil
                      || (kind.hasAPI && !byCLI && token.isEmpty)
                      || (kind == .aws && (trimmed(accessKey).isEmpty || secretKey.isEmpty || trimmed(region).isEmpty))
                      || (kind == .compute && (serviceKey.isEmpty || trimmed(folder).isEmpty || trimmed(region).isEmpty)))
        }
    }

    /// A Google service account's JSON key, read once: only its email is shown.
    private func readServiceKey(_ file: URL) {
        let opened = file.startAccessingSecurityScopedResource()
        defer { if opened { file.stopAccessingSecurityScopedResource() } }
        guard let data = try? Data(contentsOf: file),
              let email = (try? JSONValue.parse(data))?.objectValue?["client_email"]?.stringValue else {
            problem = "That file isn’t a service account’s JSON key."
            return
        }
        serviceKey = String(decoding: data, as: UTF8.self)
        serviceAccount = email
        problem = nil
    }

    /// API or CLI, for a kind that has both: the API first, as it needs nothing installed.
    private var reachPicker: some View {
        Picker("Connect with", selection: $byCLI) {
            Text("Its API").tag(false)
            Text("Its CLI").tag(true)
        }
        .pickerStyle(.segmented)
    }

    private var commandsForm: some View {
        Section("Commands") {
            TextField("Machine", text: $workspace, prompt: Text("devstack"))
            TextField("ssh", text: $ssh, prompt: Text("devstack-1.cws"))
            TextField("Start", text: $start, prompt: Text("cws start devstack-1"))
            TextField("Stop", text: $stop, prompt: Text("cws stop devstack-1"))
            TextField("Status", text: $status, prompt: Text("Optional: prints running or stopped"))
        }
    }

    @ViewBuilder
    private var secretsSection: some View {
        Section {
            // Closed at first (#6069): most kinds need none.
            DisclosureGroup("Secrets (optional)", isExpanded: $showsSecrets) {
                ForEach($secrets) { $secret in
                    HStack {
                        TextField("Name", text: $secret.name, prompt: Text("TOKEN")).frame(maxWidth: 180)
                        SecureField("Value", text: $secret.value)
                        Button {
                            secrets.removeAll { $0.id == secret.id }
                        } label: {
                            Image(icon: "minus.circle")
                        }
                        .buttonStyle(.borderless)
                        .accessibilityLabel("Remove \(secret.name.isEmpty ? "this secret" : secret.name)")
                    }
                }
                Button("Add Secret") { secrets.append(Secret()) }
                Text("Given to the provider as environment. Kept on the machine that carries it, never shown again.")
                    .appText(.fine).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // MARK: Adding

    private func add(_ kind: Kind) {
        let request: DaemonAPI.ProviderAdd
        do {
            request = try self.request(kind)
        } catch {
            problem = "\(error)"
            return
        }
        working = true
        problem = nil
        let before = Set(control.providers.map(\.id))
        Task {
            let refused = await control.addProvider(request)
            working = false
            if let refused {
                problem = refused
            } else {
                if let onAdded {
                    onAdded(control.providers.first { !before.contains($0.id) }?.id)
                } else {
                    dismiss()
                }
            }
        }
    }

    struct Wrong: Error, CustomStringConvertible { var description: String }

    private var givenSecrets: [String: String] {
        Dictionary(secrets.filter { !trimmed($0.name).isEmpty }.map { (trimmed($0.name), $0.value) }) { $1 }
    }

    private func trimmed(_ text: String) -> String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    /// Your commands, as the fields hold them.
    private func commandsJSON() throws -> JSONValue {
        guard !trimmed(workspace).isEmpty, !trimmed(start).isEmpty, !trimmed(stop).isEmpty else {
            throw Wrong(description: "Give the machine a name, and its Start and Stop commands.")
        }
        var entry: [String: JSONValue] = ["name": .string(trimmed(workspace)), "start": .string(trimmed(start)),
                                          "stop": .string(trimmed(stop))]
        if !trimmed(status).isEmpty { entry["status"] = .string(trimmed(status)) }
        if !trimmed(ssh).isEmpty { entry["ssh"] = .string(trimmed(ssh)) }
        return ["workspaces": [.object(entry)]]
    }

    private func request(_ kind: Kind) throws -> DaemonAPI.ProviderAdd {
        var request = try form(for: kind)
        request.machine = carrier
        return request
    }

    private func form(for kind: Kind) throws -> DaemonAPI.ProviderAdd {
        var given = givenSecrets
        let name = trimmed(self.name)
        var commands: [String: JSONValue] = [:]
        if let api = kind.api, !byCLI {
            // The token goes in only as the provider's secret, never in its configuration.
            given = [api.secret: token]
            commands = ["preset": .string(api.preset)]
            switch kind {
            case .digitalOcean:
                commands["tag"] = .string(trimmed(tag))
                commands["user"] = .string(trimmed(user).isEmpty ? "root" : trimmed(user))
            case .codespaces:
                if !trimmed(repo).isEmpty { commands["repo"] = .string(trimmed(repo)) }
            default:
                guard !trimmed(deployment).isEmpty else { throw Wrong(description: "Give the deployment’s address.") }
                commands["url"] = .string(trimmed(deployment))
            }
            return DaemonAPI.ProviderAdd(name: name, kind: .commands, commands: .object(commands), secrets: given)
        }
        switch kind {
        case .docker:
            let names = containers.split(separator: ",").map { trimmed(String($0)) }.filter { !$0.isEmpty }
            commands = ["preset": "docker"]
            if !names.isEmpty { commands["containers"] = .array(names.map(JSONValue.string)) }
            if names.isEmpty, !trimmed(image).isEmpty { commands["image"] = .string(trimmed(image)) }
            if !trimmed(ssh).isEmpty { commands["ssh"] = .string(trimmed(ssh)) }
        case .digitalOcean:
            commands = ["preset": "doctl", "tag": .string(trimmed(tag)), "user": .string(trimmed(user).isEmpty ? "root" : trimmed(user))]
        case .codespaces:
            commands = ["preset": "codespaces"]
            if !trimmed(repo).isEmpty { commands["repo"] = .string(trimmed(repo)) }
        case .coder:
            commands = ["preset": "coder"]
        case .aws:
            // The keys go in only as the provider's secrets, never in its configuration.
            commands = ["preset": "aws-ec2-api", "region": .string(trimmed(region)),
                        "tag": .string(trimmed(tag).isEmpty ? "agents" : trimmed(tag)),
                        "user": .string(trimmed(user).isEmpty ? "ec2-user" : trimmed(user))]
            return DaemonAPI.ProviderAdd(name: name, kind: .commands, commands: .object(commands),
                                         secrets: ["AWS_ACCESS_KEY_ID": trimmed(accessKey), "AWS_SECRET_ACCESS_KEY": secretKey])
        case .compute:
            // The key goes in only as the provider's secret, never in its configuration.
            commands = ["preset": "gcp-compute-api", "project": .string(trimmed(folder)), "zone": .string(trimmed(region)),
                        "label": .string(trimmed(tag).isEmpty ? "agents" : trimmed(tag)),
                        "user": .string(trimmed(user).isEmpty ? "agents" : trimmed(user))]
            if !trimmed(sshKey).isEmpty { commands["sshKeys"] = [.string(trimmed(sshKey))] }
            return DaemonAPI.ProviderAdd(name: name, kind: .commands, commands: .object(commands),
                                         secrets: ["GOOGLE_SERVICE_ACCOUNT_KEY": serviceKey])
        case .workstations:
            commands = ["preset": "gcloud-workstations", "cluster": .string(trimmed(cluster)),
                        "config": .string(trimmed(config)), "region": .string(trimmed(region))]
            if !trimmed(folder).isEmpty { commands["project"] = .string(trimmed(folder)) }
            if !trimmed(ssh).isEmpty { commands["ssh"] = .string(trimmed(ssh)) }
        case .commands:
            return DaemonAPI.ProviderAdd(name: name, kind: .commands, commands: try commandsJSON(), secrets: given)
        }
        return DaemonAPI.ProviderAdd(name: name, kind: .commands, commands: .object(commands), secrets: given)
    }
}

/// Edit… on a preset's card (#1932), as the web page's: its name, and for an API preset its
/// token or keys again, when one ends or is rotated. A secret left empty keeps the one it has;
/// `providers/update` keeps the configuration, so nothing is unlinked.
struct EditProviderSheet: View {
    @Environment(\.dismiss) private var dismiss
    let control: ControlSettingsModel
    let provider: DaemonAPI.ProviderSummary

    @State private var name = ""
    @State private var values: [String: String] = [:]
    @State private var serviceAccount = ""
    @State private var choosingKey = false
    @State private var working = false
    @State private var problem: String?
    /// The machine that carries it (#3496): another one asks for its secrets again.
    @State private var carrier: MachineRef?

    /// A secret the form asks for again: its label, the secret's name, and whether it is hidden.
    struct Field: Hashable {
        var label: String
        var secret: String
        var hidden = true
    }

    /// The secrets a preset reads; none for a CLI preset, which only has its name to edit.
    static func fields(_ preset: String?) -> [Field] {
        switch preset {
        case "digitalocean-api": [Field(label: "API token", secret: "DIGITALOCEAN_API_TOKEN")]
        case "codespaces-api": [Field(label: "Token", secret: "GITHUB_TOKEN")]
        case "coder-api": [Field(label: "Session token", secret: "CODER_SESSION_TOKEN")]
        case "aws-ec2-api": [Field(label: "Access key ID", secret: "AWS_ACCESS_KEY_ID", hidden: false),
                             Field(label: "Secret access key", secret: "AWS_SECRET_ACCESS_KEY")]
        case "gcp-compute-api": [Field(label: "Service account key", secret: "GOOGLE_SERVICE_ACCOUNT_KEY")]
        default: []
        }
    }

    /// What Save sends: the name, only the secrets given, so an empty one is kept, and the
    /// machine to carry it when that is another one.
    static func request(_ provider: DaemonAPI.ProviderSummary, name: String,
                        values: [String: String], carrier: MachineRef? = nil) -> DaemonAPI.ProviderAdd {
        var secrets: [String: String] = [:]
        for field in fields(provider.preset) {
            let value = values[field.secret] ?? ""
            // A key is what it is: only a shown ID is trimmed.
            let given = field.hidden ? value : value.trimmingCharacters(in: .whitespacesAndNewlines)
            if !given.isEmpty { secrets[field.secret] = given }
        }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return DaemonAPI.ProviderAdd(id: provider.id, name: trimmed.isEmpty ? provider.name : trimmed,
                                     kind: .commands,
                                     machine: ProviderCarrier.moves(provider, to: carrier) ? carrier : nil,
                                     secrets: secrets)
    }

    var body: some View {
        let fields = Self.fields(provider.preset)
        let moving = ProviderCarrier.moves(provider, to: carrier)
        let keep = moving ? "Give it again" : "Keep the one it has"
        VStack(alignment: .leading, spacing: 16) {
            Text("Edit \(provider.name)").appText(.title).fontWeight(.semibold)
            Form {
                Section("Provider") {
                    TextField("Name", text: $name)
                    CarrierPicker(control: control, carrier: $carrier, current: provider)
                }
                if !fields.isEmpty {
                    Section("Access") {
                        ForEach(fields, id: \.self) { field in
                            if field.secret == "GOOGLE_SERVICE_ACCOUNT_KEY" {
                                LabeledContent(field.label) {
                                    HStack {
                                        Text(serviceAccount.isEmpty ? keep : serviceAccount).foregroundStyle(.secondary)
                                        Button("Choose Key File…") { choosingKey = true }
                                    }
                                }
                            } else if field.hidden {
                                SecureField(field.label, text: binding(field.secret), prompt: Text(keep))
                            } else {
                                TextField(field.label, text: binding(field.secret), prompt: Text(keep))
                            }
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .fileImporter(isPresented: $choosingKey, allowedContentTypes: [.json]) { result in
                guard case .success(let file) = result else { return }
                readServiceKey(file)
            }
            let to = control.carriers.first { $0.id == carrier }?.name ?? provider.machineName
            Text(fields.isEmpty ? "Its CLI signs in on the machine that carries it."
                 : moving ? "\(ProviderCarrier.movedNote) \(ProviderCarrier.secretsFoot(to)) Its machines stay linked."
                 : "Leave a field empty to keep the one it has. \(ProviderCarrier.secretsFoot(to)) Its machines stay linked.")
                .appText(.supporting).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            SheetNote(text: problem)
            HStack {
                Spacer()
                if working { ProgressView().controlSize(.small) }
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") { save() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(working)
            }
        }
        .padding(28)
        .frame(width: 480)
        .onAppear {
            name = provider.name
            carrier = ProviderCarrier.start(provider, choices: control.carriers)
        }
    }

    private func binding(_ secret: String) -> Binding<String> {
        Binding(get: { values[secret] ?? "" }, set: { values[secret] = $0 })
    }

    /// A Google service account's JSON key, read once: only its email is shown.
    private func readServiceKey(_ file: URL) {
        let opened = file.startAccessingSecurityScopedResource()
        defer { if opened { file.stopAccessingSecurityScopedResource() } }
        guard let data = try? Data(contentsOf: file),
              let email = (try? JSONValue.parse(data))?.objectValue?["client_email"]?.stringValue else {
            problem = "That file isn’t a service account’s JSON key."
            return
        }
        values["GOOGLE_SERVICE_ACCOUNT_KEY"] = String(decoding: data, as: UTF8.self)
        serviceAccount = email
        problem = nil
    }

    private func save() {
        working = true
        problem = nil
        Task {
            let refused = await control.addProvider(Self.request(provider, name: name, values: values, carrier: carrier))
            working = false
            if let refused { problem = refused } else { dismiss() }
        }
    }
}

/// Carried by (#3496): the person's online machines that can carry a provider, this Mac first.
/// With `current`, Edit…'s: the one that carries it now shows, offline or not.
struct CarrierPicker: View {
    let control: ControlSettingsModel
    @Binding var carrier: MachineRef?
    var current: DaemonAPI.ProviderSummary?

    var body: some View {
        let choices = control.carriers
        if choices.isEmpty, current?.machine == nil {
            LabeledContent("Carried by") {
                Text(ProviderCarrier.noneOnline).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        } else {
            Picker("Carried by", selection: $carrier) {
                if let current, let machine = current.machine, !choices.contains(where: { $0.id == machine }) {
                    Text("\(current.machineName ?? "Its machine") (offline)").tag(Optional(machine))
                }
                ForEach(choices) { host in
                    Text(host.id == control.thisMacHost?.id ? "\(host.name) (this Mac)" : host.name).tag(Optional(host.id))
                }
            }
        }
    }
}

/// Link to Machine… (#691, layouts.md L5): a provider's workspace for a host that is already
/// one. A workspace another host has is shown, greyed, as that host's.
struct LinkWorkspaceSheet: View {
    @Environment(\.dismiss) private var dismiss
    let control: ControlSettingsModel
    let host: DaemonAPI.ControlMachine

    @State private var chosen: String?
    @State private var startWhenUsed = true
    @State private var stopWhenIdle = WorkspaceLink.defaultIdleMinutes
    /// The host key to trust first, for a machine that joined with the command (#3379).
    @State private var fingerprint: String?
    @State private var working = false

    /// What a new host key is said with, here and on the providers page.
    static func trustNote(_ fingerprint: String) -> String {
        "This machine’s host key is new to the control plane: \(fingerprint). Link only if this is the key the machine shows (ssh-keygen -lf on its host key): its provider starts it over ssh with this key."
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Link \(host.name) to a Provider’s Machine").appText(.title).fontWeight(.semibold)
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(control.providers) { provider in
                        Text(provider.name).appText(.reading).fontWeight(.semibold)
                        ForEach(provider.workspaces) { workspace in
                            row(provider, workspace)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxHeight: 280)
            Toggle("Start when used", isOn: $startWhenUsed)
            Picker("Stop when idle", selection: $stopWhenIdle) {
                ForEach(MachineProviderWords.idleChoices, id: \.minutes) { Text($0.label).tag($0.minutes) }
            }
            .fixedSize()
            if let fingerprint {
                Text(Self.trustNote(fingerprint))
                    .appText(.supporting)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if working { ProgressView().controlSize(.small) }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(fingerprint == nil ? "Link" : "Trust and Link") { link() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(chosen == nil || working)
            }
        }
        .padding(28)
        .frame(width: 460)
    }

    private func key(_ provider: DaemonAPI.ProviderSummary, _ workspace: DaemonAPI.MachineInfo) -> String {
        "\(provider.id.rawValue)/\(workspace.id)"
    }

    private func row(_ provider: DaemonAPI.ProviderSummary, _ workspace: DaemonAPI.MachineInfo) -> some View {
        let other = workspace.host.flatMap { id in control.hosts.first { $0.id == id }?.name }
        let selected = chosen == key(provider, workspace)
        return Button {
            if chosen != key(provider, workspace) { fingerprint = nil }
            chosen = key(provider, workspace)
        } label: {
            HStack(spacing: 8) {
                Image(icon: selected ? "largecircle.fill.circle" : "circle").accessibilityHidden(true)
                Text(workspace.name).appText(.reading)
                Text(other.map { "linked to \($0)" } ?? workspace.state.capitalized)
                    .appText(.supporting).foregroundStyle(.secondary)
                Spacer()
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(other != nil)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .padding(.leading, 12)
    }

    private func link() {
        guard let chosen, let slash = chosen.firstIndex(of: "/") else { return }
        let provider = ProviderID(rawValue: String(chosen[..<slash]))
        let workspace = String(chosen[chosen.index(after: slash)...])
        working = true
        Task {
            let shown = await control.link(host.id, to: provider, workspace: workspace,
                                           startWhenUsed: startWhenUsed, stopWhenIdleMinutes: stopWhenIdle,
                                           trust: fingerprint)
            working = false
            if let shown {
                fingerprint = shown
                return
            }
            dismiss()
        }
    }
}
