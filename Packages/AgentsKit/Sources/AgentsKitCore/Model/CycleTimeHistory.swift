import Foundation

/// The landed work's durations, by the hour and the day (#6648): `cycletime/history`, counted on
/// each ask from the host's lineage store. Each sample has the cycle, coding and PR durations
/// with the issue's repo and level, so the chart can take medians over any range.
public struct CycleTimeHistory: Codable, Sendable, Hashable {
    /// `yyyy-MM-ddTHH` in the host's own calendar.
    public var hours: [String: CycleTimeBucket]
    /// `yyyy-MM-dd` in the host's own calendar.
    public var days: [String: CycleTimeBucket]
    /// The host's hour when this was read.
    public var now: String
    /// The zone those stamps are in, an IANA identifier (#1267). Nil from a host too old
    /// to say, read as the viewer's own.
    public var timeZone: String?

    public init(hours: [String: CycleTimeBucket] = [:], days: [String: CycleTimeBucket] = [:],
                now: String = "", timeZone: String? = nil) {
        self.hours = hours
        self.days = days
        self.now = now
        self.timeZone = timeZone
    }
}

/// The work that landed in one hour or day: its duration samples and the merged PRs in it that
/// had no recorded start and so cannot be drawn.
public struct CycleTimeBucket: Codable, Sendable, Hashable {
    public var samples: [CycleTimeSample]
    public var noStart: Int

    public init(samples: [CycleTimeSample] = [], noStart: Int = 0) {
        self.samples = samples
        self.noStart = noStart
    }

    public mutating func add(_ other: CycleTimeBucket) {
        samples.append(contentsOf: other.samples)
        noStart += other.noStart
    }
}

/// One issue that landed: cycle is from the start of work (#3562) to its last PR's merge.
/// Coding is from that start to PR creation (#4553), and pr from creation to the merge; both
/// are nil when the PR's opening was not recorded, and then do not add to cycle.
public struct CycleTimeSample: Codable, Sendable, Hashable {
    public var cycle: Double
    public var coding: Double?
    public var pr: Double?
    public var level: String
    public var repo: String

    public init(cycle: Double, coding: Double? = nil, pr: Double? = nil, level: String = "", repo: String = "") {
        self.cycle = cycle
        self.coding = coding
        self.pr = pr
        self.level = level
        self.repo = repo
    }
}

extension CycleTimeHistory {
    /// This host's history moved into the viewer's calendar (#1267), as Velocity's is.
    public static func merged(_ histories: [CycleTimeHistory], calendar: Calendar = .current) -> CycleTimeHistory {
        let tiers = HistoryMerge.merged(
            histories.enumerated().map { index, history in
                let host = String(index)
                return HistoryMerge.Tiers(
                    hours: history.hours.mapValues { [host: $0] },
                    days: history.days.mapValues { [host: $0] },
                    now: history.now, timeZone: history.timeZone)
            },
            calendar: calendar, empty: CycleTimeBucket(), add: { $0.add($1) })
        func flatten(_ tiers: [String: [String: CycleTimeBucket]]) -> [String: CycleTimeBucket] {
            tiers.mapValues { buckets in
                buckets.values.reduce(into: CycleTimeBucket()) { result, bucket in result.add(bucket) }
            }
        }
        return CycleTimeHistory(hours: flatten(tiers.hours), days: flatten(tiers.days),
                                now: tiers.now, timeZone: tiers.timeZone)
    }
}
