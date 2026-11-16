import Foundation

/// A list of machine names stops at three: "a, b, c and 97 more" (#5289, #5620, epic #5281). The web page says the
/// same words (`Web/src/model/machineNames.ts`), and its "and N more" opens the Machines page with that set as its
/// filter. The Machines page is Web only, so the window and the Remote say the words with no link.
public enum MachineNames {
    public static let limit = 3

    /// "a, b, c" for up to three names, then "a, b, c and 97 more".
    public static func words(_ names: [String]) -> String {
        if names.count <= limit { return names.joined(separator: ", ") }
        return names.prefix(limit).joined(separator: ", ") + " and \(more(names.count)) more"
    }

    /// "a, b and c" for up to three names, then "a, b, c and 97 more", for a line that says "and" before the last.
    public static func and(_ names: [String]) -> String {
        if names.count > limit { return words(names) }
        guard let last = names.last, names.count > 1 else { return names.first ?? "" }
        return names.dropLast().joined(separator: ", ") + " and " + last
    }

    /// How many names "and N more" counts; 0 when all are said.
    public static func more(_ count: Int) -> Int { max(0, count - limit) }
}
