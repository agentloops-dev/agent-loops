import AgentsKitCore
import Charts
import SwiftUI

enum ChartReadResult<History> {
    case read(History, Date)
    case failed
    case cancelled
}

@MainActor func readChart<History>(_ load: () async -> History?) async -> ChartReadResult<History> {
    if let loaded = await load() { return .read(loaded, Date()) }
    return Task.isCancelled ? .cancelled : .failed
}

func chartPickedBar<Bar>(in bars: [Bar], at picked: Date?, start: (Bar) -> Date) -> Bar? {
    guard let picked else { return nil }
    return bars.last { start($0) <= picked }
}

struct RepositoryOption: Identifiable {
    let key: String
    let name: String

    var id: String { key }

    static func options(_ keys: [String]) -> [RepositoryOption] {
        keys.map { RepositoryOption(key: $0, name: VelocityChart.repoName($0)) }
    }
}

struct RepositoryPicker: View {
    @Binding var repo: String
    let options: [RepositoryOption]

    var body: some View {
        Picker("Repository", selection: $repo) {
            Text(VelocityChart.allRepositories).tag("")
            ForEach(options) { option in
                Text(option.name).tag(option.key)
            }
        }
        .pickerStyle(.menu)
        .labelsHidden()
        .disabled(options.isEmpty)
    }
}

struct ChartEmpty: View {
    var body: some View {
        Text("No pull requests landed in this range.")
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, minHeight: 160)
    }
}

enum ChartRangeParts {
    static func when(range: UsageChart.Range, picked: Date?, first: Date?, last: Date?) -> String {
        if let picked {
            switch range {
            case .day: return picked.formatted(.dateTime.hour())
                + "–" + picked.addingTimeInterval(3600).formatted(.dateTime.hour())
            case .week, .month: return picked.formatted(.dateTime.weekday(.wide).day().month())
            case .threeMonths: return "Week of " + picked.formatted(.dateTime.day().month())
            }
        }
        guard let first, let last else { return "" }
        if range == .day { return "Today" }
        let end = range == .threeMonths ? Calendar.current.date(byAdding: .day, value: 6, to: last) ?? last : last
        return (first ..< end.addingTimeInterval(1)).formatted(.interval.day().month().year())
    }

    static func unit(_ range: UsageChart.Range) -> Calendar.Component {
        switch range {
        case .day: .hour
        case .week, .month: .day
        case .threeMonths: .weekOfYear
        }
    }

    static func axisFormat(_ range: UsageChart.Range) -> Date.FormatStyle {
        switch range {
        case .day: .dateTime.hour()
        case .week: .dateTime.weekday(.abbreviated)
        case .month, .threeMonths: .dateTime.day().month(.abbreviated)
        }
    }
}

struct ChartHeadline: View {
    let title: String
    let value: String
    let when: String
    var detail: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title.uppercased())
                .appText(.fine).fontWeight(.semibold)
                .foregroundStyle(.secondary)
            Text(value)
                .appText(.title).fontWeight(.semibold)
                .monospacedDigit()
            Text(when)
                .appText(.supporting)
                .foregroundStyle(.secondary)
            if let detail {
                Text(detail)
                    .appText(.supporting)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

struct ChartReadFailureBanner: View {
    let read: Date
    let message: String
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            LastUpdateTimeline(read: read) { now in
                Text(LastUpdate.banner(message + ".", read: read, now: now))
                    .appText(.fine)
                    .foregroundStyle(Paper.secondary)
                    .lineLimit(1)
            }
            Button(WorkLook.tryAgain, action: retry).buttonStyle(.paper)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

struct ChartLoadingOrFailure<Content: View>: View {
    let failed: Bool
    let hasHistory: Bool
    let message: String
    let retry: () -> Void
    let content: Content

    init(failed: Bool, hasHistory: Bool, message: String, retry: @escaping () -> Void,
         @ViewBuilder content: () -> Content) {
        self.failed = failed
        self.hasHistory = hasHistory
        self.message = message
        self.retry = retry
        self.content = content()
    }

    var body: some View {
        if hasHistory {
            content
        } else if failed {
            ContentUnavailableView {
                Label(message, icon: "exclamationmark.triangle")
            } actions: {
                Button(WorkLook.tryAgain, action: retry).buttonStyle(.paper)
            }
            .frame(maxWidth: .infinity, minHeight: 200)
        } else {
            ProgressView().frame(maxWidth: .infinity, minHeight: 200)
        }
    }
}

extension View {
    func chartSelectionAndAxes(range: UsageChart.Range, selected: Binding<Date?>, yLabel: String,
                               yText: @escaping (Double) -> Text?) -> some View {
        self.chartXSelection(value: selected)
            .chartYAxis {
                AxisMarks(position: .trailing) { value in
                    if let number = value.as(Double.self), let label = yText(number) {
                        AxisGridLine()
                        AxisValueLabel { label }
                    }
                }
            }
            .chartXAxis {
                AxisMarks(values: .automatic(desiredCount: range == .day ? 4 : 6)) { _ in
                    AxisGridLine()
                    AxisValueLabel(format: ChartRangeParts.axisFormat(range))
                }
            }
            .frame(height: 200)
    }
}
