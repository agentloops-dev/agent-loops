import AgentsKitCore
import Charts
import SwiftUI

/// The pull requests that landed, over time (#4595): D / W / M / 3M bars of the PRs the
/// Multiplier counts, the daily average above them, a picked bar's own figure, and the shares
/// by repository, drawn as Cost, Tokens and Time are.
///
/// The Velocity page of the window (#4597) and the Remote (#4758); the web page draws the same
/// in VelocityCharts.tsx (#4598). The bars are worked out in `VelocityChart`, where they are
/// tested; this only draws them.
///
/// The range control and the chart, fetching through `load`, again whenever `refresh` changes.
struct VelocityChartsSection<Refresh: Equatable>: View {
    let load: () async -> VelocityHistory?
    let refresh: Refresh

    @State private var history: VelocityHistory?
    /// When `history` was read (#5144).
    @State private var read: Date?
    private var pageRead: Date? { read }
    @State private var range: VelocityChart.Range = .week
    @State private var repo = ""
    @State private var picked: Date?
    /// The last read failed (#5972): with no history, the page says so in place of the spinner;
    /// with one, the line at the top says so beside its age.
    @State private var failed = false
    /// Try again's count, which reads again.
    @State private var attempt = 0

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let read = pageRead {
                if failed, history != nil {
                    ChartReadFailureBanner(read: read, message: Self.couldNotRead) { attempt += 1 }
                } else {
                    // How old what the page shows is (#5144): the oldest of its reads, at the top.
                    LastUpdateText(read: read).frame(maxWidth: .infinity, alignment: .trailing)
                }
            }
            Picker("Range", selection: $range) {
                ForEach(VelocityChart.Range.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            RepositoryPicker(repo: $repo,
                             options: history.map { RepositoryOption.options(VelocityChart.repositories(in: $0)) } ?? [])
            ChartLoadingOrFailure(failed: failed, hasHistory: history != nil,
                                  message: Self.couldNotRead, retry: { attempt += 1 }) {
                if let history {
                    VelocityChartBody(chart: VelocityChart(history, range: range,
                                                            repo: repo.isEmpty ? nil : repo),
                                      picked: $picked)
                }
            }
        }
        .onChange(of: range) { picked = nil }
        .onChange(of: repo) { picked = nil }
        .task(id: Reading(refresh: refresh, attempt: attempt)) {
            switch await readChart(load) {
            case let .read(loaded, at):
                history = loaded
                read = at
                failed = false
            case .failed: failed = true
            case .cancelled: break
            }
        }
    }

    static var couldNotRead: String { "Could not read velocity" }

    /// What starts a read: a change of `refresh`, or Try again.
    private struct Reading: Equatable {
        let refresh: Refresh
        let attempt: Int
    }
}

private struct VelocityChartBody: View {
    let chart: VelocityChart
    @Binding var picked: Date?

    private var pickedBar: VelocityChart.Bar? {
        chartPickedBar(in: chart.bars, at: picked) { $0.start }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            headline
            if chart.isEmpty {
                ChartEmpty()
            } else {
                bars
                Text(chart.revertsLine)
                    .appText(.fine)
                    .foregroundStyle(.secondary)
                shares
            }
        }
    }

    private var headline: some View {
        ChartHeadline(
            title: pickedBar == nil ? VelocityChart.headline : "Total",
            value: VelocityChart.words(pickedBar?.value ?? chart.average),
            when: ChartRangeParts.when(range: chart.range, picked: pickedBar?.start,
                                       first: chart.bars.first?.start, last: chart.bars.last?.start))
    }

    private var bars: some View {
        Chart {
            ForEach(chart.bars) { bar in
                BarMark(x: .value("When", bar.start, unit: ChartRangeParts.unit(chart.range)), y: .value("PRs", bar.value))
                    .foregroundStyle(Paper.accent.opacity(pickedBar == nil || pickedBar == bar ? 1 : 0.35))
                    .cornerRadius(2)
            }
            if chart.range != .day, pickedBar == nil, chart.average > 0 {
                RuleMark(y: .value("Average", perBar(chart.average)))
                    .foregroundStyle(.secondary)
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
            }
        }
        .chartSelectionAndAxes(range: chart.range, selected: $picked, yLabel: "PRs") { number in
            guard number == number.rounded() else { return nil }
            return Text(Int(number).formatted())
        }
    }

    /// 3M's bars are weeks, so its daily average is drawn as a week's worth.
    private func perBar(_ daily: Double) -> Double { chart.range == .threeMonths ? daily * 7 : daily }

    /// Where the range's PRs landed, by repository, most first.
    private var shares: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(chart.shares) { share in
                HStack(alignment: .firstTextBaseline) {
                    Text(share.name)
                    Spacer(minLength: 24)
                    Text(share.merged.formatted()).monospacedDigit()
                }
                .accessibilityElement(children: .combine)
            }
        }
    }
}
