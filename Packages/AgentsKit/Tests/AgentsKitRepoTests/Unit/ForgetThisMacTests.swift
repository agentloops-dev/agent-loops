import Foundation
import Testing

/// The Mac app has no unit-test target. Keep its Forget This Mac cleanup wiring covered
/// from the repository test target, alongside the stores' behavioral unit tests.
@Suite("Forget This Mac erasure")
struct ForgetThisMacTests {
    private static let root = URL(filePath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent()

    @Test func forgetClearsTheLocalCopyAndWorkCache() throws {
        let model = try String(contentsOf: Self.root.appending(path: "App/Sources/AppModel.swift"), encoding: .utf8)
        let forget = try #require(model.range(of: "func leaveControlPlane(forgetLocalData: Bool = false) async"))
        let body = model[forget.lowerBound...]
        #expect(body.contains("if forgetLocalData"))
        #expect(body.contains("await localCopy?.forget()"))
        #expect(body.contains("await workCache?.forget()"))
    }

    @Test func forgetRemovesTheSavedLocalControlPlanePairing() throws {
        let config = try String(contentsOf: Self.root.appending(path: "App/Sources/Control/ControlConfig.swift"), encoding: .utf8)
        let forget = try #require(config.range(of: "static func forget()"))
        let body = config[forget.lowerBound...]
        #expect(body.contains("removeItem(at: membershipFile)"))
        #expect(body.contains("removeItem(at: herePairingFile)"))
    }
}
