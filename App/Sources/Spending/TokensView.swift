import AgentsKitCore
import SwiftUI

/// What the work used in tokens (#956): the usage charts in tokens, then today's tokens
/// against the token limits. It was a switch on Cost; a page of its own is one place to
/// look for one question.
///
/// The Cost report's Tokens tab (#6884). Read-only: the limits
/// are set in Settings ▸ Agents ▸ Policy (#2957).
struct TokensView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                // Over time, as Health draws it (#823).
                UsageChartsSection(metric: .tokens, load: { await model.usageHistory() }, refresh: model.costState,
                                   alsoRead: model.work.costStateReadAt)
                    .padding(.bottom, 8)
                if let state = model.costState { TokensToday(state: state) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(24)
        }
    }
}

/// The day's tokens, and the token limits where there are any (#838).
private struct TokensToday: View {
    let state: DaemonAPI.CostState

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Today")
                .appText(.supporting)
                .foregroundStyle(.secondary)
            Text("\(state.tokensToday.formatted()) tokens")
                .appText(.title).fontWeight(.semibold)
                .monospacedDigit()
                .textSelection(.enabled)
            if let dailyTokens = state.limits.dailyTokens {
                Text("Daily token limit \(dailyTokens.formatted()) · \((state.dayTokenHeadroom ?? 0).formatted()) left")
                    .monospacedDigit()
                    .foregroundStyle((state.dayTokensAreCloseToFull ? StateTint.failure : .none).style(or: .secondary))
            }
            if let perAgentTokens = state.limits.perAgentTokens {
                Text("Each agent up to \(perAgentTokens.formatted()) tokens")
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
        }
    }
}
