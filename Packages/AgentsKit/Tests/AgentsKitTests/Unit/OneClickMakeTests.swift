import Foundation
import Testing
@testable import AgentsKitCore

/// New Droplet on a DigitalOcean provider's card (#6577): what one click makes, in words.
@Suite("New Droplet, in words")
struct OneClickMakeTests {
    private func options(_ json: String) throws -> DaemonAPI.ProviderOptions {
        try JSONDecoder().decode(DaemonAPI.ProviderOptions.self, from: Data(json.utf8))
    }

    /// #6073's form: regions and sizes with titles and prices, the recommended size the default.
    private let form = """
    {"schema": {"type": "object", "properties": {
      "region": {"type": "string", "enum": ["sfo3", "nyc1"], "default": "sfo3",
                 "x-titles": {"sfo3": "San Francisco 3", "nyc1": "New York 1"}},
      "size": {"type": "string", "enum": ["s-1vcpu-1gb", "s-2vcpu-4gb"], "default": "s-2vcpu-4gb",
               "x-titles": {"s-1vcpu-1gb": "1 vCPU · 1 GB · 25 GB disk · $6/month",
                            "s-2vcpu-4gb": "2 vCPUs · 4 GB · 80 GB disk · $24/month"},
               "x-costs": {"s-2vcpu-4gb": "$0.03571/hour ($24/month)"}},
      "image": {"type": "string", "default": "ubuntu-24-04-x64"}}},
     "id": "do", "name": "machine-3", "values": %@}
    """

    @Test func theCardSaysWhereAndWhatItCosts() throws {
        let plan = try #require(OneClickMake.plan(options(form.replacingOccurrences(
            of: "%@", with: #"{"region": "sfo3", "size": "s-2vcpu-4gb", "image": "ubuntu-24-04-x64"}"#))))
        #expect(plan.name == "machine-3")
        #expect(plan.summary == "San Francisco 3 · 2 vCPUs · 4 GB · 80 GB disk · $24/month")
        #expect(plan.values["size"]?.stringValue == "s-2vcpu-4gb")
        #expect(plan.note.hasPrefix("New Droplet makes machine-3 there."))
    }

    @Test func theLastChoiceIsUsedAgain() throws {
        // The control plane gives the options the person made a machine with last (#3979).
        let plan = try #require(OneClickMake.plan(options(form.replacingOccurrences(
            of: "%@", with: #"{"region": "nyc1", "size": "s-1vcpu-1gb"}"#))))
        #expect(plan.summary == "New York 1 · 1 vCPU · 1 GB · 25 GB disk · $6/month")
        // A field the values leave out takes the form's first choice, or its default.
        #expect(plan.values["image"]?.stringValue == "ubuntu-24-04-x64")
    }

    @Test func noValuesTakeTheFirstChoices() throws {
        let plan = try #require(OneClickMake.plan(options(form.replacingOccurrences(of: "%@", with: "null"))))
        #expect(plan.summary == "San Francisco 3 · 1 vCPU · 1 GB · 25 GB disk · $6/month")
    }

    @Test func onlyADigitalOceanProviderThatMakesOffersIt() {
        func provider(_ preset: String?, _ capabilities: [String]) -> DaemonAPI.ProviderSummary {
            .init(id: ProviderID(rawValue: "p"), name: "P", kind: "commands", capabilities: capabilities, preset: preset)
        }
        #expect(OneClickMake.offers(provider("digitalocean-api", ["create", "list"])))
        #expect(!OneClickMake.offers(provider("digitalocean-api", ["list"])))
        #expect(!OneClickMake.offers(provider("codespaces-api", ["create"])))
        #expect(!OneClickMake.offers(provider(nil, ["create"])))
    }
}
