import AgentsKitCore
import Foundation
import Testing

/// Features show their maturity (#6052): a capability that `Packages/Wire/maturity.json` does
/// not list is GA; one that it lists has a badge, and a toggle in Features, on by default
/// and kept in the store of the feature flags (#6254).
struct MaturityTests {
    private let list = [
        EarlyFeature(capability: "chat.branch/1", level: .beta, title: "Branches", issue: 1, clients: nil),
        EarlyFeature(capability: "work.cache/1", level: .pilot, title: "Work cache", issue: 2, clients: [.mac]),
    ]

    @Test func aCapabilityThatIsNotListedIsGAAndShowsNoBadge() {
        #expect(EarlyFeatures.level(of: "host.wire/1", in: list) == .ga)
        #expect(EarlyFeatures.level(of: "chat.branch/1", in: list) == .beta)
        #expect(Maturity.ga.badge == nil && Maturity.ga.help == nil)
        for level in [Maturity.pilot, .alpha, .beta] {
            #expect(level.badge != nil)
            #expect(level.help?.hasPrefix((level.badge ?? "-") + ": ") == true, "the help line starts with its badge")
        }
        #expect(Maturity.pilot < .alpha && .alpha < .beta && .beta < .ga)
    }

    @Test func aPilotShowsOnlyOnItsClients() {
        #expect(EarlyFeatures.rows(on: .mac, in: list).map(\.capability) == ["chat.branch/1", "work.cache/1"])
        #expect(EarlyFeatures.rows(on: .remote, in: list).map(\.capability) == ["chat.branch/1"])
        let defaults = MemoryDefaults()
        #expect(EarlyFeatures.shows("work.cache/1", on: .mac, in: defaults, list: list))
        #expect(!EarlyFeatures.shows("work.cache/1", on: .web, in: defaults, list: list))
        #expect(EarlyFeatures.shows("host.wire/1", on: .web, in: defaults, list: list), "GA shows everywhere")
    }

    @Test func aFeatureTurnedOffShowsNowhereAndOnlyTheOffIsKept() {
        let defaults = MemoryDefaults()
        let branch = list[0]
        #expect(branch.key == "flag:chat:branch/1")
        #expect(EarlyFeatures.shows(branch.capability, on: .remote, in: defaults, list: list), "on by default")
        defaults.set(false, forKey: branch.key)
        #expect(!EarlyFeatures.shows(branch.capability, on: .remote, in: defaults, list: list))
        #expect(!EarlyFeatures.shows(branch.capability, on: .mac, in: defaults, list: list))
        #expect(EarlyFeatures.change(branch.capability, on: false) == [branch.key: .bool(false)])
        #expect(EarlyFeatures.change(branch.capability, on: true) == [branch.key: .null])
    }

    @Test func everyListedCapabilityIsMirroredAsABool() {
        // wiregen checks each name against the capabilities Packages/Wire names.
        for feature in EarlyFeature.all {
            #expect(DisplayPreference.mirrored[feature.key] == .bool)
            #expect(feature.level != .ga && !feature.title.isEmpty && feature.issue > 0)
        }
    }

    @Test func aClientOlderThanItsHostShowsTheHostsLevel() {
        // A host that says none is older than #6579: this client's own list.
        #expect(EarlyFeatures.level(of: "chat.branch/1", said: nil, in: list) == .beta)
        // One that says its levels: not named is GA there, named is its level.
        #expect(EarlyFeatures.level(of: "chat.branch/1", said: [:], in: list) == .ga)
        #expect(EarlyFeatures.level(of: "host.wire/1", said: ["host.wire/1": "alpha"], in: list) == .alpha)
        // A level this client does not know: its own list.
        #expect(EarlyFeatures.level(of: "chat.branch/1", said: ["chat.branch/1": "later"], in: list) == .beta)
    }

    @Test func whereNoOnePartRunsItTheLowestLevelShows() {
        let parts: [[String: String]?] = [[:], ["chat.branch/1": "alpha"], nil]
        #expect(EarlyFeatures.lowest(of: "chat.branch/1", said: parts, in: list) == .alpha)
        #expect(EarlyFeatures.lowest(of: "chat.branch/1", said: [], in: list) == .beta, "no part: its own list")
        let rows = EarlyFeatures.rows(on: .mac, said: parts, in: list)
        #expect(rows.map(\.capability) == ["chat.branch/1", "work.cache/1"])
        #expect(rows.map(\.level) == [.alpha, .pilot])
        // Every part says GA: no row.
        #expect(EarlyFeatures.rows(on: .remote, said: [[:]], in: list).isEmpty)
    }
}
