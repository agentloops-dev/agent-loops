import AgentsKitCore
import SwiftUI

/// The agent browser or a simulator in a window of its own (#4138, #4141), from Open on a
/// live tile or Open the page on a hand-off: where the person uses what the agent drives,
/// not in the side column. The browser window watches (`browser/watch`) while it is open,
/// so it gets frames whoever drives; over it, a hand-off's step with Done and Can't do it.
struct TakeOverWindow: View {
    @Environment(AppModel.self) private var model
    let key: TakeOverKey?

    private var agent: Agent? { key.flatMap { model.work.agent($0.agentID) } }

    var body: some View {
        Group {
            if let key, let agent {
                content(key.source, agent: agent)
            } else {
                ContentUnavailableView("This session is gone", icon: "questionmark.folder",
                                       description: Text("Close this window."))
            }
        }
        .paperGround()
        .frame(minWidth: 480, minHeight: 360)
        .navigationTitle(AppCheckout.windowTitle(agent?.title))
        .navigationSubtitle(key?.title ?? "")
        .tint(Paper.accent)
        .environment(\.inTakeOver, true)
    }

    @ViewBuilder
    private func content(_ source: LiveSource, agent: Agent) -> some View {
        switch source {
        case .simulator:
            if model.host(agent.host, has: Capability.simulators1) {
                SimulatorPaneView(calls: model.simulatorCalls(for: agent.id), isVisible: true)
            } else {
                HostNeedsUpdateView()
            }
        case .browser, .screen:
            if model.host(agent.host, has: Capability.browser1) {
                VStack(spacing: 0) {
                    if let request = model.work.elicitation(for: agent.id), request.handOff != nil {
                        ElicitationView(request: request)
                            .id(request.id)
                            .padding(12)
                        Divider()
                    }
                    if source == .browser, model.host(ofAgent: agent.id) == .mac,
                       model.agent(agent.id, has: Capability.browserWindow1) {
                        BrowserOwnWindow(agent: agent)
                    } else {
                        AgentBrowserPane(agent: agent, isVisible: true)
                    }
                }
            } else {
                HostNeedsUpdateView()
            }
        }
    }
}

/// Take over in the browser's own window (#7259): on the Mac that runs the agents' browser,
/// the person gets its real window with the session's tab, in front, in place of the
/// screencast, as `hand_to_person` gives it. This window then says where the page is, and
/// the browser's window goes back below the screen when this one closes. When the host
/// cannot give it (another agent has the screen, the browser has no window here), or the
/// person asks for it, the page is the screencast here, as on every other client.
private struct BrowserOwnWindow: View {
    @Environment(AppModel.self) private var model
    let agent: Agent

    private enum Place: Equatable {
        /// Asking the host for the window.
        case asking
        /// The browser's own window is in front: its name, as the host says it.
        case ownWindow(String)
        /// The screencast, in this window.
        case here
    }

    @State private var place = Place.asking

    private var browser: MachineBrowser { model.machineBrowser(for: agent.id) }

    var body: some View {
        Group {
            switch place {
            case .asking:
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            case .ownWindow(let window):
                ContentUnavailableView {
                    Label("The page is in its own window", icon: "safari")
                } description: {
                    Text(Self.words(window))
                } actions: {
                    Button("Bring to Front") { Task { await show() } }
                        .keyboardShortcut(.defaultAction)
                    Button("Show Here") { Task { await showHere() } }
                }
            case .here:
                AgentBrowserPane(agent: agent, isVisible: true)
            }
        }
        .task(id: agent.id) { await show() }
        .onDisappear {
            guard case .ownWindow = place else { return }
            let browser = browser, session = agent.id
            Task { await browser.putBackWindow(session: session) }
        }
    }

    /// The browser's own window with the session's tab, in front; the screencast when the
    /// host cannot.
    private func show() async {
        if let window = await browser.showWindow(session: agent.id) {
            place = .ownWindow(window)
        } else {
            place = .here
        }
    }

    /// The screencast here, and the browser's own window back below the screen.
    private func showHere() async {
        place = .here
        await browser.putBackWindow(session: agent.id)
    }

    /// "Use the page in the agents' Chrome window, in front (github.com). It goes back when
    /// you close this window."
    static func words(_ window: String) -> String {
        let named = window.isEmpty ? "the agents' browser window" : window
        return "Use the page in \(named). It goes back when you close this window."
    }
}
