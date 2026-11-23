import Foundation

// Machine providers (#691, specs/691-workspace-providers): the command provider, an MCP
// server on the control plane that starts and stops the machines hosts run on. What is here is records and the wire; the
// MCP client and the starts are ControlPlaneKit's, handed in as `WorkspaceHandling`.

/// A provider, by the id the control plane gave it.
public struct ProviderID: RawRepresentable, Codable, Hashable, Sendable, CustomStringConvertible {
    public var rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }

    public var description: String { rawValue }
}

/// A host and the workspace it runs on (data-model.md, "WorkspaceLink").
public struct WorkspaceLink: Codable, Hashable, Sendable {
    public var provider: ProviderID
    /// The workspace's id, as its provider lists it.
    public var workspace: String
    /// A prompt or agent start for it while stopped starts it (US3). Nil reads as on.
    public var startWhenUsed: Bool?
    /// Stopped once idle this long (US4); nil is off.
    public var stopWhenIdleMinutes: Int?
    /// Started a little before the host's next scheduled workflow while stopped (#1004
    /// FR-009). Nil reads as on.
    public var startForSchedules: Bool?
    /// The host's next scheduled workflow as it last said it (FR-003), kept while it is stopped.
    public var schedule: DaemonAPI.MachineSchedule?
    /// How long its last start took, from asking the provider to the host connecting (FR-004).
    public var measuredStartSeconds: Int?
    /// The due time it was last started for, so each starts it once (FR-006).
    public var wokeFor: Date?

    public static let defaultIdleMinutes = 120
}

/// A provider as the store keeps it, `v1/providers/<id>.json`: never its secrets' values,
/// which stay in the control plane's own home (research R3).
public struct ProviderRecord: Codable, Hashable, Sendable, Identifiable {
    public enum Kind: String, Codable, Sendable {
        /// The command provider Agents ships (`workspace-commands`). The MCP server kinds,
        /// `stdio` and `http`, were removed (#6623).
        case commands
    }

    public var id: ProviderID
    public var name: String
    public var kind: Kind
    /// The command provider's configuration (contracts/command-provider.md).
    public var commands: JSONValue?
    public var secretNames: [String]
    /// Which optional contract tools it has: `options`, `create`, `delete`.
    public var capabilities: [String]
    public var contract: Int
    public var rev: Int
    public var forgotten: Bool?

    public init(id: ProviderID, name: String, kind: Kind, commands: JSONValue? = nil, secretNames: [String] = [],
                capabilities: [String] = [], contract: Int = 1, rev: Int = 0, forgotten: Bool? = nil) {
        self.id = id
        self.name = name
        self.kind = kind
        self.commands = commands
        self.secretNames = secretNames
        self.capabilities = capabilities
        self.contract = contract
        self.rev = rev
        self.forgotten = forgotten
    }

    /// Why it can't be used as it is, in words for the sheet; nil when it can.
    public var problem: String? {
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        if trimmed.isEmpty { return "Give it a name." }
        if trimmed.count > 60 { return "The name is over 60 characters." }
        if commands?.objectValue == nil { return "Choose a preset or give its commands." }
        return nil
    }
}

public extension DaemonAPI.Method {
    /// `{}` → `{providers: [ProviderSummary]}`, as last listed.
    static let providersList = "providers/list"
    /// `ProviderAdd` → `ProviderSummary`, once the server has been checked and has listed.
    static let providersAdd = "providers/add"
    /// `ProviderAdd` with `id`; secrets left out are kept, and so are its commands.
    static let providersUpdate = "providers/update"
    /// `ProviderRequest`. Its hosts stay, unlinked.
    static let providersRemove = "providers/remove"
    /// `ProviderOAuthStart` → `ProviderOAuthStarted` (#6072): the consent page to open.
    static let providersOAuthStart = "providers/oauthStart"
    /// `ProviderOAuthFinish` → `ProviderSummary` (#6072): what the hand-back page gave.
    static let providersOAuthFinish = "providers/oauthFinish"
    /// `{id?}`: list again, said as `control/providersChanged`.
    static let providersRefresh = "providers/refresh"
    /// `MachineLink` → `ControlMachine`. A workspace linked to another host moves here.
    static let hostsLink = "machines/link"
    /// `MachineRequest` → `ControlMachine`.
    static let hostsUnlink = "machines/unlink"
    /// `MachineOptions` → `ControlMachine`.
    static let hostsSetMachineOptions = "machines/setMachineOptions"
    /// `MachineRequest` → `ControlMachine`, at once; the start goes on, said as `control/hostChanged`.
    static let hostsStart = "machines/start"
    /// `MachineStop` → `ControlMachine`, at once.
    static let hostsStop = "machines/stop"
    /// `ProviderRequest` → `ProviderOptions`: the New Machine form (US5).
    static let providersOptions = "providers/options"
    /// `ProviderCreate` → `ProviderCreated`, once the provider has made it (US5).
    static let providersCreate = "providers/create"
    /// `MachineRequest` → `{}`: its workspace deleted, then the host removed (US5).
    static let hostsDeleteMachine = "machines/deleteMachine"
    /// `MachineRequest` → `StartedMachinePreview`: Delete's question, the worktrees on it with
    /// work not pushed (#3979).
    static let hostsDeletePreview = "machines/deletePreview"
    /// `ProviderRequest` → `{}`: Make Default, the provider New Machine uses (#3979).
    static let providersMakeDefault = "providers/makeDefault"
    /// `ProviderWorkspaceRequest` → `{}`: Delete on a machine New Machine made that never
    /// joined (#4610). The provider deletes the workspace.
    static let providersDeleteWorkspace = "providers/deleteWorkspace"
}

public extension DaemonAPI.Notification {
    /// `{providers: [ProviderSummary]}`.
    static let controlProvidersChanged = "control/providersChanged"
}

public extension DaemonAPI.Failure {
    /// Providers are off on this control plane: several copies (research R4), or no folder for their secrets (#3113). The message says which.
    static let providersNeedOneCopy = -32064
    /// Stop while agents are working there, not confirmed (US4 AS4). `data` is `{running: [names]}`.
    static let agentsWorking = -32068
    /// machines/link for a machine whose ssh host key nobody has trusted yet: one that joined
    /// with the command (#3379). `data` is `{fingerprint}`, given back as `trust`.
    static let hostKeyToTrust = -32069
}

public extension DaemonAPI {
    /// One workspace, as its provider last listed it (contracts/provider-contract.md).
    struct MachineInfo: Codable, Sendable, Hashable, Identifiable {
        public var id: String
        public var name: String
        /// `running`, `starting`, `stopping`, `stopped` or `unknown`.
        public var state: String
        public var ssh: String?
        public var tunnel: Bool?
        /// Its ssh host key is made again at each start (#1701).
        public var hostKeyRenews: Bool?
        public var cost: String?
        /// The provider stops it by itself after this many minutes with no use, as GitHub
        /// stops a codespace (#4803).
        public var idleTimeoutMinutes: Int?
        /// The host it is linked to, in `providers/list`.
        public var host: MachineRef?
        /// Why the make failed, in the provider's words, when `state` is failed (#3973).
        public var reason: String?
        /// New Machine made it and the control plane waits for it to join (#4610): the row
        /// says Making….
        public var joining: Bool?
        /// Why the control plane gave up waiting for it to join, once it did (#4610): the row
        /// says so, with Delete.
        public var notJoined: String?
        /// The set-up of a machine New Machine is making (#6073), from the press of Make until
        /// it joins: what each step did, for a client that opens late.
        public var setup: MachineSetup?

        public init(id: String, name: String, state: String, ssh: String? = nil, tunnel: Bool? = nil,
                    hostKeyRenews: Bool? = nil, cost: String? = nil, idleTimeoutMinutes: Int? = nil,
                    host: MachineRef? = nil, joining: Bool? = nil, notJoined: String? = nil,
                    setup: MachineSetup? = nil) {
            self.id = id
            self.name = name
            self.state = state
            self.ssh = ssh
            self.tunnel = tunnel
            self.hostKeyRenews = hostKeyRenews
            self.cost = cost
            self.idleTimeoutMinutes = idleTimeoutMinutes
            self.host = host
            self.joining = joining
            self.notJoined = notJoined
            self.setup = setup
        }

        /// Whether a host added by hand may be this workspace (#976): its name is the
        /// workspace's ssh destination, or one of its dot-separated parts is the workspace's
        /// name, as `cws.devstack-d7539e59` is for `devstack-d7539e59`. Never a part of a
        /// part: `dev` is not `devstack`.
        public func mayBe(hostNamed hostName: String) -> Bool {
            let host = hostName.lowercased()
            if let ssh, !ssh.isEmpty, ssh.lowercased() == host { return true }
            let name = name.lowercased()
            guard !name.isEmpty else { return false }
            if host == name { return true }
            // The name may hold dots of its own, so match it as a run of whole parts.
            return ".\(host).".contains(".\(name).")
        }
    }

    /// A provider for the Providers page: never its commands, headers or secrets' values.
    struct ProviderSummary: Codable, Sendable, Hashable, Identifiable {
        public var id: ProviderID
        public var name: String
        public var kind: String
        /// Each secret's name, and whether it has a value.
        public var secrets: [String: Bool]
        public var capabilities: [String]
        public var contract: Int
        /// The command provider's preset, when it uses one.
        public var preset: String?
        public var workspaces: [MachineInfo]
        public var listedAt: Date?
        /// False once a list has failed after a good one: `workspaces` is what it last said.
        public var current: Bool
        public var problem: String?
        /// The group whose users may use it (#1761); none is the admins' alone.
        public var group: String?
        /// The caller may start machines from it (#1761). Nil from an older control plane,
        /// which lets everyone.
        public var mayUse: Bool?
        /// New Machine makes the caller's machines on it (#3979): the one made on last or
        /// chosen with Make Default, or else the first one added. Nil from an older control plane.
        public var `default`: Bool?
        /// The CLI its machines are reached through over ssh (gh, coder) when the machine
        /// that runs the provider has none (#3367). Nil when it needs none or has it.
        public var sshToolMissing: String?
        /// The machine that carries it: runs its server and keeps its secrets (#3398). Nil for
        /// one made before, which must be added again.
        public var machine: MachineRef?
        public var machineName: String?
        /// Its machine is offline: its machines can't be started or stopped until it is back.
        public var machineOffline: Bool?
        /// Connected with OAuth (#6072), not a pasted token: whose account, and whether the
        /// connection still works. Nil for a pasted token.
        public var oauth: ProviderOAuth?

        public init(id: ProviderID, name: String, kind: String, secrets: [String: Bool] = [:],
                    capabilities: [String] = [], contract: Int = 1, preset: String? = nil,
                    workspaces: [MachineInfo] = [], listedAt: Date? = nil, current: Bool = true,
                    problem: String? = nil, group: String? = nil, mayUse: Bool? = nil,
                    sshToolMissing: String? = nil, machine: MachineRef? = nil, machineName: String? = nil,
                    machineOffline: Bool? = nil, default isDefault: Bool? = nil, oauth: ProviderOAuth? = nil) {
            self.default = isDefault
            self.oauth = oauth
            self.machine = machine
            self.machineName = machineName
            self.machineOffline = machineOffline
            self.id = id
            self.name = name
            self.kind = kind
            self.secrets = secrets
            self.capabilities = capabilities
            self.contract = contract
            self.preset = preset
            self.workspaces = workspaces
            self.listedAt = listedAt
            self.current = current
            self.problem = problem
            self.group = group
            self.mayUse = mayUse
            self.sshToolMissing = sshToolMissing
        }

        /// Whether the caller may start machines from it.
        public var usable: Bool { mayUse ?? true }
    }

    /// A linked host's workspace, on its `machines/list` row and in `control/hostChanged`.
    struct MachineWorkspace: Codable, Sendable, Hashable {
        public var provider: ProviderID
        public var providerName: String
        public var workspace: String
        public var workspaceName: String
        /// As the provider last listed it.
        public var workspaceState: String
        /// What the host is shown as while it isn't online, over its uplink's own state:
        /// `stopped`, `starting` or `stopping`. `stopping` shows while it is still online too.
        public var hostState: String?
        /// While starting: `provider` (asked to start it), `connecting` (waiting for its
        /// agentsd) or `sshStart` (starting agentsd over ssh).
        public var phase: String?
        /// When the start or stop began, for the strip's timer.
        public var since: Date?
        /// Why the last start or stop failed, in the provider's words.
        public var problem: String?
        /// "Stopped by Agents after 30 minutes idle", already worded.
        public var stoppedBy: String?
        public var canStart: Bool
        public var canStop: Bool
        public var startWhenUsed: Bool
        public var stopWhenIdleMinutes: Int?
        public var cost: String?
        /// As its provider last listed it: the provider stops it after this many minutes
        /// with no use (#4803).
        public var idleTimeoutMinutes: Int?
        /// False when the provider's last list failed.
        public var current: Bool
        /// The Start for scheduled workflows switch (#1004 FR-009).
        public var startForSchedules: Bool?
        /// Whether the host says when its next scheduled workflow is due (FR-011): one from
        /// before #1004 doesn't, so it gets no wake-up calls and keeps the old warning.
        public var tellsSchedule: Bool?
        /// Its next scheduled workflow, as it last said it.
        public var nextWorkflow: String?
        public var nextWorkflowAt: Date?
        /// When the control plane will start it for that workflow (FR-007): while it is
        /// stopped, with the switch on.
        public var wakeAt: Date?

        public init(provider: ProviderID, providerName: String, workspace: String, workspaceName: String,
                    workspaceState: String, hostState: String? = nil, phase: String? = nil, since: Date? = nil,
                    problem: String? = nil, stoppedBy: String? = nil, canStart: Bool = false, canStop: Bool = false,
                    startWhenUsed: Bool = true, stopWhenIdleMinutes: Int? = nil, cost: String? = nil,
                    idleTimeoutMinutes: Int? = nil, current: Bool = true, startForSchedules: Bool? = nil, tellsSchedule: Bool? = nil,
                    nextWorkflow: String? = nil, nextWorkflowAt: Date? = nil, wakeAt: Date? = nil) {
            self.startForSchedules = startForSchedules
            self.tellsSchedule = tellsSchedule
            self.nextWorkflow = nextWorkflow
            self.nextWorkflowAt = nextWorkflowAt
            self.wakeAt = wakeAt
            self.provider = provider
            self.providerName = providerName
            self.workspace = workspace
            self.workspaceName = workspaceName
            self.workspaceState = workspaceState
            self.hostState = hostState
            self.phase = phase
            self.since = since
            self.problem = problem
            self.stoppedBy = stoppedBy
            self.canStart = canStart
            self.canStop = canStop
            self.startWhenUsed = startWhenUsed
            self.stopWhenIdleMinutes = stopWhenIdleMinutes
            self.cost = cost
            self.idleTimeoutMinutes = idleTimeoutMinutes
            self.current = current
        }
    }

    /// `providers/add` and `providers/update`.
    struct ProviderAdd: Codable, Sendable, Hashable {
        /// For an update.
        public var id: ProviderID?
        public var name: String
        public var kind: ProviderRecord.Kind
        public var commands: JSONValue?
        /// The machine that carries it (#3398). Nil: the person's first online machine on an
        /// add, the one it has on an update.
        public var machine: MachineRef?
        /// Name to value. Passed on to `machine`, which keeps them; never given back.
        public var secrets: [String: String]?

        public init(id: ProviderID? = nil, name: String, kind: ProviderRecord.Kind, commands: JSONValue? = nil,
                    machine: MachineRef? = nil, secrets: [String: String]? = nil) {
            self.machine = machine
            self.id = id
            self.name = name
            self.kind = kind
            self.commands = commands
            self.secrets = secrets
        }
    }

    /// A provider connected with OAuth (#6072): whose account and team its machine's token is
    /// for, and whether the provider still takes it. Never a token.
    struct ProviderOAuth: Codable, Sendable, Hashable {
        public var account: String?
        public var team: String?
        /// `connected`, or `disconnected` once its last list was refused: connect again.
        public var state: String
        public init(account: String? = nil, team: String? = nil, state: String = "connected") {
            self.account = account
            self.team = team
            self.state = state
        }
        public var disconnected: Bool { state == "disconnected" }
    }

    /// `providers/oauthStart` (#6072): connect with OAuth on `machine`; with `id`, again.
    struct ProviderOAuthStart: Codable, Sendable, Hashable {
        public var preset: String
        public var machine: MachineRef?
        public var id: ProviderID?
        public var origin: String?
        public init(preset: String, machine: MachineRef? = nil, id: ProviderID? = nil, origin: String? = nil) {
            self.preset = preset
            self.machine = machine
            self.id = id
            self.origin = origin
        }
    }

    /// `providers/oauthStart`'s result: the consent page, its state, and the keeping machine.
    struct ProviderOAuthStarted: Codable, Sendable, Hashable {
        public var url: String
        public var state: String
        public var machine: MachineRef
        public var machineName: String?
        public init(url: String, state: String, machine: MachineRef, machineName: String? = nil) {
            self.url = url
            self.state = state
            self.machine = machine
            self.machineName = machineName
        }
    }

    /// `providers/oauthFinish` (#6072): the code and the state, or the provider's error.
    struct ProviderOAuthFinish: Codable, Sendable, Hashable {
        public var state: String?
        public var code: String?
        public var error: String?
        public var name: String?
        public init(state: String? = nil, code: String? = nil, error: String? = nil, name: String? = nil) {
            self.state = state
            self.code = code
            self.error = error
            self.name = name
        }

        /// What agents://oauth/callback carried, or nil when it carried neither a code nor an error.
        public init?(callback: URL) {
            let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
            func value(_ name: String) -> String? { items.first { $0.name == name }?.value.flatMap { $0.isEmpty ? nil : $0 } }
            guard value("code") != nil || value("error") != nil else { return nil }
            self.init(state: value("state"), code: value("code"), error: value("error"))
        }
    }

    struct ProviderRequest: Codable, Sendable, Hashable {
        public var id: ProviderID?
        public init(id: ProviderID?) { self.id = id }
    }

    /// `providers/deleteWorkspace` (#4610): one of a provider's workspaces that no machine is
    /// linked to.
    struct ProviderWorkspaceRequest: Codable, Sendable, Hashable {
        public var id: ProviderID
        public var workspace: String
        public init(id: ProviderID, workspace: String) {
            self.id = id
            self.workspace = workspace
        }
    }

    /// `machines/link`.
    struct MachineLink: Codable, Sendable, Hashable {
        public var host: MachineRef
        public var provider: ProviderID
        public var workspace: String
        public var startWhenUsed: Bool?
        public var stopWhenIdleMinutes: Int?
        /// The fingerprint of the host key shown with `hostKeyToTrust`, once the person
        /// trusts it (#3379).
        public var trust: String?

        public init(host: MachineRef, provider: ProviderID, workspace: String, startWhenUsed: Bool? = nil,
                    stopWhenIdleMinutes: Int? = nil, trust: String? = nil) {
            self.host = host
            self.provider = provider
            self.workspace = workspace
            self.startWhenUsed = startWhenUsed
            self.stopWhenIdleMinutes = stopWhenIdleMinutes
            self.trust = trust
        }
    }

    /// `machines/setMachineOptions`. `stopWhenIdleMinutes` 0 turns it off.
    struct MachineOptions: Codable, Sendable, Hashable {
        public var host: MachineRef
        public var startWhenUsed: Bool?
        public var stopWhenIdleMinutes: Int?
        /// Start for scheduled workflows (#1004 FR-009).
        public var startForSchedules: Bool?

        public init(host: MachineRef, startWhenUsed: Bool? = nil, stopWhenIdleMinutes: Int? = nil,
                    startForSchedules: Bool? = nil) {
            self.startForSchedules = startForSchedules
            self.host = host
            self.startWhenUsed = startWhenUsed
            self.stopWhenIdleMinutes = stopWhenIdleMinutes
        }
    }

    /// `providers/options`: a flat JSON Schema object of strings, integers, booleans and
    /// enums with titles. An enum's `x-costs` may name each value's cost, for the confirmation.
    ///
    /// With it, what New Machine would make (#4634): the provider it is for (the default one
    /// when none was asked for), the `values` the form starts with (the options used last
    /// there, over each field's default or first choice), and the next `machine-N`. An older
    /// control plane leaves them out.
    struct ProviderOptions: Codable, Sendable, Hashable {
        public var schema: JSONValue
        public var id: ProviderID?
        public var values: JSONValue?
        public var name: String?
    }

    /// `providers/create`.
    struct ProviderCreate: Codable, Sendable, Hashable {
        public var id: ProviderID
        public var name: String
        public var options: JSONValue?
        /// Git URLs the new host clones once it has joined (#1122), as File ▸ Clone Folder
        /// does, one after another. Nil or empty for none.
        public var clones: [String]?
        /// The repository to start it from (#1809), an https git address. A provider whose
        /// New Machine form has `repository`, such as Codespaces, makes the workspace from
        /// it; any other has the host clone it once it joins, before `clones`.
        public var repository: String?
        public init(id: ProviderID, name: String, options: JSONValue? = nil, clones: [String]? = nil,
                    repository: String? = nil) {
            self.id = id
            self.name = name
            self.options = options
            self.clones = clones
            self.repository = repository
        }
    }

    /// What `providers/create` made. `joining`: it runs the install on its first boot, and is
    /// linked when it joins. Otherwise it is added over its `ssh`, as Add as Host… does.
    struct ProviderCreated: Codable, Sendable, Hashable {
        public var workspace: MachineInfo
        public var joining: Bool
        public init(workspace: MachineInfo, joining: Bool) {
            self.workspace = workspace
            self.joining = joining
        }
    }

    /// `machines/stop`.
    struct MachineStop: Codable, Sendable, Hashable {
        public var host: MachineRef
        /// Stop even though turns are running there (US4).
        public var confirmRunning: Bool?

        public init(host: MachineRef, confirmRunning: Bool? = nil) {
            self.host = host
            self.confirmRunning = confirmRunning
        }
    }
}
