import AgentsKitCore
import Charts
import SwiftUI

/// What the work used over time, as Health draws it (#823): D / W / M / 3M, the daily
/// average above the bars, and a bar picked to read its own figure.
///
/// Drawn in cost on Cost and in tokens on Tokens (#956), by the window and the Remote; the page draws `UsageCharts` in
/// UsageCharts.tsx. The bars are worked out in `UsageChart`, where they are tested; this
/// only draws them. It fetches through `load`, again whenever `refresh` changes.
struct UsageChartsSection<Refresh: Equatable>: View {
    let metric: UsageChart.Metric
    let load: () async -> UsageHistory?
    let refresh: Refresh
    /// The range, when the page beside the charts needs it too (Cost's Open Reports, #4938);
    /// else the charts keep their own.
    var shown: Binding<UsageChart.Range>?
    /// When the page's other part was read (Cost's and Tokens' day, #5144): the line says the older.
    var alsoRead: Date?

    @State private var history: UsageHistory?
    /// When `history` was read (#5144).
    @State private var read: Date?
    @State private var own: UsageChart.Range = .week
    @State private var picked: Date?

    private var rangeBinding: Binding<UsageChart.Range> { shown ?? $own }
    private var range: UsageChart.Range { rangeBinding.wrappedValue }
    private var pageRead: Date? { [read, alsoRead].compactMap { $0 }.min() }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let read = pageRead {
                // How old what the page shows is (#5144): the oldest of its reads, at the top.
                LastUpdateText(read: read).frame(maxWidth: .infinity, alignment: .trailing)
            }
            Picker("Range", selection: rangeBinding) {
                ForEach(UsageChart.Range.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            if let history {
                UsageChartBody(chart: UsageChart(history, range: range, metric: metric), picked: $picked)
            } else {
                ProgressView().frame(maxWidth: .infinity, minHeight: 200)
            }
        }
        .onChange(of: range) { picked = nil }
        .task(id: refresh) {
            if let loaded = await load() {
                history = loaded
                read = Date()
            }
        }
    }
}

private struct UsageChartBody: View {
    let chart: UsageChart
    @Binding var picked: Date?

    private var pickedBar: UsageChart.Bar? {
        guard let picked else { return nil }
        return chart.bars.last { $0.start <= picked }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            headline
            if chart.isEmpty {
                Text(chart.metric == .cost ? "Nothing was spent in this range." : "No tokens were used in this range.")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 160)
            } else {
                bars
                shares
            }
            notes
        }
    }

    private var headline: some View {
        ChartHeadline(
            title: pickedBar == nil ? chart.headline : "Total",
            value: chart.words(pickedBar?.value ?? chart.average),
            when: ChartRangeParts.when(range: chart.range, picked: pickedBar?.start,
                                       first: chart.bars.first?.start, last: chart.bars.last?.start))
    }

    private var bars: some View {
        Chart {
            ForEach(chart.bars) { bar in
                BarMark(x: .value("When", bar.start, unit: ChartRangeParts.unit(chart.range)), y: .value(chart.metric.rawValue, bar.value))
                    .foregroundStyle(Paper.accent.opacity(pickedBar == nil || pickedBar == bar ? 1 : 0.35))
                    .cornerRadius(2)
            }
            if chart.range != .day, pickedBar == nil, chart.average > 0 {
                RuleMark(y: .value("Average", perBar(chart.average)))
                    .foregroundStyle(.secondary)
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
            }
        }
        .chartSelectionAndAxes(range: chart.range, selected: $picked, yLabel: chart.metric.rawValue) { number in
            Text(chart.words(number))
        }
    }

    /// 3M's bars are weeks, so its daily average is drawn as a week's worth.
    private func perBar(_ daily: Double) -> Double { chart.range == .threeMonths ? daily * 7 : daily }

    /// Where the range went, by runtime, largest first.
    private var shares: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(chart.shares) { share in
                HStack(alignment: .firstTextBaseline) {
                    Text(UsageChart.runtimeName(share.runtime))
                    Spacer(minLength: 24)
                    Text(chart.words(share.value)).monospacedDigit()
                }
                .accessibilityElement(children: .combine)
            }
        }
    }

    @ViewBuilder
    private var notes: some View {
        VStack(alignment: .leading, spacing: 4) {
            if chart.metric == .tokens, chart.cachedReadTokens > 0 {
                Text("Plus \(UsageChart.tokenWords(Double(chart.cachedReadTokens))) read from cache, not drawn.")
            }
            if chart.metric == .cost, chart.unpricedTurns > 0 {
                Text("At least this much: \(chart.unpricedTurns == 1 ? "1 turn" : "\(chart.unpricedTurns) turns") ran on a runtime that reported no price.")
            }
            if !chart.otherCurrencies.isEmpty {
                Text("Also spent in \(chart.otherCurrencies.formatted(.list(type: .and))), not drawn.")
            }
        }
        .appText(.fine)
        .foregroundStyle(.secondary)
    }
}
