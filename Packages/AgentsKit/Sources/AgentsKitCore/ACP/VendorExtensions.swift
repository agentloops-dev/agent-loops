import Foundation

// MARK: Who is signed in

/// Which kind of account a runtime says it is using, from `_auth/status_update`.
///
/// Only what the runtime menu shows: the kind and the runtime's own label for it
/// ("Claude Max", "Anthropic API key"). The payload also carries the account's email
/// and organisation, which are never read, so they are never stored or relayed.
public struct AuthStatus: Codable, Hashable, Sendable {
    /// `account`, `api_key`, `gateway`, `external` or `none`. Kept as the runtime's
    /// own word, so a kind invented later still decodes on an older phone.
    public var kind: String
    public var label: String

    /// The runtime says, in so many words, that nobody is signed in.
    public var isSignedOut: Bool { kind == "none" }
}
