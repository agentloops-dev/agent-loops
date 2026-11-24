import Foundation

/// How long archived agents are kept before they are deleted (051, #398).
///
/// The person's, kept by the daemon. Forever is only an older host's: one from #6706 on
/// keeps no conversation past 90 days, and says 90 days for it.
public struct RetentionSettings: Codable, Hashable, Sendable {
    public var keepFor: KeepFor

    public init(keepFor: KeepFor = .days30) {
        self.keepFor = keepFor
    }

    /// Nothing is deleted, however old.
    public var isOff: Bool { keepFor == .forever }

    public enum KeepFor: String, Codable, Hashable, Sendable, CaseIterable {
        case days7, days14, days30, days90, forever

        /// The choices: at most 90 days (#6677). Forever only while an older host says it.
        public static func offered(with current: KeepFor) -> [KeepFor] {
            allCases.filter { $0 != .forever || current == .forever }
        }

        public var interval: TimeInterval? {
            switch self {
            case .days7: return 7 * 86_400
            case .days14: return 14 * 86_400
            case .days30: return 30 * 86_400
            case .days90: return 90 * 86_400
            case .forever: return nil
            }
        }

        /// A value a newer build wrote is the default, never Forever: a file this build
        /// cannot read must not turn deletion off on the person's behalf.
        public init(from decoder: any Decoder) throws {
            self = KeepFor(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .days30
        }
    }

    /// A file from before #398 also has `cap`, the size limit that was dropped; it is
    /// not read.
    public init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        keepFor = (try? c.decode(KeepFor.self, forKey: .keepFor)) ?? .days30
    }
}
