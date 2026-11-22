#if canImport(CryptoKit)
import CryptoKit
#endif
import Foundation

/// An agent's secret becomes the person's (#5308, `specs/5308-agent-secrets-sync`). A machine
/// seals what an agent saved to the person's clients only, and the control plane keeps the
/// envelopes as an offer. The Mac window (and the web page) opens its own copy and sets the
/// secret as Add a Secret does, sealed to every machine and client; a name the person has waits
/// for Keep Yours or Use the Agent's. The Remote shows the rows and does not seal.
/// Hand-written against `Packages/Wire/schema` (`WireSchemaTests`).
public extension DaemonAPI {
    /// Where a person's secret came from: an agent saved it on this machine.
    struct SecretSavedBy: Codable, Sendable, Hashable {
        public var machine: String
        /// The machine's name when it was saved.
        public var name: String
        public var agent: UUID?
        public var at: String

        public init(machine: String, name: String, agent: UUID? = nil, at: String) {
            self.machine = machine
            self.name = name
            self.agent = agent
            self.at = at
        }
    }

    /// One row of `user/secretOffers`: never a value. `mine` is this client's copy; `conflict`
    /// says the person has a secret of that name, so it waits for them.
    struct SecretOffer: Codable, Sendable, Hashable, Identifiable {
        public var offer: String
        public var name: String
        public var machine: String
        public var machineName: String
        public var agent: UUID?
        public var savedAt: String
        public var needsApproval: Bool?
        public var description: String?
        public var conflict: Bool?
        public var mine: Envelope?

        public init(offer: String, name: String, machine: String, machineName: String, agent: UUID? = nil, savedAt: String,
                    needsApproval: Bool? = nil, description: String? = nil, conflict: Bool? = nil, mine: Envelope? = nil) {
            self.offer = offer
            self.name = name
            self.machine = machine
            self.machineName = machineName
            self.agent = agent
            self.savedAt = savedAt
            self.needsApproval = needsApproval
            self.description = description
            self.conflict = conflict
            self.mine = mine
        }

        public var id: String { offer }
        public var waitsForYou: Bool { conflict == true }
        /// The card that shows it (#6626): its machine and name, not its id, so a new offer of
        /// the same name keeps the card's progress and failure.
        public var card: String { "\(machine)|\(name)" }

        /// The offers a person sees, oldest first: for each machine and name, its newest offer
        /// only. The web page's `waitingOffers`.
        public static func waiting(_ offers: [SecretOffer]) -> [SecretOffer] {
            var newest: [String: SecretOffer] = [:]
            for offer in offers where newest[offer.card].map({ $0.savedAt <= offer.savedAt }) ?? true {
                newest[offer.card] = offer
            }
            return newest.values.sorted { ($0.savedAt, $0.card) < ($1.savedAt, $1.card) }
        }
    }

    /// What a client shows while it answers its offers (#6626): the cards being answered, and
    /// why one failed. Keyed by `card`, so a new offer of the same name, read while the answer
    /// is under way, keeps them.
    struct SecretOfferAnswers: Sendable, Hashable {
        public private(set) var answering: Set<String> = []
        public private(set) var failures: [String: String] = [:]

        public init() {}

        public func isAnswering(_ offer: SecretOffer) -> Bool { answering.contains(offer.card) }
        public func failure(_ offer: SecretOffer) -> String? { failures[offer.card] }

        public mutating func begin(_ offer: SecretOffer) {
            answering.insert(offer.card)
            failures[offer.card] = nil
        }

        /// Done: `failure` is why not, or nil.
        public mutating func end(_ offer: SecretOffer, failure: String?) {
            answering.remove(offer.card)
            failures[offer.card] = failure
        }

        /// After a read of the offers: a failure whose card went goes too.
        public mutating func keep(only offers: [SecretOffer]) {
            let cards = Set(offers.map(\.card))
            failures = failures.filter { cards.contains($0.key) }
        }
    }

    /// `user/secretOffers`; `user` is the person, as the associated data names them.
    struct SecretOffers: Codable, Sendable, Hashable {
        public var user: String?
        public var offers: [SecretOffer]
    }

    /// `user/dropSecretOffer`: Keep Yours.
    struct SecretOfferRequest: Codable, Sendable, Hashable {
        public var offer: String

        public init(offer: String) { self.offer = offer }
    }
}

public extension DaemonAPI.Method {
    /// `{}` → `SecretOffers` (#5308).
    static let userSecretOffers = "user/secretOffers"
    /// `SecretOfferRequest` → `{}` (#5308): Keep Yours.
    static let userDropSecretOffer = "user/dropSecretOffer"
}

/// The words of an agent's secret, the web page's `Web/src/model/secretOffers.ts`.
public enum SecretOfferWords {
    public static let keepYours = "Keep Yours"
    public static let useTheAgents = "Use the Agent's"
    /// On the Remote, which does not seal: where the person chooses.
    public static let chooseElsewhere = "Choose Keep Yours or Use the Agent's in Agents on your Mac or in the browser."
    /// Use the Agent's on a Mac window with no key of its own on the control plane (#6626).
    public static let noKey = "This Mac has no key on the control plane to open the agent's copy with. Choose in the browser, "
        + "or sign this Mac in to the control plane again."

    /// Under an offer's name in Needs you: where it came from, and what happens next.
    public static func line(_ offer: DaemonAPI.SecretOffer) -> String {
        offer.waitsForYou
            ? "Saved by an agent on \(offer.machineName). You have a secret with this name: your other machines keep yours until you choose."
            : "Saved by an agent on \(offer.machineName). It goes to your other machines when you next open Agents."
    }

    /// Under a secret of yours that an agent saved: "Saved by an agent on devbox, 2 h ago".
    public static func savedBy(_ by: DaemonAPI.SecretSavedBy?, now: Date = Date()) -> String? {
        guard let by else { return nil }
        guard let at = ISO8601DateFormatter().date(from: by.at) else { return "Saved by an agent on \(by.name)" }
        return "Saved by an agent on \(by.name), \(SecretInfoWords.age(at, now: now))"
    }
}

#if canImport(CryptoKit)
public extension ClientSealing {
    /// HPKE's info an agent's secret is sealed under, by its machine, to the person's clients.
    static let agentSecretPurpose = "com.alexecollins.agents.agentsecret.v1"

    /// Go's `controllink.AgentSecretAD`, byte for byte.
    static func agentSecretAD(user: String, machine: String, recipient: String, name: String, offer: String, ask: Bool = false) -> Data {
        Data("agentsecret.v1|\(user)|\(machine)|\(recipient)|\(name)|\(offer)\(ask ? "|ask" : "")".utf8)
    }

    /// This client's copy of an offer, opened with its own key: in memory only.
    static func open(_ offer: DaemonAPI.SecretOffer, user: String, with key: DeviceKey, client: UUID) throws -> Data {
        guard let mine = offer.mine else { throw CocoaError(.coderValueNotFound) }
        return try key.open(mine, info: agentSecretPurpose,
                            authenticating: agentSecretAD(user: user, machine: offer.machine, recipient: clientRecipient(client),
                                                          name: offer.name, offer: offer.offer, ask: offer.needsApproval == true))
    }

    /// Makes an offer one of the person's secrets: opened here, sealed to every machine the
    /// lock names (`signed`) and every client of the person's, with the same Ask before use and
    /// description. `kept` is the person's version of the name, for Use the Agent's; nil for a
    /// name they have none of.
    static func take(_ offer: DaemonAPI.SecretOffer, user: String, kept: Int?, on control: DaemonClient, key: DeviceKey, client: UUID,
                     isolation: isolated (any Actor)? = #isolation,
                     signed: ([DaemonAPI.SealRecipient]) async -> [DaemonAPI.SealRecipient]) async throws {
        let plain = try open(offer, user: user, with: key, client: client)
        let value = String(decoding: plain, as: UTF8.self)
        let version = kept.map { $0 + 1 } ?? Int(Date().timeIntervalSince1970 * 1000)
        let recipients = try await control.call(DaemonAPI.Method.userSecretRecipients, DaemonAPI.Empty(),
                                                returning: DaemonAPI.SealRecipients.self)
        let ask = offer.needsApproval == true
        let values = try sealSecret(user: recipients.user ?? user, name: offer.name, version: version, value: value, ask: ask,
                                    to: await signed(recipients.recipients))
        let set = DaemonAPI.UserSecretSet(name: offer.name, version: version, values: values, needsApproval: ask ? true : nil,
                                          description: offer.description, offer: offer.offer)
        _ = try await control.call(DaemonAPI.Method.userSetSecret, set)
    }

    /// `user/secretOffers`, and each offer with no conflict taken at once when `take` is true
    /// (the Mac window; the Remote does not seal). The offers that still wait come back. Quiet
    /// when the control plane is older than #5308.
    static func takeOffers(on control: DaemonClient, key: DeviceKey?, client: UUID?, take: Bool,
                           isolation: isolated (any Actor)? = #isolation,
                           signed: ([DaemonAPI.SealRecipient]) async -> [DaemonAPI.SealRecipient]) async -> DaemonAPI.SecretOffers? {
        guard let listed = try? await control.call(DaemonAPI.Method.userSecretOffers, DaemonAPI.Empty(),
                                                   returning: DaemonAPI.SecretOffers.self) else { return nil }
        guard take, let key, let client, let user = listed.user else { return listed }
        var took = false
        for offer in listed.offers where !offer.waitsForYou && offer.mine != nil {
            if (try? await self.take(offer, user: user, kept: nil, on: control, key: key, client: client, signed: signed)) != nil {
                took = true
            }
        }
        guard took else { return listed }
        return (try? await control.call(DaemonAPI.Method.userSecretOffers, DaemonAPI.Empty(),
                                        returning: DaemonAPI.SecretOffers.self)) ?? listed
    }
}
#endif
