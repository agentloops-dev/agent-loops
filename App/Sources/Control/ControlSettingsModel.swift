import AgentsKitCore
import AppKit
import Network

/// What Settings ▸ Control plane shows (058, frames D–F): the control plane itself, its
/// hosts and its clients, kept true by the control plane's own notifications.
///
/// A connection of its own to the control plane, as the window's second client session:
/// Settings can be open while the window reconnects, and the other way round.
@MainActor
@Observable
final class ControlSettingsModel {
    private(set) var status: DaemonAPI.ControlStatus?
    private(set) var hosts: [DaemonAPI.ControlMachine] = []
    /// Machine providers (#691), as the control plane last listed them.
    private(set) var providers: [DaemonAPI.ProviderSummary] = []
    /// Why this control plane runs no providers, in its own words (several copies, research R4,
    /// or no folder for their secrets, #3113); nil when it runs them.
    private(set) var providersOff: String?
    var providersHere: Bool { providersOff == nil }
    /// The groups providers are given to (#1761): every one for an admin, a member's own.
    /// Empty from a control plane before groups.
    private(set) var groups: [DaemonAPI.Group] = []
    /// Whether this window's person is an admin, who adds and changes providers (#1761).
    var isAdmin: Bool { status?.person?.isAdmin ?? true }
    /// Servers added from a provider's workspace (Add as Host…), by name: linked to it once
    /// they join.
    private var linkOnJoin: [String: (provider: ProviderID, workspace: String, trust: String?)] = [:]
    private(set) var clients: [ClientRecord] = []
    /// How each connected client reaches the control plane now (frame N). Empty from a
    /// control plane that does not say.
    private(set) var connections: [UUID: DaemonAPI.ClientConnection] = [:]
    /// The person's warm pools (#5366), as the control plane last listed them, and when.
    private(set) var pools: [DaemonAPI.WarmPool] = []
    private(set) var poolsReadAt: Date?
    /// False on a control plane before warm pools: the section is not drawn.
    private(set) var poolsHere = true
    /// Each provider's price an hour by size, from its New Machine form, for a pool's cost.
    private(set) var poolPrices: [ProviderID: [String: Double]] = [:]
    /// Said when an action was refused, in the words the control plane used.
    var problem: String?
    private(set) var isReachable = false

    /// Where the control plane is, as the page says it when it can't be reached: its
    /// folder on this Mac, or the addresses it was paired at.
    let whereItIs: String
    /// The control plane's address when it is on the internet (#1498), such as
    /// `control.agentloops.dev`: paired with no pin, so its certificate is publicly trusted
    /// and devices reach it from any network. Nil for one with a pin, as on a Mac.
    let publicAddress: String?
    private let link: ControlLink
    private let client: DaemonClient
    private var listening: Task<Void, Never>?

    init?(endpoint: ControlConfig.Endpoint) {
        guard let link = ControlConfig.link(endpoint) else { return nil }
        self.link = link
        client = DaemonClient(link: link.controlLink)
        switch endpoint {
        case .remote(let membership):
            whereItIs = "\(membership.name) (\(membership.url ?? membership.addresses.first ?? "no address"))"
            publicAddress = Self.publicAddress(url: membership.url, pin: membership.pin)
        }
    }

    /// A pinless `https` address at a name, not an IP address or `localhost`: its host,
    /// with its port when that is not 443.
    static func publicAddress(url: String?, pin: String?) -> String? {
        guard pin?.isEmpty ?? true, let url, let parts = URLComponents(string: url), parts.scheme == "https",
              let host = parts.host, !host.isEmpty, host != "localhost",
              IPv4Address(host) == nil, IPv6Address(host) == nil else { return nil }
        guard let port = parts.port, port != 443 else { return host }
        return "\(host):\(port)"
    }

    /// Whether the control plane runs on this Mac, and so may be restarted from here.
    var isOnThisMac: Bool { status?.machineID == MachineID.current }

    /// The host on this Mac: shown first and without buttons (the look gate's decision).
    var thisMacHost: DaemonAPI.ControlMachine? { hosts.first { $0.machineID == MachineID.current } }
    /// The machines that may carry a provider (#3496): the person's online ones, this Mac first.
    var carriers: [DaemonAPI.ControlMachine] {
        ProviderCarrier.choices(hosts, person: status?.person?.id, own: thisMacHost?.id)
    }

    var onlineCount: Int { hosts.filter(\.answers).count }

    func start() async {
        await refresh()
        guard listening == nil else { return }
        listening = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                for await note in self.client.notifications() where note.method.hasPrefix("control/") {
                    if note.method == DaemonAPI.Notification.controlInstallProgress {
                        self.installStep = note.params?["step"]?.stringValue
                        continue
                    }
                    if note.method == DaemonAPI.Notification.controlProvidersChanged,
                       let listed = try? note.params?["providers"]?.decode([DaemonAPI.ProviderSummary].self) {
                        self.providers = listed
                        continue
                    }
                    if note.method == DaemonAPI.Notification.controlTunnelChanged,
                       let change = try? note.params?.decode(DaemonAPI.MachineTunnelChanged.self) {
                        self.tunnels[change.name] = change.tunnel
                    }
                    // What this page does not show asks for nothing (#2055).
                    if Self.notShown.contains(note.method) { continue }
                    await self.refresh()
                }
                // The control plane went; come back when it does.
                self.isReachable = false
                try? await Task.sleep(for: .seconds(2))
                await self.refresh()
            }
        }
    }

    /// Control notifications about what this page does not show: approvals, links,
    /// sign-ins, caps, user settings and kept work. Each would otherwise run `refresh()`'s
    /// seven calls, on every Mac with Settings open (#2055).
    static let notShown: Set<String> = [
        DaemonAPI.Notification.controlJoinRequested, DaemonAPI.Notification.controlJoinDecided,
        DaemonAPI.Notification.controlLinkRequested, DaemonAPI.Notification.controlLinkDecided,
        DaemonAPI.Notification.controlLinksChanged,
        DaemonAPI.Notification.controlSignInRequested, DaemonAPI.Notification.controlSignInDecided,
        DaemonAPI.Notification.controlSignInWaiting,
        DaemonAPI.Notification.controlCapReached,
        DaemonAPI.Notification.controlUserSettingsChanged, DaemonAPI.Notification.controlSecretsLeft,
        DaemonAPI.Notification.controlKeptChanged, DaemonAPI.Notification.controlKeptDelivered,
    ]

    func stop() {
        listening?.cancel()
        listening = nil
        Task { await client.disconnect() }
    }

    func refresh() async {
        do {
            try await client.connect(startIfNeeded: false, timeout: .seconds(2))
            status = try await client.call(DaemonAPI.Method.controlStatus, returning: DaemonAPI.ControlStatus.self)
            OlderPeer.controlVersion = status?.version
            hosts = try await client.call(DaemonAPI.Method.hostsList, returning: [DaemonAPI.ControlMachine].self)
            clients = try await client.call(DaemonAPI.Method.clientsList, returning: [ClientRecord].self)
                .sorted { ($0.id == status?.you ? 0 : 1, $0.paired) < ($1.id == status?.you ? 0 : 1, $1.paired) }
            let links = (try? await client.call(DaemonAPI.Method.clientsConnections,
                                                returning: [DaemonAPI.ClientConnection].self)) ?? []
            connections = Dictionary(links.map { ($0.client, $0) }, uniquingKeysWith: { $1 })
            await refreshProviderList()
            groups = (try? await client.call(DaemonAPI.Method.groupsList, DaemonAPI.Empty(),
                                             returning: DaemonAPI.GroupList.self))?.groups ?? []
            await linkJoined()
            isReachable = true
        } catch {
            isReachable = false
        }
    }

    // MARK: Machine providers (#691)

    private func refreshProviderList() async {
        do {
            let answer = try await client.call(DaemonAPI.Method.providersList, DaemonAPI.Empty())
            providers = (try? answer["providers"]?.decode([DaemonAPI.ProviderSummary].self)) ?? []
            providersOff = nil
        } catch let error as JSONRPCError where error.code == DaemonAPI.Failure.providersNeedOneCopy {
            providers = []
            providersOff = error.message
        } catch let error as JSONRPCError where error.code == JSONRPCError.methodNotFound {
            providers = []
            providersOff = "This control plane runs no machine providers."
        } catch {}
    }

    /// Make Default (#3979): New Machine uses this provider, until a machine is made on another.
    func makeDefault(_ provider: ProviderID) async {
        await perform(DaemonAPI.Method.providersMakeDefault, DaemonAPI.ProviderRequest(id: provider))
    }

    /// Ask every provider for its workspaces now (FR-004: when a page that shows them opens).
    func listMachinesAgain(_ provider: ProviderID? = nil) async {
        _ = try? await client.call(DaemonAPI.Method.providersRefresh, DaemonAPI.ProviderRequest(id: provider))
    }

    /// Adds a provider; nil once it is added, or why not, in the control plane's words.
    func addProvider(_ request: DaemonAPI.ProviderAdd) async -> String? {
        do {
            _ = try await client.call(request.id == nil ? DaemonAPI.Method.providersAdd : DaemonAPI.Method.providersUpdate,
                                      request)
            await refresh()
            return nil
        } catch let error as JSONRPCError {
            return HostProblem.controlRefusal(error)
        } catch {
            return "The control plane can’t be reached."
        }
    }

    /// Why an OAuth connection did not start or finish (#6074): the words, and data.reason.
    struct OAuthRefusal: Error, Equatable {
        var words: String
        var reason: String?
    }

    private static func oauthRefusal(_ error: any Error) -> OAuthRefusal {
        guard let error = error as? JSONRPCError else { return OAuthRefusal(words: "The control plane can’t be reached.") }
        return OAuthRefusal(words: HostProblem.controlRefusal(error), reason: error.data?["reason"]?.stringValue)
    }

    /// providers/oauthStart (#6072): the consent page to open, on the machine that keeps it.
    func oauthStart(_ request: DaemonAPI.ProviderOAuthStart) async -> Result<DaemonAPI.ProviderOAuthStarted, OAuthRefusal> {
        do {
            return .success(try await client.call(DaemonAPI.Method.providersOAuthStart, request,
                                                  returning: DaemonAPI.ProviderOAuthStarted.self))
        } catch {
            return .failure(Self.oauthRefusal(error))
        }
    }

    /// providers/oauthFinish (#6072): the provider, added or connected again.
    func oauthFinish(_ request: DaemonAPI.ProviderOAuthFinish) async -> Result<DaemonAPI.ProviderSummary, OAuthRefusal> {
        do {
            let summary = try await client.call(DaemonAPI.Method.providersOAuthFinish, request,
                                                returning: DaemonAPI.ProviderSummary.self)
            await refresh()
            return .success(summary)
        } catch {
            return .failure(Self.oauthRefusal(error))
        }
    }

    /// Gives a provider to a group, or with nil to the admins alone (#1761).
    func setGroup(_ provider: ProviderID, _ group: String?) async {
        await perform(DaemonAPI.Method.providersSetGroup, DaemonAPI.ProviderGroup(provider: provider, group: group))
    }

    func removeProvider(_ id: ProviderID) async {
        await perform(DaemonAPI.Method.providersRemove, DaemonAPI.ProviderRequest(id: id))
    }

    /// Links a host to a workspace. A host that joined with the command has no ssh host key
    /// trusted yet (#3379): the fingerprint to show is answered, and `trust` is that one once
    /// the person trusts it. Nil once linked or refused.
    @discardableResult
    func link(_ host: MachineRef, to provider: ProviderID, workspace: String, startWhenUsed: Bool? = nil,
              stopWhenIdleMinutes: Int? = nil, trust: String? = nil) async -> String? {
        do {
            _ = try await client.call(DaemonAPI.Method.hostsLink,
                                      DaemonAPI.MachineLink(host: host, provider: provider, workspace: workspace,
                                                            startWhenUsed: startWhenUsed, stopWhenIdleMinutes: stopWhenIdleMinutes,
                                                            trust: trust))
            problem = nil
        } catch let error as JSONRPCError where error.code == DaemonAPI.Failure.hostKeyToTrust {
            if let fingerprint = error.data?["fingerprint"]?.stringValue { return fingerprint }
            problem = HostProblem.controlRefusal(error)
        } catch let error as JSONRPCError {
            problem = HostProblem.controlRefusal(error)
        } catch {
            problem = "The control plane can’t be reached."
        }
        await refresh()
        return nil
    }

    /// Delete on a failed make's row (#6078): the control plane drops the row and asks the
    /// provider for nothing, since it made nothing.
    func deleteFailedMake(_ provider: ProviderID, workspace: String) async {
        await perform(DaemonAPI.Method.providersDeleteWorkspace,
                      DaemonAPI.ProviderWorkspaceRequest(id: provider, workspace: workspace))
    }

    // MARK: Making and deleting workspaces (US5)

    /// Workspaces made here that install on their first boot, as `provider/workspace`, shown
    /// as Joining… until their host joins and is linked.
    private(set) var joiningWorkspaces: Set<String> = []

    /// The New Machine form and its starting values (#4634), or why it couldn’t be read.
    func workspaceOptions(_ provider: ProviderID?) async -> Result<DaemonAPI.ProviderOptions, WorkspaceRefusal> {
        do {
            let options = try await client.call(DaemonAPI.Method.providersOptions, DaemonAPI.ProviderRequest(id: provider),
                                                returning: DaemonAPI.ProviderOptions.self)
            return .success(options)
        } catch let error as JSONRPCError {
            return .failure(WorkspaceRefusal(error.message))
        } catch {
            return .failure(WorkspaceRefusal("The control plane can’t be reached."))
        }
    }

    /// Makes a workspace: what was made, or why not in the provider's words.
    func makeMachine(_ provider: ProviderID, name: String, options: JSONValue,
                       clones: [String] = []) async -> Result<DaemonAPI.ProviderCreated, WorkspaceRefusal> {
        do {
            let made = try await client.call(DaemonAPI.Method.providersCreate,
                                             DaemonAPI.ProviderCreate(id: provider, name: name, options: options,
                                                                      clones: clones.isEmpty ? nil : clones),
                                             returning: DaemonAPI.ProviderCreated.self)
            if made.joining { joiningWorkspaces.insert("\(provider.rawValue)/\(made.workspace.id)") }
            await refresh()
            return .success(made)
        } catch let error as JSONRPCError {
            return .failure(WorkspaceRefusal(error.message))
        } catch {
            return .failure(WorkspaceRefusal("The control plane can’t be reached."))
        }
    }

    /// Start a Machine from a Repository (#1809): the shared sheet's calls, on this connection.
    var machineCalls: MachineFromRepositorySheet.Calls { .through(client) }

    /// A refusal from the control plane, in the window's words.
    nonisolated static func refusal(_ error: any Error) -> String {
        (error as? JSONRPCError).map(HostProblem.controlRefusal) ?? "The control plane can’t be reached."
    }

    func isJoining(_ provider: ProviderID, _ workspace: DaemonAPI.MachineInfo) -> Bool {
        workspace.host == nil && joiningWorkspaces.contains("\(provider.rawValue)/\(workspace.id)")
    }

    /// Links a server added from a workspace once it joins under `name` (Add as Host…).
    /// `trust` is the host key's fingerprint the install trusted, which the link keeps (#3379).
    func linkWhenJoined(name: String, provider: ProviderID, workspace: String, trust: String? = nil) async {
        linkOnJoin[name] = (provider, workspace, trust)
        await linkJoined()
    }

    private func linkJoined() async {
        for (name, wanted) in linkOnJoin {
            guard let host = hosts.first(where: { $0.name == name && $0.workspace == nil }) else { continue }
            linkOnJoin[name] = nil
            _ = try? await client.call(DaemonAPI.Method.hostsLink,
                                       DaemonAPI.MachineLink(host: host.id, provider: wanted.provider, workspace: wanted.workspace,
                                                             trust: wanted.trust))
        }
    }

    /// The unlinked hosts that may already be `workspace`, added by hand under its ssh alias
    /// (#976): Add as Host… offers to link one instead.
    func hostsThatMayBe(_ workspace: DaemonAPI.MachineInfo) -> [DaemonAPI.ControlMachine] {
        guard workspace.host == nil else { return [] }
        return hosts.filter { $0.workspace == nil && workspace.mayBe(hostNamed: $0.name) }
    }

    struct WorkspaceRefusal: Error {
        var message: String
        init(_ message: String) { self.message = message }
    }

    // MARK: Actions

    func forget(_ client: UUID) async {
        await perform(DaemonAPI.Method.clientsForget, DaemonAPI.ClientRequest(client: client))
    }

    /// Forget This Mac (#344, 071 FR-015): the control plane forgets this window, and only
    /// it, then the window's pairing goes. True when it was forgotten; otherwise `problem`
    /// says why, and nothing has changed.
    func forgetThisWindow() async -> Bool {
        // Paired with the control plane the organisation pins this Mac to (#998, US7 AS3).
        if let refusal = ControlConfig.leaveRefusal {
            problem = refusal
            return false
        }
        do {
            _ = try await client.call(DaemonAPI.Method.clientsForgetSelf, DaemonAPI.Empty())
        } catch let error as JSONRPCError {
            problem = HostProblem.controlRefusal(error)
            return false
        } catch {
            problem = "The control plane can’t be reached."
            return false
        }
        problem = nil
        ControlConfig.forget()
        return true
    }

    /// The step the control plane says an install is at (`control/installProgress`).
    private(set) var installStep: String?
    /// How each server's reverse tunnel stands, by name, as the control plane said it
    /// (`control/tunnelChanged`, #435): before the server has joined, too.
    private(set) var tunnels: [String: DaemonAPI.MachineTunnel] = [:]

    enum InstallOutcome: Equatable {
        /// `tunnel`: the server dials the control plane through a reverse tunnel the
        /// control plane holds over ssh (#435, #610).
        case added(name: String, tunnel: Bool)
        /// The server's key is new to this Mac: the person looks at it and says so.
        case needsTrust(fingerprint: String)
        /// A host updated over ssh (#1766): it starts again with the control plane's version.
        case updated(name: String)
        case failed(String)
    }

    /// Add a server over ssh (frame M2): the control plane installs the host once with the
    /// key given here, which goes in this one call and is kept nowhere (058, T072). With no
    /// key, the control plane's ssh logs in as `ssh user@host` would (#413). `tunnel`: whether
    /// it dials back through an ssh tunnel; nil leaves it to whether it is behind a bastion.
    /// `provider` and `workspace` are Add as Machine…'s: a codespace whose provider a machine
    /// carries is installed from that machine, with the token kept there (#3497).
    func install(destination: String, name: String? = nil, key: String? = nil, trust: String? = nil,
                 tunnel: Bool? = nil, provider: ProviderID? = nil, workspace: String? = nil) async -> InstallOutcome {
        installStep = nil
        var params: [String: JSONValue] = ["destination": .string(destination)]
        if let tunnel { params["tunnel"] = .bool(tunnel) }
        if let provider, let workspace {
            params["provider"] = .string(provider.rawValue)
            params["workspace"] = .string(workspace)
        }
        if let key { params["key"] = .string(key) }
        if let name, !name.isEmpty { params["name"] = .string(name) }
        if let trust { params["trust"] = .string(trust) }
        do {
            let answer = try await client.call(DaemonAPI.Method.hostsInstall, JSONValue.object(params))
            await refresh()
            if let fingerprint = answer["needsTrust"]?.stringValue { return .needsTrust(fingerprint: fingerprint) }
            return .added(name: answer["name"]?.stringValue ?? name ?? destination,
                          tunnel: answer["tunnel"]?.boolValue ?? false)
        } catch let error as JSONRPCError {
            return .failed(HostProblem.controlRefusal(error))
        } catch {
            return .failed("The control plane can’t be reached.")
        }
    }

    /// Update a host over ssh (#1766): its server and key given again, as at its install,
    /// because the control plane keeps neither. The control plane checks the server's root
    /// is this host's, copies its agentsd there, and starts the host again.
    func update(host: MachineRef, destination: String, key: String? = nil, trust: String? = nil) async -> InstallOutcome {
        installStep = nil
        var params: [String: JSONValue] = ["host": .string(host.rawValue), "destination": .string(destination)]
        if let key { params["key"] = .string(key) }
        if let trust { params["trust"] = .string(trust) }
        do {
            let answer = try await client.call(DaemonAPI.Method.hostsUpdate, JSONValue.object(params))
            await refresh()
            if let fingerprint = answer["needsTrust"]?.stringValue { return .needsTrust(fingerprint: fingerprint) }
            return .updated(name: answer["name"]?.stringValue ?? destination)
        } catch let error as JSONRPCError {
            return .failed(HostProblem.controlRefusal(error))
        } catch {
            return .failed("The control plane can’t be reached.")
        }
    }

    /// The hosts in the control plane's ssh config, or just `destination` as typed, as
    /// `ssh -G` resolves each (#610): what Add a Machine's server box offers. Empty when the
    /// control plane cannot say.
    func sshHosts(_ destination: String? = nil) async -> [DaemonAPI.SSHHost] {
        var params: [String: JSONValue] = [:]
        if let destination { params["destination"] = .string(destination) }
        return (try? await client.call(DaemonAPI.Method.hostsSSHHosts, JSONValue.object(params),
                                       returning: [DaemonAPI.SSHHost].self)) ?? []
    }

    /// Where a host added from now on has its folders found (#429).
    var folderDetection: FolderDetection { status?.folderDetection ?? .standard }
    /// Where the folders picker looks (#656): nowhere with auto-detect off.
    var suggestedPaths: [String] { folderDetection.enabled ? folderDetection.paths : [] }

    func setFolderDetection(_ detection: FolderDetection) async {
        await perform(DaemonAPI.Method.controlSetFolderDetection, detection)
    }

    /// A code for a new client, or for a new host (frame G, Add by Code). A browser's is
    /// good only through the page's own listener (071 R2).
    func startCode(forHost: Bool, browser: Bool = false) async -> DaemonAPI.ControlCodeShown? {
        do {
            let shown: DaemonAPI.ControlCodeShown = if forHost {
                // With an invite on the lock in the code, so the machine adds itself (#3491, D3).
                try await LockState.invite(client.call(DaemonAPI.Method.hostsStartEnroll, returning: DaemonAPI.ControlCodeShown.self),
                                           on: client, pins: LockPins(defaults: WindowDefaults.store, name: ControlConfig.lockPinName),
                                           account: ControlConfig.lockAccount)
            } else {
                try await client.call(DaemonAPI.Method.clientsStartPairing,
                                      JSONValue.object(browser ? ["kind": .string(ClientRecord.Kind.browser.rawValue)] : [:]),
                                      returning: DaemonAPI.ControlCodeShown.self)
            }
            problem = nil
            return shown
        } catch let error as JSONRPCError {
            problem = HostProblem.controlRefusal(error)
        } catch {
            problem = "The control plane can’t be reached."
        }
        return nil
    }

    /// **Open in Browser** (#109): the page in the default browser, pairing it in the same
    /// step if no browser of its kind is paired yet (`BrowserOpening`). Nil once opened, else
    /// why not, in words to show.
    ///
    /// With a `place` (a link of the Web + Mac link tier, #4937, such as Cost's Open Reports), the
    /// page opens there, also after it pairs. With the control plane on another machine, it opens
    /// the page on the control plane's own domain, whose sign-in keeps the address (#4937).
    func openInBrowser(place: String? = nil) async -> String? {
        await refresh()
        guard isReachable, let status else { return "The control plane can’t be reached." }
        guard isOnThisMac else {
            if place != nil, let page = BrowserOpening.linkPage(status, onThisMac: false),
               let url = BrowserOpening.pageURL(page, place: place) {
                Self.open(url)
                return nil
            }
            return "The page is served only to a browser on the Mac that runs the control plane."
        }
        let browsers = clients.filter { $0.kind == .browser }.map(\.name)
        switch BrowserOpening.step(web: status.web, browsers: browsers, family: Self.defaultBrowserFamily) {
        case .notServed(let why):
            return why
        case .open(let url):
            Self.open(place.flatMap { BrowserOpening.pageURL(url.absoluteString, place: $0) } ?? url)
            return nil
        case .pair:
            guard let web = status.web, let code = await startCode(forHost: false, browser: true) else {
                return problem ?? "No code could be made."
            }
            guard let url = BrowserOpening.pairingURL(web, code: code.text, place: place) else { return "No code could be made." }
            Self.open(url)
            return nil
        }
    }

    /// A walk drives a headless Chrome of its own, never the person's browser.
    private static var defaultBrowserFamily: String? {
        if ControlConfig.walk != nil { return "Chrome" }
        let app = NSWorkspace.shared.urlForApplication(toOpen: URL(string: "http://localhost")!)
        return BrowserOpening.family(bundleID: app.flatMap { Bundle(url: $0)?.bundleIdentifier })
    }

    /// The default browser, or for a walk the file its headless browser is pointed at by.
    private static func open(_ url: URL) {
        if let file = ControlConfig.walkOpenedURL {
            // store-bounds: not a store: a walk's hand-off file, that the walk's own browser reads once
            try? Data(url.absoluteString.utf8).write(to: file, options: .atomic)
            return
        }
        NSWorkspace.shared.open(url)
    }

    func stopCodes() async {
        _ = try? await client.call(DaemonAPI.Method.clientsStopPairing)
    }

    func restart() async {
        // The control plane on this Mac is the host side's to restart (ControlSettingsView).
        problem = "Restart the control plane from Agent Loops on this Mac."
        return
    }

    /// The person's warm pools (#5366). A control plane before pools answers no such method,
    /// and the section goes; one that cannot be reached keeps the last list, with its time.
    func loadPools() async {
        do {
            pools = try await client.call(DaemonAPI.Method.poolList, DaemonAPI.Empty(), returning: DaemonAPI.WarmPools.self).pools
            poolsReadAt = Date()
            poolsHere = true
            for provider in Set(pools.map(\.provider)) where poolPrices[provider] == nil {
                guard case .success(let options) = await providerOptions(provider) else { continue }
                var prices: [String: Double] = [:]
                for field in NewMachineSheet.Field.read(options.schema) {
                    for (choice, cost) in field.costs { prices[choice] = WarmPoolWords.hourly(cost) }
                }
                poolPrices[provider] = prices
            }
        } catch let error as JSONRPCError where error.isMethodNotFound {
            poolsHere = false
        } catch {}
    }

    /// Save on a warm pool's form: nil once it is kept, or why not, in the control plane's words.
    func setPool(_ set: DaemonAPI.WarmPoolSet) async -> String? {
        do {
            _ = try await client.call(DaemonAPI.Method.poolSet, set)
            await loadPools()
            return nil
        } catch {
            return Self.refusal(error)
        }
    }

    /// Delete Warm Pool…: the pool and its ready machines; claimed machines stay.
    func deletePool(_ id: String) async {
        do {
            _ = try await client.call(DaemonAPI.Method.poolDelete, DaemonAPI.WarmPoolRequest(id: id))
            problem = nil
        } catch {
            problem = Self.refusal(error)
        }
        await loadPools()
    }

    /// A provider's New Machine form, for a pool's size and region (#5366).
    func providerOptions(_ provider: ProviderID) async -> Result<DaemonAPI.ProviderOptions, NewMachineRefusal> {
        do {
            return .success(try await client.call(DaemonAPI.Method.providersOptions, DaemonAPI.ProviderRequest(id: provider),
                                                  returning: DaemonAPI.ProviderOptions.self))
        } catch {
            return .failure(NewMachineRefusal(Self.refusal(error)))
        }
    }

    private func perform(_ method: String, _ params: some Encodable & Sendable) async {
        do {
            _ = try await client.call(method, params)
            problem = nil
        } catch let error as JSONRPCError {
            problem = HostProblem.controlRefusal(error)
        } catch {
            problem = "The control plane can’t be reached."
        }
        await refresh()
    }
}
