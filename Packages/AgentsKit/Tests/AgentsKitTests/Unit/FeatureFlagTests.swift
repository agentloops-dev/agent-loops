import AgentsKitCore
import Foundation
import Testing

/// The per-person feature flags (#6254): kept as `flag:<name>` in the display settings, off
/// when absent, and mirrored into each client's defaults.
struct FeatureFlagTests {
    @Test func aFlagIsOnOnlyWhenThePersonTurnedItOn() {
        let flag = FeatureFlag.commitTrailers
        #expect(flag.key == "flag:commit-trailers")
        #expect(!FeatureFlags.key("chat.branch").contains("."), "@AppStorage hears no key with a dot")
        #expect(!flag.isOn(in: [:]))
        #expect(flag.isOn(in: [flag.key: .bool(true)]))
        #expect(!flag.isOn(in: [flag.key: .bool(false)]))
        #expect(!flag.isOn(in: [flag.key: .string("true")]))
    }

    @Test func aToggleOfAnyNameKeepsOnlyWhatDiffersFromItsDefault() {
        // A capability's toggle (#6052) is on by default: the store keeps only an off.
        #expect(FeatureFlags.isOn("chat.branch", default: true, in: [:]))
        #expect(FeatureFlags.change("chat.branch", on: false, default: true) == ["flag:chat:branch": .bool(false)])
        #expect(FeatureFlags.change("chat.branch", on: true, default: true) == ["flag:chat:branch": .null])
        #expect(FeatureFlags.change("commit-trailers", on: true) == ["flag:commit-trailers": .bool(true)])
    }

    @Test func everyFlagIsMirroredAsABool() {
        for flag in FeatureFlag.allCases {
            #expect(DisplayPreference.mirrored[flag.key] == .bool)
            #expect(!flag.title.isEmpty && !flag.about.isEmpty && flag.issue > 0)
        }
        #expect(DisplayPreference.mirrored[DisplayPreference.smartGroupsOff] == .string)
    }

    @MainActor
    @Test func theMirrorReadsAFlagIntoTheDefaults() {
        let defaults = MemoryDefaults()
        let mirror = DisplayPreferenceMirror(defaults: defaults, send: { $0 }, failed: { _ in })
        let flag = FeatureFlag.commitTrailers
        mirror.hostSaid([flag.key: .bool(true)])
        #expect(flag.isOn(in: defaults))
        mirror.hostSaid([:])
        #expect(!flag.isOn(in: defaults))
    }

    /// A model's flags follow the mirror with no restart (#6572): the four features off by default.
    @MainActor
    @Test func aModelsFlagsFollowTheMirror() {
        let defaults = MemoryDefaults()
        let states = FeatureFlagStates(defaults: defaults)
        let mirror = DisplayPreferenceMirror(defaults: defaults, send: { $0 }, failed: { _ in })
        // A UserDefaults posts this for itself; the test's stand-in does not.
        func said(_ preferences: DaemonAPI.DisplayPreferences) {
            mirror.hostSaid(preferences)
            NotificationCenter.default.post(name: UserDefaults.didChangeNotification, object: defaults)
        }
        let hidden: [FeatureFlag] = [.dashboard, .docs, .suggested, .work]
        said([:])
        #expect(hidden.allSatisfy { !states.isOn($0) })
        said([FeatureFlag.work.key: .bool(true)])
        #expect(states.on == [.work])
        said([:])
        #expect(states.on.isEmpty)
    }
}
