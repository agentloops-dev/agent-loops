import AgentsKitCore
import Charts
import SwiftUI

struct CycleTimeChartsSection<Refresh: Equatable>: View {
    let load: () async -> CycleTimeHistory?
    let refresh: Refresh

    @State private var history: CycleTimeHistory?
    @State private var read: Date?
    private var pageRead: Date? { read }
    @State private var range: CycleTimeChart.Range = .week
    @State private var breakdown: CycleTimeChart.Breakdown = .level
    @State private var repo = ""
    @State private var picked: Date?
    @State private var failed = false
    @State private var attempt = 0

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let read = pageRead {
                if failed, history != nil {
                    ChartReadFailureBanner(read: read, message: Self.couldNotRead) { attempt += 1 }
                } else {
                    LastUpdateText(read: read).frame(maxWidth: .infinity, alignment: .trailing)
                }
            }
            HStack {
                Picker("Range", selection: $range) {
                    ForEach(CycleTimeChart.Range.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                Picker("Group", selection: $breakdown) {
                    ForEach(CycleTimeChart.Breakdown.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.menu)
                .labelsHidden()
            }
            RepositoryPicker(repo: $repo,
                             options: history.map { RepositoryOption.options(CycleTimeChart.repositories(in: $0)) } ?? [])
            ChartLoadingOrFailure(failed: failed, hasHistory: history != nil,
                                  message: Self.couldNotRead, retry: { attempt += 1 }) {
                if let history {
                    CycleTimeChartBody(chart: CycleTimeChart(history, range: range,
                                                              breakdown: breakdown,
                                                              repo: repo.isEmpty ? nil : repo),
                                       picked: $picked)
                }
            }
        }
        .onChange(of: range) { picked = nil }
        .onChange(of: repo) { picked = nil }
        .task(id: Reading(refresh: refresh, attempt: attempt)) {
            apply(await readChart(load))
        }
    }

    static var couldNotRead: String { "Could not read cycle time" }

    private func apply(_ result: ChartReadResult<CycleTimeHistory>) {
        switch result {
        case let .read(loaded, at):
            history = loaded
            read = at
            failed = false
        case .failed: failed = true
        case .cancelled: break
        }
    }

    private struct Reading: Equatable {
        let refresh: Refresh
        let attempt: Int
    }
}

private struct CycleTimeChartBody: View {
    let chart: CycleTimeChart
    @Binding var picked: Date?

    private var pickedBar: CycleTimeChart.Bar? {
        chartPickedBar(in: chart.bars, at: picked) { $0.start }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            headline
            if chart.isEmpty {
                ChartEmpty()
                if let line = chart.noStartLine {
                    Text(line).appText(.fine).foregroundStyle(.secondary)
                }
            } else {
                bars
                if let line = chart.splitLine {
                    Text(line).appText(.fine).foregroundStyle(.secondary)
                }
                if let line = chart.noStartLine {
                    Text(line).appText(.fine).foregroundStyle(.secondary)
                }
                shares
            }
        }
    }

    private var headline: some View {
        ChartHeadline(
            title: pickedBar == nil ? CycleTimeChart.headline : "Picked bar",
            value: CycleTimeChart.words(pickedBar?.value ?? chart.median),
            when: ChartRangeParts.when(range: chart.range, picked: pickedBar?.start,
                                       first: chart.bars.first?.start, last: chart.bars.last?.start),
            detail: pickedBar.map { "\($0.count) \($0.count == 1 ? "PR" : "PRs")" })
    }

    private var bars: some View {
        Chart {
            ForEach(chart.bars) { bar in
                BarMark(x: .value("When", bar.start, unit: ChartRangeParts.unit(chart.range)),
                        y: .value("Cycle time", bar.value))
                    .foregroundStyle(Paper.accent.opacity(pickedBar == nil || pickedBar == bar ? 1 : 0.35))
                    .cornerRadius(2)
            }
            if chart.range != .day, pickedBar == nil, chart.p85 > 0 {
                RuleMark(y: .value("P85", chart.p85))
                    .foregroundStyle(.secondary)
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
            }
        }
        .chartSelectionAndAxes(range: chart.range, selected: $picked, yLabel: "Cycle time") { number in
            Text(CycleTimeChart.words(number))
        }
    }

    private var shares: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(chart.shares) { share in
                HStack(alignment: .firstTextBaseline) {
                    Text(share.name)
                    Spacer(minLength: 24)
                    Text(CycleTimeChart.words(share.value)).monospacedDigit()
                    Text("\(share.count)").monospacedDigit().foregroundStyle(.secondary)
                }
                .accessibilityElement(children: .combine)
            }
        }
    }
}
