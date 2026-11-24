import Foundation
import Testing
@testable import AgentsKitCore

@Suite("Cycle time chart")
struct CycleTimeChartTests {
    private let pacific: Calendar = {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        return calendar
    }()

    private let agents = "github:alexec/agents"
    private let site = "github:alexec/agents-site"

    private var history: CycleTimeHistory {
        let today = [
            CycleTimeSample(cycle: 8 * 3600, coding: 2 * 3600, pr: 6 * 3600, level: "story", repo: agents),
            CycleTimeSample(cycle: 2 * 3600, coding: 0.5 * 3600, pr: 1.5 * 3600, level: "bug", repo: site)
        ]
        let bucket = CycleTimeBucket(samples: today, noStart: 1)
        return CycleTimeHistory(
            hours: ["2026-11-09T14": bucket],
            days: ["2026-11-09": bucket,
                   "2026-11-08": CycleTimeBucket(samples: [
                       CycleTimeSample(cycle: 3600, level: "task", repo: agents)
                   ])],
            now: "2026-11-09T20", timeZone: "America/Los_Angeles")
    }

    @Test("W is the last seven days, with the median, P85 and split over the range")
    func week() {
        let chart = CycleTimeChart(history, range: .week, calendar: pacific)
        #expect(chart.bars.count == 7)
        #expect(chart.bars.last?.stamp == "2026-11-09")
        #expect(chart.bars.last?.value == 5.0 * 3600)
        #expect(chart.bars.last?.count == 2)
        #expect(chart.bars.dropLast().last?.value == 3600)
        #expect(chart.count == 3)
        #expect(chart.noStart == 1)
        #expect(chart.median == 2 * 3600)
        #expect(chart.p85 == 8 * 3600)
        #expect(chart.coding == 1.25 * 3600)
        #expect(chart.pr == 3.75 * 3600)
        #expect(chart.noStartLine == "1 PR had no start recorded; not drawn.")
        #expect(chart.splitLine == "1.3h coding · 3.8h PR")
    }

    @Test("the shares group by level by default and by repository when asked")
    func shares() {
        let byLevel = CycleTimeChart(history, range: .week, calendar: pacific)
        #expect(byLevel.breakdown == .level)
        #expect(byLevel.shares.map(\.name) == ["Story", "Bug", "Task"])
        #expect(byLevel.shares.map(\.value) == [8 * 3600, 2 * 3600, 3600])

        let byRepo = CycleTimeChart(history, range: .week, breakdown: .repo, calendar: pacific)
        let values = byRepo.shares.map(\.value)
        let expected: [Double] = [4.5 * 3600, 2 * 3600]
        #expect(byRepo.shares.map(\.name) == ["alexec/agents", "alexec/agents-site"])
        #expect(values == expected)
        #expect(byRepo.shares.map(\.count) == [2, 1])
    }

    @Test("the repository picker filters the chart and keeps the no-start count global")
    func repositoryFilter() {
        let chart = CycleTimeChart(history, range: .week, breakdown: .repo, repo: agents, calendar: pacific)
        #expect(chart.repo == agents)
        #expect(chart.bars.last?.count == 1)
        #expect(chart.count == 2)
        #expect(chart.median == 4.5 * 3600)
        #expect(chart.p85 == 8 * 3600)
        #expect(chart.shares.map(\.name) == ["alexec/agents"])
        #expect(chart.shares.map(\.count) == [2])
        #expect(chart.noStart == 1)
        #expect(chart.noStartLine == "1 PR had no start recorded across all repositories; not drawn.")
        #expect(CycleTimeChart.repositories(in: history) == [agents, site])
    }

    @Test("D is today by the hour, and its figure is the hour's median")
    func day() {
        let chart = CycleTimeChart(history, range: .day, calendar: pacific)
        #expect(chart.bars.count == 24)
        #expect(chart.bars.first { $0.stamp == "2026-11-09T14" }?.value == 5.0 * 3600)
        #expect(chart.count == 2)
        #expect(chart.noStart == 1)
    }

    @Test("a range with nothing in it is empty")
    func empty() {
        let chart = CycleTimeChart(CycleTimeHistory(now: "2026-11-09T20"), range: .week, calendar: pacific)
        #expect(chart.isEmpty)
        #expect(chart.bars.count == 7)
        #expect(chart.shares.isEmpty)
        #expect(chart.median == 0)
        #expect(chart.splitLine == nil)
        #expect(chart.noStartLine == nil)
    }

    @Test("durations read as minutes, hours or days")
    func words() {
        #expect(CycleTimeChart.words(0) == "0s")
        #expect(CycleTimeChart.words(90) == "2m")
        #expect(CycleTimeChart.words(3600) == "1h")
        #expect(CycleTimeChart.words(6.2 * 3600) == "6.2h")
        #expect(CycleTimeChart.words(86400) == "1 day")
        #expect(CycleTimeChart.words(2.5 * 86400) == "2.5 days")
        #expect(CycleTimeChart.words(10 * 86400) == "10 days")
    }

    @Test("a host in another zone moves into the viewer's days")
    func merged() {
        let hour = CycleTimeBucket(samples: [
            CycleTimeSample(cycle: 3600, level: "task", repo: agents)
        ])
        let day = CycleTimeBucket(samples: [
            CycleTimeSample(cycle: 3600, level: "task", repo: agents)
        ])
        let utc = CycleTimeHistory(hours: ["2026-11-10T02": hour],
                                   days: ["2026-11-10": day],
                                   now: "2026-11-10T04", timeZone: "UTC")

        let here = CycleTimeHistory.merged([utc], calendar: pacific)
        #expect(here.days["2026-11-09"]?.samples.count == 1)
        #expect(here.days["2026-11-10"] == nil)
    }
}
