import Foundation
import Testing

/// In `Unit/`, not `Support/`: every test target links `Support/`, so a suite there runs
/// once for each target (#5630).
@Suite("Memory defaults")
struct MemoryDefaultsTests {
    @Test func keepsWhatIsSetAndNothingElse() {
        let defaults = MemoryDefaults()
        defaults.set(Data([1, 2]), forKey: "data")
        defaults.set(["a", "b"], forKey: "strings")
        #expect(defaults.data(forKey: "data") == Data([1, 2]))
        #expect(defaults.stringArray(forKey: "strings") == ["a", "b"])
        #expect(Set(defaults.dictionaryRepresentation().keys) == ["data", "strings"],
                "nothing from the app's own domains")
        defaults.removeObject(forKey: "data")
        #expect(defaults.data(forKey: "data") == nil)
        #expect(MemoryDefaults().stringArray(forKey: "strings") == nil, "each is its own")
    }

    /// No test makes an on-disk suite: each one is a plist that outlives the run.
    @Test func noTestMakesADefaultsSuiteOnDisk() throws {
        // Tests/, so every test target is read; the links to Support/ are not followed.
        let tests = URL(filePath: #filePath)
            .deletingLastPathComponent()      // Unit
            .deletingLastPathComponent()      // AgentsKitTests
            .deletingLastPathComponent()      // Tests
        let files = try #require(FileManager.default.enumerator(at: tests, includingPropertiesForKeys: nil))
        let offenders = files.compactMap { $0 as? URL }
            .filter { $0.pathExtension == "swift" && !["MemoryDefaults.swift", "MemoryDefaultsTests.swift"].contains($0.lastPathComponent) }
            .filter { (try? String(contentsOf: $0, encoding: .utf8))?.contains("UserDefaults(suiteName:") == true }
            .map(\.lastPathComponent)
        #expect(offenders.isEmpty, "use MemoryDefaults(): \(offenders)")
    }
}
