import AgentsKitCore
import SwiftUI

/// The allowance's strip (#5034, fourth of the session's strips; #1859): once the chat is
/// waiting to continue, when it continues and a Cancel, as the Mac's strip has them. While
/// it is only stopped, the two buttons sit under the line in the conversation (#5919).
struct RemoteContinueWhenBackStrip: View {
    @Environment(RemoteModel.self) private var model
    let agent: Agent

    @MainActor
    static func isShown(_ agent: Agent, model: RemoteModel) -> Bool {
        agent.state != .archived && agent.continueWhenBack != nil
            && model.host(agent.host, has: Capability.chatContinueWhenBack1)
    }

    var body: some View {
        // Grey: the chat carries on by itself.
        if let line = ContinueWhenBackWords.line(agent) {
            Strip(tone: .neutral, symbol: ContinueWhenBackWords.symbol, line: line,
                  second: StripButton(title: ContinueWhenBackWords.cancel) {
                      Task { await model.cancelContinue(agent.id) }
                  })
        }
    }
}
