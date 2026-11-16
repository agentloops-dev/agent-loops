import Foundation

/// A list of machine names stops at three: "a, b, c and 97 more" (#5289, epic #5281). The web page says the
/// same words (`Web/src/model/machineNames.ts`), and its "and N more" opens the Machines page with that set as
/// its filter; the window and the Remote say the words, and the link follows in #5289's per-client issues.
enum MachineNames {
    static let limit = 3

    /// "a, b, c" for up to three names, then "a, b, c and 97 more".
    static func words(_ names: [String]) -> String {
        if names.count <= limit { return names.joined(separator: ", ") }
        return names.prefix(limit).joined(separator: ", ") + " and \(names.count - limit) more"
    }
}
