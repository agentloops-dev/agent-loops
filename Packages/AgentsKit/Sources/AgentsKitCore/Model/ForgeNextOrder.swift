import Foundation

public extension DaemonAPI.ForgeItem {
    /// Its place in the order of `forge/next` (#5689, epic #6086), the lowest first: not on hold
    /// before on hold, then High, Medium, none, Low. A list breaks a tie by its own order.
    var nextRank: Int {
        let priority = switch self.priority?.lowercased() {
        case "high": 0
        case "medium": 1
        case "low": 3
        default: 2
        }
        return (onHold == true ? 4 : 0) + priority
    }
}
