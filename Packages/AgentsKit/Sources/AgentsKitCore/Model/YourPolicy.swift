import Foundation

/// Your layer (#2957): the person's own rules, kept on the Mac's host (the keeper), signed with
/// the person key, and sent to each of the person's other machines (FR-009 to FR-013). The
/// types are hand-written against `Packages/Wire/schema` (`WireSchemaTests`).
public extension DaemonAPI {
    /// A policy's signature (#998, FR-004; #2957 reuses it for the person key): ECDSA P-256
    /// with SHA-256 (`es256`), over the policy's canonical JSON. `sig` is the 64 bytes r then s.
    struct PolicySignature: Codable, Sendable, Hashable {
        public var version: Int
        public var keyID: String
        public var alg: String
        public var sig: Data
    }

    /// Which client saved your layer.
    struct YourPolicyChangedFrom: Codable, Sendable, Hashable {
        public var client: String
        /// `mac`, `remote` or `web`.
        public var kind: String
    }

    /// Your layer (#2957, data-model.md "YourPolicy"): the rules, a revision, when it was
    /// changed and from which client, and a signature by the person key. No agent, MCP tool or
    /// workflow can change it (FR-010).
    struct YourPolicy: Codable, Sendable, Hashable {
        public var rules: PolicyRules
        /// One higher at each save. A machine refuses a lower one (FR-011).
        public var revision: Int
        public var changed: Date
        public var changedFrom: YourPolicyChangedFrom?
        /// The person's id (the user scope's user, or `local` for a person with no control plane).
        public var person: String
        /// The host id of the keeper (research R4).
        public var keeper: String
    }

    /// Which machine signs your layer (#2957, research R4): the Mac's host, or the first
    /// machine of yours that saved one, when you have no Mac.
    struct PolicyKeeper: Codable, Sendable, Hashable {
        public var host: String
        public var name: String
    }

    /// `policy/yours` (#2957): your layer, which machine is the keeper, and the person key this
    /// machine has pinned.
    struct YourPolicyState: Codable, Sendable, Hashable {
        public var policy: YourPolicy
        public var keeper: PolicyKeeper
        public var pinnedKeyID: String?
        /// The signed copy, when this machine keeps one: the Mac window passes it to a server
        /// over its direct link with `policy/applyYours` (research R3, path 2).
        public var signed: SignedYourPolicy?
        /// Your rules from a copy of your layer this machine cannot trust (#3230): the newest such
        /// copy. Its rules do not apply until the person signs them again here.
        public var untrusted: UntrustedYourPolicy?
    }

    /// One rule a layer sets, in the Policy pane's words (#3230).
    struct PolicyRuleLine: Codable, Sendable, Hashable {
        /// `permissionMode`, `budgets.daily`, and so on.
        public var rule: String
        public var title: String
        public var words: String
    }

    /// A copy of your layer on disk that fails the signature check (#3230): signed with a person
    /// key this machine does not have, such as on another Mac before the root moved here. Shown,
    /// never applied, until the person signs it again here (`policy/settleUntrustedYours`).
    struct UntrustedYourPolicy: Codable, Sendable, Hashable {
        /// The kept file's name: `policy-yours.json`, or `policy-yours.json.untrusted-<time>`.
        public var file: String
        public var rules: PolicyRules
        public var revision: Int
        public var changed: Date
        /// The host id of the machine that signed it.
        public var keeper: String
        public var keyID: String
        /// Each rule the copy sets, in the Policy pane's words and order.
        public var lines: [PolicyRuleLine]
    }

    /// `policy/settleUntrustedYours` (#3230): what the person chose for the untrusted copy in
    /// `file`, read at `revision` of your layer. `sign` signs its rules with this machine's person
    /// key as the next revision; false sets the copy aside and changes nothing.
    struct SettleUntrustedYourPolicyRequest: Codable, Sendable, Hashable {
        public var file: String
        public var revision: Int
        public var sign: Bool

        public init(file: String, revision: Int, sign: Bool) {
            self.file = file
            self.revision = revision
            self.sign = sign
        }
    }

    /// `policy/setYours` (#2957): the rules to save as your layer, at the revision this client
    /// last read. Refused `conflict` when `revision` is not the current one, `notKeeper` on a
    /// machine that is not the keeper, `forbidden` from the agent socket role, an MCP tool or a
    /// workflow (FR-010), and `invalid` when the rules fail `policylayers.Validate`.
    struct SetYourPolicyRequest: Codable, Sendable, Hashable {
        public var rules: PolicyRules
        public var revision: Int

        public init(rules: PolicyRules, revision: Int) {
            self.rules = rules
            self.revision = revision
        }
    }

    /// The person key moving to a new pair (#2957, data-model.md "PersonKeyRotation"): the new
    /// public key, signed by the one it replaces. A machine moves its pin only with one that
    /// checks against the key it has pinned.
    struct PersonKeyRotation: Codable, Sendable, Hashable {
        public var newKey: Data
        public var newKeyID: String
        public var signature: PolicySignature
    }

    /// Your layer, signed by the person key (#2957, data-model.md "SignedYourPolicy"): what the
    /// control plane stores and forwards, and what a non-keeper machine verifies before it
    /// applies a copy.
    struct SignedYourPolicy: Codable, Sendable, Hashable {
        /// The exact CanonicalJSON bytes of YourPolicy, base64.
        public var policy: String
        public var signature: PolicySignature
        /// Present only on the first copy after a new person key.
        public var rotation: PersonKeyRotation?
        /// The person key's public half (X9.63), for a machine with no pin yet to check the
        /// signature and pin it (trust on first use, research R3).
        public var key: Data?
    }
}
