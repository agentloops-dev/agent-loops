import AgentsKitCore
import SwiftUI

/// The words of the session summary (#7322, epic #7318), the same on each client: the web
/// page's port (#7324) and the Remote's (#7325) take them from here.
enum SessionSummaryWords {
    static let summary = "Summary"
    static let chat = "Chat"
    static let heading = "What is going on"
    static let writing = "Writing a summary…"
    static let notWritten = "No summary was written. It is written again when the next turn ends."
    static let lastTurn = "Last turn"
    static let openChat = "Open the chat"
    static let pickerHelp = "Show the session's summary or its whole chat"

    /// "You asked: …", the person's first prompt under a summary not written yet.
    static func asked(_ prompt: String) -> String { "You asked: “\(prompt)”" }

    /// "8 turns", or nil when the count is not known (an earlier page is not loaded).
    static func turns(_ count: Int?) -> String? {
        guard let count, count > 0 else { return nil }
        return count == 1 ? "1 turn" : "\(count) turns"
    }

    /// The last outcome on one line: "Complete · “The index keeps each turn's changed paths.”"
    static func outcome(_ report: WorkReport) -> String {
        "\(report.outcome.heading) · “\(report.message)”"
    }
}

/// A session on its summary (#7322, the wireframes Alex approved at gate 2, #7320): in the
/// transcript's place, what is going on (the paragraph the host writes, `Agent.summary`),
/// the goal and plan, the last turn, what waits for the person, and the Now card at the
/// foot. The whole chat is one click away. An Ask is answered here as in the chat.
struct SessionSummaryPage: View {
    let agent: Agent
    /// The person's first prompt, shown while no summary is written yet.
    var firstPrompt: String?
    /// How many turns the session has, when all of them are loaded.
    var turnCount: Int?
    var isComingBack = false
    /// What waits on the person, and the parts of the Now card, as the chat has them.
    var cards: [WaitingCard] = []
    /// How much of the foot of the pane the floating prompt covers.
    var bottomInset: CGFloat = 0
    let openChat: @MainActor () -> Void

    @Environment(\.chatActions) private var actions

    var body: some View {
        ScrollView {
            VStack(spacing: 14) {
                SummaryParagraph(summary: agent.summary, firstPrompt: firstPrompt)
                    .chatColumn()
                // The goal and plan stay under the paragraph (gate 2): where it is going.
                NowTopCard(agent: agent)
                if let report = agent.report {
                    LastTurnCard(report: report, labels: workLabels, turnCount: turnCount, openChat: openChat)
                        .chatColumn()
                }
                let asks = cards.filter(\.asks)
                if !asks.isEmpty {
                    VStack(spacing: 12) {
                        WaitingForYouLabel().chatColumn()
                        ForEach(asks) { $0.view }
                    }
                }
                nowCard.chatColumn()
            }
            .padding(.top, 16)
            .padding(.bottom, bottomInset + 20)
            .frame(maxWidth: .infinity)
        }
        .scrollIndicators(.automatic)
    }

    /// The session's issue and pull request labels (`#<n>`, `P#<n>`) and its finished one.
    private var workLabels: [SessionLabel] {
        agent.labels.filter { $0.kind != .plain }
    }

    private var nowCard: NowCard {
        let agentID = agent.id
        var background = BackgroundActions(output: actions.backgroundOutput)
        if let stop = actions.stopBackground {
            background.stop = { item in await stop(item, agentID) }
        }
        if let steps = actions.subagentSteps {
            background.steps = { item in steps(item.id) }
        }
        return NowCard(agent: agent, isComingBack: isComingBack,
                       parts: cards.filter { !$0.asks },
                       leases: actions.leaseStatus(agentID)?.without(agent.eventWait),
                       capacityStart: actions.capacityStart(agent),
                       background: background,
                       openLease: actions.openLease)
    }
}

/// "What is going on": the paragraph and its "Last update" (#5139). Before the first one is
/// written, "Writing a summary…" and the person's first prompt; when a write fails, the old
/// text stays with its time.
private struct SummaryParagraph: View {
    let summary: SessionSummary?
    let firstPrompt: String?

    private var text: String? {
        guard let text = summary?.text.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else { return nil }
        return text
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(SessionSummaryWords.heading)
                    .appText(.fine).fontWeight(.semibold)
                    .foregroundStyle(Paper.tertiary)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 8)
                if summary?.state == .writing, text != nil {
                    ProgressView().controlSize(.mini)
                        .accessibilityLabel(SessionSummaryWords.writing)
                }
                if text != nil, let at = summary?.at {
                    LastUpdateText(read: at)
                }
            }
            if let text {
                Text(text)
                    .appText(.reading)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                Text(summary?.state == .failed ? SessionSummaryWords.notWritten : SessionSummaryWords.writing)
                    .appText(.reading)
                    .foregroundStyle(Paper.secondary)
                if let firstPrompt, !firstPrompt.isEmpty {
                    Text(SessionSummaryWords.asked(firstPrompt))
                        .appText(.supporting)
                        .foregroundStyle(Paper.secondary)
                        .lineLimit(3)
                        .truncationMode(.tail)
                }
            }
        }
        .summaryCard()
    }
}

/// The last turn: its outcome and the agent's sentence, the session's issue and pull
/// request, and the way to the whole chat.
private struct LastTurnCard: View {
    let report: WorkReport
    let labels: [SessionLabel]
    let turnCount: Int?
    let openChat: @MainActor () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(SessionSummaryWords.lastTurn)
                    .appText(.fine).fontWeight(.semibold)
                    .foregroundStyle(Paper.tertiary)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 8)
                if let turns = SessionSummaryWords.turns(turnCount) {
                    Text(turns + " ·")
                        .appText(.fine)
                        .foregroundStyle(Paper.tertiary)
                        .monospacedDigit()
                }
                Button(SessionSummaryWords.openChat, action: openChat)
                    .buttonStyle(.plain)
                    .appText(.fine)
                    .foregroundStyle(.tint)
            }
            Text(SessionSummaryWords.outcome(report))
                .appText(.supporting)
                .fixedSize(horizontal: false, vertical: true)
            if !labels.isEmpty {
                HStack(spacing: 6) {
                    ForEach(labels, id: \.normalizedValue) { LabelChip(label: $0) }
                }
            }
        }
        .summaryCard()
    }
}

private extension View {
    /// A card of the summary: raised paper with a rule round it, as the wireframes draw it.
    func summaryCard() -> some View {
        padding(.horizontal, 16)
            .padding(.vertical, 14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Paper.raised, in: RoundedRectangle(cornerRadius: Paper.Radius.card))
            .overlay(RoundedRectangle(cornerRadius: Paper.Radius.card).strokeBorder(Paper.rule, lineWidth: 1))
    }
}
