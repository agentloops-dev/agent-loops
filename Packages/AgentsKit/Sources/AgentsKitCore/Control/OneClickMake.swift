import Foundation

/// New Droplet (#6577, epic #6070 W3 to W5): on a connected DigitalOcean provider, one click
/// makes a machine with what New Machine would make (the region and size used last there,
/// else the nearest region and the recommended size, #6073). The card says where and what it
/// costs before the click; Change… opens the whole form. The web page's `oneClickMake.ts`
/// says the same words.
public enum OneClickMake {
    /// The preset whose card offers it: DigitalOcean Droplets over its API.
    public static let preset = "digitalocean-api"

    /// The button's words.
    public static let button = "New Droplet"
    /// The button that opens the whole form.
    public static let change = "Change…"

    /// Whether the provider's card offers New Droplet.
    public static func offers(_ provider: DaemonAPI.ProviderSummary) -> Bool {
        provider.preset == preset && provider.capabilities.contains("create")
    }

    /// What one click makes: its name, the options, and the line beside the button.
    public struct Plan: Equatable, Sendable {
        public var name: String
        public var values: [String: JSONValue]
        /// Where and what, with the monthly price: "San Francisco 3 · 2 vCPUs · 4 GB · 80 GB
        /// disk · $24/month".
        public var summary: String

        public init(name: String, values: [String: JSONValue], summary: String) {
            self.name = name
            self.values = values
            self.summary = summary
        }

        /// The note under the line: the money, in the epic's W3 words.
        public var note: String {
            "\(OneClickMake.button) makes \(name) there. DigitalOcean charges for it while it exists, also when it is off: delete it to stop paying."
        }
    }

    /// The plan from the provider's New Machine form (`providers/options`): its values, each
    /// enum on its first choice where none is given, named by the form's `x-titles`. Nil when
    /// the form gives no name.
    public static func plan(_ options: DaemonAPI.ProviderOptions) -> Plan? {
        guard let name = options.name, !name.isEmpty else { return nil }
        let properties = options.schema["properties"]?.objectValue ?? [:]
        var values = options.values?.objectValue ?? [:]
        for (key, property) in properties where values[key] == nil {
            if let first = property["enum"]?.arrayValue?.first { values[key] = first }
            else if let given = property["default"] { values[key] = given }
        }
        var parts: [String] = []
        for key in ["region", "size"] {
            guard let chosen = values[key]?.stringValue else { continue }
            let property = properties[key]
            if let title = property?["x-titles"]?[chosen]?.stringValue {
                parts.append(title)
            } else if let cost = property?["x-costs"]?[chosen]?.stringValue {
                parts.append("\(chosen) · \(cost)")
            } else {
                parts.append(chosen)
            }
        }
        return Plan(name: name, values: values, summary: parts.joined(separator: " · "))
    }

    /// The line on a machine that New Droplet made here once it joined (W5).
    public static func ready(_ name: String) -> String { "\(name) is ready. Agents can work on it now." }
    /// W5's button: the machine's page, where a folder is added and a session started.
    public static let openMachine = "Open Machine"
}
