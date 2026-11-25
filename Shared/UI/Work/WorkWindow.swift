import AgentsKitCore
import Observation
import SwiftUI

/// A window of its own's pages (#5475): what its Back and Forward go along.
@MainActor
protocol WorkWindowPages: AnyObject {
    /// The page the window shows now.
    var key: WorkWindowKey? { get }
    var canGoBack: Bool { get }
    var canGoForward: Bool { get }
    func step(back: Bool)
}

/// The windows on screen now, of every kind, by the page each shows.
@MainActor
enum OpenWorkWindows {
    private static var pages: [ObjectIdentifier: any WorkWindowPages] = [:]

    static func opened(_ window: any WorkWindowPages) { pages[ObjectIdentifier(window)] = window }
    static func closed(_ window: any WorkWindowPages) { pages[ObjectIdentifier(window)] = nil }

    /// Whether a window other than this one shows the page: a page has one window at most.
    static func shows(_ key: WorkWindowKey, besides window: any WorkWindowPages) -> Bool {
        pages.values.contains { $0 !== window && $0.key == key }
    }
}

/// A Work page in a window of its own (#5475, epic #5480), on the Mac and on an iPad: what its page
/// asks of the app, with this window's own places, so Back in one window never moves another. Each
/// read and write goes on to the app's model, so every window draws the same lists, cache and saves.
@MainActor @Observable
final class WorkWindowModel: WorkModel, WorkWindowPages {
    @ObservationIgnored private let base: any WorkModel
    /// The main window, in front: what is not an item's page opens there.
    @ObservationIgnored private let showMain: @MainActor () -> Void
    /// Opens a session's chat, where the app opens one from a window of its own.
    @ObservationIgnored private let showSession: @MainActor (UUID) -> Void
    /// Brings forward the window that shows a page already.
    @ObservationIgnored var bringForward: (@MainActor (WorkWindowKey) -> Void)?

    var workHistory = LineageHistory<WorkPlace>()

    /// No place: a window of a page that no link moves (a conversation, #5482), so Back and Forward stay off.
    init(base: any WorkModel, place: WorkPlace?, showMain: @escaping @MainActor () -> Void,
         showSession: @escaping @MainActor (UUID) -> Void) {
        self.base = base
        self.showMain = showMain
        self.showSession = showSession
        if let place { workHistory.go(place) }
    }

    /// The page the window shows now.
    var key: WorkWindowKey? { WorkWindowKey(workPlace) }

    var canGoBack: Bool { workHistory.canGoBack }
    var canGoForward: Bool { workHistory.canGoForward }

    var isOwnWindow: Bool { true }

    /// A link to an item opens in this window, or brings forward the window that shows it already; a
    /// list is the main window's.
    func showWork(_ place: WorkPlace) {
        guard let key = WorkWindowKey(place) else {
            base.showWork(place)
            return showMain()
        }
        guard !shownElsewhere(key) else { return }
        workHistory.go(WorkPlace(kind: place.kind, ref: place.ref, files: place.files))
    }

    /// Back or Forward, along this window's places.
    func step(back: Bool) {
        var history = workHistory
        if back { history.back() } else { history.forward() }
        guard let place = history.current, let key = WorkWindowKey(place), !shownElsewhere(key) else { return }
        workHistory = history
    }

    /// Another window shows the page, and comes forward: a page has one window at most.
    private func shownElsewhere(_ key: WorkWindowKey) -> Bool {
        guard key != self.key, let bringForward, OpenWorkWindows.shows(key, besides: self) else { return false }
        bringForward(key)
        return true
    }

    func openWorkSession(_ id: UUID) { showSession(id) }

    func offerStart(_ start: AppViewStart, in key: FolderKey) -> String? {
        let why = base.offerStart(start, in: key)
        if why == nil { showMain() }
        return why
    }

    // MARK: The app's own

    var workLists: WorkLists { base.workLists }
    var forgeMachines: [(id: MachineRef, name: String)] { base.forgeMachines }
    var boardMachines: [(id: MachineRef, name: String)] { base.boardMachines }
    var timelineMachines: [(id: MachineRef, name: String)] { base.timelineMachines }
    var timelineProjects: [WorkList.Project] { base.timelineProjects }
    var deploysMachines: [(id: MachineRef, name: String)] { base.deploysMachines }
    var workProjects: [WorkList.Project] { base.workProjects }
    var workFolders: [FolderKey] { base.workFolders }
    var lineageMachines: [(id: MachineRef, name: String)] { base.lineageMachines }
    var lineageChanges: Int { base.lineageChanges }
    var workAgents: [Agent] { base.workAgents }
    var workCopy: LocalCopy? { base.workCopy }
    var workCache: (any WorkCaching)? { base.workCache }
    var workSaves: WorkSaves? { base.workSaves }

    func askWorkConnectors() -> Bool { base.askWorkConnectors() }
    func forgeWrites(on host: MachineRef) -> Bool { base.forgeWrites(on: host) }
    func readsImageHosts(on host: MachineRef) -> Bool { base.readsImageHosts(on: host) }
    func workSessionTitle(_ id: UUID) -> String? { base.workSessionTitle(id) }
    func workSessionState(_ id: UUID) -> AgentState? { base.workSessionState(id) }
    func workLead(of epic: DaemonAPI.LineageNode) -> Agent? { base.workLead(of: epic) }

    func forgeList(_ kind: WorkList.Kind, _ request: DaemonAPI.ForgeListRequest,
                   on host: MachineRef) async throws -> DaemonAPI.ForgeList {
        try await base.forgeList(kind, request, on: host)
    }

    func forgeItem(_ node: DaemonAPI.LineageNode, cursor: String?,
                   on host: MachineRef) async throws -> DaemonAPI.ForgeItemDetail {
        try await base.forgeItem(node, cursor: cursor, on: host)
    }

    func forge<R: Decodable & Sendable>(_ method: String, _ params: some Encodable & Sendable, returning: R.Type,
                                        on host: MachineRef) async throws -> R {
        try await base.forge(method, params, returning: returning, on: host)
    }

    func controlAll<R: Decodable & Sendable>(_ method: String, _ params: some Encodable & Sendable,
                                             returning: R.Type) async throws -> R? {
        try await base.controlAll(method, params, returning: returning)
    }

    func reportNodes(_ query: DaemonAPI.ReportNodesQuery) async throws -> DaemonAPI.ReportNodes? {
        try await base.reportNodes(query)
    }

    func lineageSessionsOn(_ nodes: [DaemonAPI.LineageNode]) async -> DaemonAPI.LineageSessionsOn? {
        await base.lineageSessionsOn(nodes)
    }

    func lineageWalk(_ node: DaemonAPI.LineageNode, depth: Int, on host: MachineRef) async throws -> DaemonAPI.LineageGraph {
        try await base.lineageWalk(node, depth: depth, on: host)
    }

    func lineageCost(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageCostRollup? {
        await base.lineageCost(node, on: host)
    }

    func lineageEstimate(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageEstimate? {
        await base.lineageEstimate(node, on: host)
    }

    func lineageForecast(_ node: DaemonAPI.LineageNode, on host: MachineRef) async -> DaemonAPI.LineageForecast? {
        await base.lineageForecast(node, on: host)
    }
}

/// What a Work window draws: the page of its place, with its own Back and Forward. The app puts it in
/// its scene, with the model its window is made with.
struct WorkWindowPage: View {
    let base: any WorkModel
    /// The page, as the window was opened with it and is restored with it; a link in the window changes
    /// it, so the window is found by the page it shows.
    @Binding var key: WorkWindowKey?
    let showMain: @MainActor () -> Void
    let showSession: @MainActor (UUID) -> Void
    /// The window's model, for the menus of the app; nil once the window closes.
    var opened: (WorkWindowModel?) -> Void = { _ in }
    @Environment(\.openWindow) private var openWindow
    @State private var model: WorkWindowModel?

    var body: some View {
        Group {
            if let model {
                page(model, model.workPlace)
                    .toolbar { WorkWindowHistoryButtons(pages: model) }
            } else if key == nil || key?.place == nil {
                ContentUnavailableView("No page", icon: "macwindow.badge.plus",
                                       description: Text("Open an issue, a pull request or a release from Work."))
            } else {
                Color.clear
            }
        }
        .modifier(WorkWindows())
        .task(id: key) { follow() }
        .onChange(of: model?.key) { _, shown in
            if let shown, shown != key { key = shown }
        }
        .onDisappear {
            if let model { OpenWorkWindows.closed(model) }
            model = nil
            opened(nil)
        }
    }

    private func follow() {
        guard let key, model?.key != key, let place = key.place else { return }
        if let model { OpenWorkWindows.closed(model) }
        let made = WorkWindowModel(base: base, place: place, showMain: showMain, showSession: showSession)
        made.bringForward = { [openWindow] in openWindow(id: OpenWorkWindow.sceneID, value: $0) }
        OpenWorkWindows.opened(made)
        model = made
        opened(made)
    }

    @ViewBuilder
    private func page(_ model: WorkWindowModel, _ place: WorkPlace) -> some View {
        if place.files {
            WorkFilesPage(model: model, place: place).id(place)
        } else if place.kind == .release {
            WorkReleasePage(model: model, place: place).id(place)
        } else {
            WorkItemPage(model: model, place: place).id(place)
        }
    }
}

/// **Back** and **Forward** of a window of its own, along its pages.
struct WorkWindowHistoryButtons: ToolbarContent {
    let pages: any WorkWindowPages

    var body: some ToolbarContent {
        ToolbarItemGroup(placement: Self.placement) {
            Button { pages.step(back: true) } label: { Image(toolbarIcon: "chevron.left") }
                .disabled(!pages.canGoBack)
                .help("Back")
                .accessibilityLabel("Back")
            Button { pages.step(back: false) } label: { Image(toolbarIcon: "chevron.right") }
                .disabled(!pages.canGoForward)
                .help("Forward")
                .accessibilityLabel("Forward")
        }
    }

    private static var placement: ToolbarItemPlacement {
        #if os(iOS)
        .topBarLeading
        #else
        .navigation
        #endif
    }
}

/// Opens an item's page in a window of its own, or brings its window forward. In the environment where
/// the person has Work's windows on (`FeatureFlag.workWindows`) and the device has more than one window.
struct OpenWorkWindow: Equatable {
    fileprivate let open: OpenWindowAction

    /// The scene's id (`AgentsApp`, `RemoteApp`).
    static let sceneID = "work"
    static let words = "Open in New Window"

    @MainActor
    func callAsFunction(_ place: WorkPlace) {
        if let key = WorkWindowKey(place) { self(key) }
    }

    @MainActor
    func callAsFunction(_ key: WorkWindowKey) { open(id: Self.sceneID, value: key) }

    #if os(macOS)
    /// The click now is a ⌘-click.
    @MainActor
    static var commandHeld: Bool { NSApp.currentEvent?.modifierFlags.contains(.command) == true }
    #endif

    /// One action for the app: two are never different, so a row is not drawn again for one.
    static func == (_: Self, _: Self) -> Bool { true }
}

extension EnvironmentValues {
    @Entry var openWorkWindow: OpenWorkWindow? = nil
}

/// Gives the Work views inside it `openWorkWindow`.
struct WorkWindows: ViewModifier {
    @AppStorage(FeatureFlag.workWindows.key, store: WindowDefaults.store) private var on = false
    @Environment(\.supportsMultipleWindows) private var supportsMultipleWindows
    @Environment(\.openWindow) private var openWindow

    func body(content: Content) -> some View {
        content.environment(\.openWorkWindow, on && supportsMultipleWindows ? OpenWorkWindow(open: openWindow) : nil)
    }
}

extension WorkModel {
    /// A row or a link pressed: its page here, or with ⌘ held on the Mac, in a window of its own.
    func follow(_ place: WorkPlace, window: OpenWorkWindow?) {
        #if os(macOS)
        if let window, WorkWindowKey(place) != nil, OpenWorkWindow.commandHeld {
            return window(place)
        }
        #endif
        showWork(place)
    }
}

/// **Open in New Window** in the menu of a row or a link; nothing where it has no window.
struct WorkWindowButton: View {
    @Environment(\.openWorkWindow) private var openWorkWindow
    let key: WorkWindowKey?

    init(_ place: WorkPlace?) { key = place.flatMap { WorkWindowKey($0) } }
    init(key: WorkWindowKey) { self.key = key }

    var body: some View {
        if let openWorkWindow, let key {
            #if os(macOS)
            Button(OpenWorkWindow.words) { openWorkWindow(key) }
            #else
            Button(OpenWorkWindow.words, icon: "macwindow.badge.plus") { openWorkWindow(key) }
            #endif
        }
    }
}

/// **Open in New Window** in the toolbar of an item's page; none on a page in a window of its own.
struct WorkWindowToolbarButton: View {
    @Environment(\.openWorkWindow) private var openWorkWindow
    let key: WorkWindowKey?

    init(model: any WorkModel, place: WorkPlace) { key = model.isOwnWindow ? nil : WorkWindowKey(place) }
    init(key: WorkWindowKey) { self.key = key }

    var body: some View {
        if let openWorkWindow, let key {
            Button { openWorkWindow(key) } label: { Image(toolbarIcon: "macwindow.badge.plus") }
                .help(OpenWorkWindow.words)
                .accessibilityLabel(OpenWorkWindow.words)
        }
    }
}

extension View {
    /// A link to an item has **Open in New Window** in its menu; a list, or no item, has none.
    func workWindowMenu(_ place: WorkPlace?) -> some View { modifier(WorkWindowMenu(place: place)) }

    /// The title of a window of its own: the item's number and title. The main window keeps its own.
    @ViewBuilder
    func workWindowTitle(_ model: any WorkModel, title: String?, ref: String) -> some View {
        if model.isOwnWindow {
            navigationTitle(WorkWindowKey.title(title, ref: ref))
            #if os(iOS)
                .navigationBarTitleDisplayMode(.inline)
            #endif
        } else {
            self
        }
    }
}

private struct WorkWindowMenu: ViewModifier {
    @Environment(\.openWorkWindow) private var openWorkWindow
    let place: WorkPlace?

    func body(content: Content) -> some View {
        if openWorkWindow != nil {
            content.contextMenu { WorkWindowButton(place) }
        } else {
            content
        }
    }
}

#if os(iOS)
extension OpenWorkWindow {
    /// What a row dragged out of a Work list carries, to open a scene of its own (as a session's,
    /// #888). Listed under `NSUserActivityTypes`.
    static let activityType = "com.alexecollins.agents.remote.work"

    static func itemProvider(for key: WorkWindowKey, title: String) -> NSItemProvider {
        let activity = NSUserActivity(activityType: activityType)
        activity.title = title
        // What the page's scene, and only it, says it handles.
        activity.targetContentIdentifier = activityType
        activity.userInfo = ["kind": key.kind, "ref": key.ref]
        let provider = NSItemProvider()
        provider.registerObject(activity, visibility: .all)
        provider.suggestedName = title
        return provider
    }

    static func key(in activity: NSUserActivity) -> WorkWindowKey? {
        guard let kind = activity.userInfo?["kind"] as? String, let ref = activity.userInfo?["ref"] as? String else { return nil }
        return WorkWindowKey(kind: kind, ref: ref)
    }
}

extension View {
    /// A row of a Work list, draggable out of it into a scene of its own on an iPad.
    func workWindowDrag(_ place: WorkPlace, title: String) -> some View {
        modifier(WorkWindowDrag(place: place, title: title))
    }
}

private struct WorkWindowDrag: ViewModifier {
    @Environment(\.openWorkWindow) private var openWorkWindow
    let place: WorkPlace
    let title: String

    func body(content: Content) -> some View {
        if openWorkWindow != nil, let key = WorkWindowKey(place) {
            content.onDrag { OpenWorkWindow.itemProvider(for: key, title: WorkWindowKey.title(title, ref: key.ref)) }
        } else {
            content
        }
    }
}
#endif
