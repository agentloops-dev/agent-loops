import Foundation

/// A runtime whose sign-in is one file of providers, which a server run borrows from the Mac
/// in one variable (049 D7: OpenCode's `auth.json`, as `OPENCODE_AUTH_CONTENT`).
///
/// Only entries that never rotate are lent. A browser sign-in (OpenCode's `oauth`) refreshes
/// its token as it is used, and a server refreshing the Mac's would leave the Mac's copy
/// stale, as 047 found for Codex. Those stay on the Mac and are named, never sent.
public struct LentFileSignIn: Hashable, Sendable {
    /// Where the file is: `$<dataHomeVariable>/<dataHomePath>`, or else
    /// `$HOME/<defaultPath>`.
    public var dataHomeVariable: String
    public var dataHomePath: String
    public var defaultPath: String
    /// The entry `type`s that are lent.
    public var lendableTypes: Set<String>

    public init(dataHomeVariable: String, dataHomePath: String, defaultPath: String,
                lendableTypes: Set<String>) {
        self.dataHomeVariable = dataHomeVariable
        self.dataHomePath = dataHomePath
        self.defaultPath = defaultPath
        self.lendableTypes = lendableTypes
    }
}
