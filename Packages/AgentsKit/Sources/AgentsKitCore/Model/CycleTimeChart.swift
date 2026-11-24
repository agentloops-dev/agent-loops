import Foundation

/// The bars of the Cycle time page (#6648): the time from work starting to a PR landing, worked
/// out here as `VelocityChart` is for Velocity. Each bar is the median of the work that landed
/// in it; the line across them is the whole range's P85.
public struct CycleTimeChart: Sendable, Hashable {
    public typealias Range = UsageChart.Range

    public enum Breakdown: String, CaseIterable, Identifiable, Sendable {
        case level = "By level"
        case repo = "By repository"
        public var id: String { rawValue }
    }

    public struct Bar: Sendable, Hashable, Identifiable {
        /// The first stamp the bar covers: an hour, a day, or a week's first day.
        public let stamp: String
        public let start: Date
        /// The median cycle time of the work that landed in it, in seconds.
        public let value: Double
        /// The work that landed in it, for the picked bar's figure.
        public let count: Int
        public var id: String { stamp }
    }

    public struct Share: Sendable, Hashable, Identifiable {
        public let name: String
        /// The median cycle time of this group in the range, in seconds.
        public let value: Double
        public let count: Int
        public var id: String { name }
    }

    public let range: Range
    public let breakdown: Breakdown
    public let repo: String?
    public let bars: [Bar]
    public let shares: [Share]
    /// The whole range's median and P85 cycle time, in seconds.
    public let median: Double
    public let p85: Double
    /// The work that landed in the range, and the merged PRs with no recorded start.
    public let count: Int
    public let noStart: Int
    /// The medians of the split, in seconds, for landings whose PR opening was recorded.
    public let coding: Double?
    public let pr: Double?

    public init(_ history: CycleTimeHistory, range: Range,
                breakdown: Breakdown = .level, repo: String? = nil,
                calendar: Calendar = .current) {
        self.range = range
        self.breakdown = breakdown
        self.repo = repo
        let now = UsageChart.date(history.now, calendar: calendar) ?? Date()
        let slots = UsageChart.slots(range, today: calendar.startOfDay(for: now), calendar: calendar)
        let tier = range == .day ? history.hours : history.days

        var bars: [Bar] = []
        var all: [CycleTimeSample] = []
        var noStart = 0
        for slot in slots {
            var samples: [CycleTimeSample] = []
            for key in slot.keys {
                guard let bucket = tier[key] else { continue }
                samples.append(contentsOf: bucket.samples.filter { repo == nil || $0.repo == repo })
                noStart += bucket.noStart
            }
            all.append(contentsOf: samples)
            bars.append(Bar(stamp: slot.stamp, start: slot.start,
                            value: Self.median(samples.map(\.cycle)) ?? 0, count: samples.count))
        }
        self.bars = bars
        self.noStart = noStart
        self.count = all.count
        self.median = Self.median(all.map(\.cycle)) ?? 0
        self.p85 = Self.percentile(0.85, all.map(\.cycle)) ?? 0
        self.coding = Self.median(all.compactMap(\.coding))
        self.pr = Self.median(all.compactMap(\.pr))

        let groups = Dictionary(grouping: all) { sample -> String in
            switch breakdown {
            case .level: return sample.level.isEmpty ? "No level" : sample.level.capitalized
            case .repo: return VelocityChart.repoName(sample.repo)
            }
        }
        self.shares = groups.map { name, group in
            Share(name: name, value: Self.median(group.map(\.cycle)) ?? 0, count: group.count)
        }.sorted { $0.value != $1.value ? $0.value > $1.value : $0.name < $1.name }
    }

    public var isEmpty: Bool { count == 0 }

    /// The figure above the bars: the whole range's median, not an average of medians.
    public static let headline = "Median cycle time"

    /// A duration for the eye: *38m*, *6.2h*, *2.4 days*.
    public static func words(_ seconds: Double) -> String {
        if seconds < 60 { return "\(Int(seconds.rounded()))s" }
        if seconds < 3600 { return "\(Int((seconds / 60).rounded()))m" }
        if seconds < 86400 { return duration(seconds / 3600, unit: "h") }
        let days = seconds / 86400
        if days == 1 { return "1 day" }
        return duration(days, unit: " days")
    }

    private static func duration(_ value: Double, unit: String) -> String {
        if value >= 10 || value == value.rounded() { return "\(Int(value.rounded()))\(unit)" }
        let tenths = (value * 10).rounded() / 10
        return "\(tenths.formatted(.number.precision(.fractionLength(1))))\(unit)"
    }

    /// The line under the bars: how many merged PRs had no recorded start, if any.
    public var noStartLine: String? {
        let across = repo == nil ? "" : " across all repositories"
        switch noStart {
        case 0: return nil
        case 1: return "1 PR had no start recorded\(across); not drawn."
        default: return "\(noStart.formatted()) PRs had no start recorded\(across); not drawn."
        }
    }

    /// The split's line: how much of the median was coding and how much was PR review.
    public var splitLine: String? {
        guard let coding, let pr else { return nil }
        return "\(Self.words(coding)) coding · \(Self.words(pr)) PR"
    }

    static func median(_ values: [Double]) -> Double? {
        guard !values.isEmpty else { return nil }
        let sorted = values.sorted()
        let middle = sorted.count / 2
        return sorted.count.isMultiple(of: 2) ? (sorted[middle - 1] + sorted[middle]) / 2 : sorted[middle]
    }

    static func percentile(_ fraction: Double, _ values: [Double]) -> Double? {
        guard !values.isEmpty else { return nil }
        let sorted = values.sorted()
        let rank = Int((Double(sorted.count) * fraction).rounded(.up))
        return sorted[max(0, min(sorted.count - 1, rank - 1))]
    }

    public static func repositories(in history: CycleTimeHistory) -> [String] {
        let keys = Set(history.hours.values.flatMap { $0.samples.map(\.repo) })
            .union(history.days.values.flatMap { $0.samples.map(\.repo) })
        return keys.sorted { VelocityChart.repoName($0) < VelocityChart.repoName($1) }
    }
}
