import Foundation

/// The person's set-up scripts (#7167, #7155, epic #7151): `root.sh` and `setup.sh` in
/// `~/.agents/machine-setup/` on the machine that sends them, the sender. Hand-written against
/// `Packages/Wire/schema/machines.schema.json` (`WireSchemaTests`).
public extension DaemonAPI.Method {
    /// The sender's two scripts, with the text approved last (#7167).
    static let setupScripts = "setup/scripts"
    /// A client of the person's lock approves a script's text as it is now (#7167, #7157).
    static let setupApprove = "setup/approve"
    /// Send Set-up (#7155): the sender sends `setup.sh` to one machine, with a client's approval for it.
    static let setupSend = "setup/send"
    /// Run Again, and Try Again of your set-up (#7155): the machine runs the copy it was sent again.
    static let setupRun = "setup/run"
}

public extension DaemonAPI {
    /// One script on the sender, as `setup/scripts` says it.
    struct SetupScript: Codable, Sendable, Hashable, Identifiable {
        /// `root` or `setup`.
        public var name: String
        public var present: Bool
        public var text: String?
        public var hash: String?
        public var size: Int64?
        /// Why a file that is there has no text: over its limit, or unreadable.
        public var problem: String?
        public var approvedText: String?
        public var approvedHash: String?
        public var approvals: [LockRecord]
        /// The file is not the approved text, so it is not used until a client approves it.
        public var changed: Bool
        /// The number to sign the next approval with.
        public var nextSeq: Int64

        public init(name: String, present: Bool, text: String? = nil, hash: String? = nil, size: Int64? = nil,
                    problem: String? = nil, approvedText: String? = nil, approvedHash: String? = nil,
                    approvals: [LockRecord] = [], changed: Bool = false, nextSeq: Int64 = 1) {
            self.name = name
            self.present = present
            self.text = text
            self.hash = hash
            self.size = size
            self.problem = problem
            self.approvedText = approvedText
            self.approvedHash = approvedHash
            self.approvals = approvals
            self.changed = changed
            self.nextSeq = nextSeq
        }

        public var id: String { name }

        /// The file's name: `root.sh` or `setup.sh`.
        public var file: String { name + ".sh" }

        /// Whether a client's approval for `scope` is in force for the text there now (#7157).
        public func approved(for scope: String) -> Bool {
            !changed && approvals.contains { $0.scope == scope }
        }

        /// Whether a make must ask first (#7157): the file is there, can be read, and is not
        /// the text approved for machines as they are made.
        public var needsApprovalForMade: Bool {
            present && text != nil && !approved(for: "made")
        }
    }

    /// `setup/scripts`: root then setup.
    struct SetupScripts: Codable, Sendable, Hashable {
        public var scripts: [SetupScript]
        /// The folder they are in, as an absolute path (#7512); nil with no personal home.
        public var folder: String?

        public init(scripts: [SetupScript], folder: String? = nil) {
            self.scripts = scripts
            self.folder = folder
        }

        public func script(_ name: String) -> SetupScript? { scripts.first { $0.name == name } }
    }

    /// `setup/approve`: an approval of `script` (`root` or `setup`), a `setup.script` record.
    struct SetupApprove: Codable, Sendable, Hashable {
        public var script: String
        public var approval: LockRecord

        public init(script: String, approval: LockRecord) {
            self.script = script
            self.approval = approval
        }
    }

    /// `setup/send`: Send Set-up to the machine `host`, with an approval of `setup` of scope `m:<host>`.
    struct SetupSend: Codable, Sendable, Hashable {
        public var host: String
        public var approval: LockRecord

        public init(host: String, approval: LockRecord) {
            self.host = host
            self.approval = approval
        }
    }
}
