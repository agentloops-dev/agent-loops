import Foundation

/// Labels on a machine (#5284): a key, or key=value. A provider sets `os`, `arch` and
/// `provider`; a person adds the rest (`gpu`, `team=a`) on a machine's page, or on many
/// machines at once. A workflow's Runs on names them (spec 736 FR-034).
public enum MachineLabels {
    /// The keys only a provider sets.
    public static let providerKeys: Set<String> = ["os", "arch", "provider"]

    public static let hint = "A key or key=value, like gpu or team=a. Workflows can run on machines with a label."

    /// What a person typed as a list of labels: split on commas and spaces, trimmed, without repeats.
    public static func parse(_ text: String) -> [String] {
        var seen: Set<String> = []
        return text.split(whereSeparator: { $0 == "," || $0.isWhitespace }).map(String.init).filter {
            seen.insert($0.lowercased()).inserted
        }
    }

    /// Why a label cannot be used, in words; nil when it can.
    public static func problem(_ label: String) -> String? {
        let key = label.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false).first.map(String.init) ?? label
        if providerKeys.contains(key.lowercased()) { return "\(key) is set by the machine’s provider. Pick another key." }
        let word = #"^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}(=[A-Za-z0-9._/:@+-]{1,63})?$"#
        if label.range(of: word, options: .regularExpression) == nil {
            return "“\(label)” is not a label. Use a key or key=value of letters, digits and . _ / -."
        }
        return nil
    }

    /// Every label on these machines, once each and sorted, for the Runs on section and the filters.
    public static func all(_ machines: [DaemonAPI.ControlMachine]) -> [String] {
        var seen: Set<String> = []
        var out: [String] = []
        for label in machines.flatMap({ $0.labels ?? [] }) where seen.insert(label.lowercased()).inserted { out.append(label) }
        return out.sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }
    }
}
