#if canImport(CryptoKit)
import CryptoKit
#endif
import Foundation

/// Signed records (#7167): a value that a client of the lock signed for one or more of the
/// person's machines, such as the approval of a set-up script. The types are hand-written
/// against `Packages/Wire/schema` (`WireSchemaTests`).
public extension DaemonAPI {
    /// A record as its signer wrote it. Empty content says that the value is removed.
    struct LockRecord: Codable, Sendable, Hashable {
        public var v: Int
        public var person: String
        public var root: Data
        public var kind: String
        public var subject: String
        public var scope: String
        public var seq: Int64
        public var content: Data
        public var key: Data
        public var sig: Data

        public init(v: Int, person: String, root: Data, kind: String, subject: String, scope: String, seq: Int64,
                    content: Data, key: Data, sig: Data) {
            self.v = v
            self.person = person
            self.root = root
            self.kind = kind
            self.subject = subject
            self.scope = scope
            self.seq = seq
            self.content = content
            self.key = key
            self.sig = sig
        }
    }

    /// A `setup.script` record's content: the script's SHA-256 in hexadecimal, and its length in bytes.
    struct SetupScriptApproval: Codable, Sendable, Hashable {
        public var hash: String
        public var size: Int64

        public init(hash: String, size: Int64) {
            self.hash = hash
            self.size = size
        }
    }
}

#if canImport(CryptoKit)
/// A record's signature and its checks, the same as Go's `machinelock/record.go`.
public enum LockRecords {
    public static let version = 1
    public static let mostContent = 256 << 10
    /// The highest number, the largest whole number every client reads exactly.
    public static let mostSeq: Int64 = (1 << 53) - 1
    /// The highest number of a first record, and how far above the last number the next may be.
    public static let mostFirstSeq: Int64 = 1 << 40
    public static let mostSeqStep: Int64 = 1 << 20
    static let domain = "agents-lock-record/1"

    public enum Failure: String, Error, Sendable, Hashable {
        /// A part that no record has.
        case invalid
        /// Of another person, or of a lock that was replaced.
        case otherLock
        /// Not signed by a client that the lock names now.
        case notMember
        /// The signature does not check.
        case forged
        /// For another machine.
        case otherPlace
        /// Its number is lower than the one last taken.
        case older
        /// Its number is too far above the one last taken.
        case far
    }

    /// The kinds, with the scopes each may have: `made`, and `m:` for one machine. The signed
    /// bytes name both as text, so one can be added with no change to them.
    static let kinds: [String: [String]] = [
        "setup.script": ["made", "m:"],
    ]

    /// A subject or a machine's id. By bytes, as two texts that Swift calls equal can differ.
    static func isName(_ text: String) -> Bool {
        let bytes = Array(text.utf8)
        guard let first = bytes.first, bytes.count <= 128, first != UInt8(ascii: "."), first != UInt8(ascii: "-") else { return false }
        return bytes.allSatisfy { b in
            switch b {
            case UInt8(ascii: "A")...UInt8(ascii: "Z"), UInt8(ascii: "a")...UInt8(ascii: "z"), UInt8(ascii: "0")...UInt8(ascii: "9"),
                 UInt8(ascii: "."), UInt8(ascii: "_"), UInt8(ascii: "-"): true
            default: false
            }
        }
    }

    static func isNamed(kind: String, subject: String, scope: String) -> Bool {
        guard kind.utf8.allSatisfy({ $0 < 0x80 }), scope.utf8.allSatisfy({ $0 < 0x80 }),
              let scopes = kinds[kind], isName(subject) else { return false }
        if scope.hasPrefix("m:") { return scopes.contains("m:") && isName(String(scope.dropFirst(2))) }
        return scopes.contains(scope)
    }

    static func hex(_ bytes: some Sequence<UInt8>) -> String {
        bytes.map { String(format: "%02x", $0) }.joined()
    }

    /// What a record's signature covers: the domain and each part, ended by a newline.
    public static func signedData(_ r: DaemonAPI.LockRecord) -> Data {
        let parts = [domain, r.person, hex(r.root), r.kind, r.subject, r.scope, String(r.seq), hex(SHA256.hash(data: r.content)), ""]
        return Data(parts.joined(separator: "\n").utf8)
    }

    /// Signs content as a record of the lock, with a client's key.
    public static func sign(_ key: LockSigningKey, state: MachineLock.State, kind: String, subject: String, scope: String,
                            seq: Int64, content: Data) throws -> DaemonAPI.LockRecord {
        var r = DaemonAPI.LockRecord(v: version, person: state.person, root: state.root, kind: kind, subject: subject, scope: scope,
                                     seq: seq, content: content, key: key.publicKey, sig: Data())
        try checkShape(r)
        r.sig = try key.sign(signedData(r))
        return r
    }

    static func checkShape(_ r: DaemonAPI.LockRecord) throws(Failure) {
        guard r.v == version, isNamed(kind: r.kind, subject: r.subject, scope: r.scope),
              r.person.utf8.allSatisfy({ $0 >= 0x20 && $0 != 0x7f }),
              (1...mostSeq).contains(r.seq), r.content.count <= mostContent else { throw .invalid }
    }

    /// Checks that a client the lock names now signed the record, for this person and this
    /// lock. It does not say which machine may take it, nor whether a newer one is there.
    @discardableResult
    public static func check(_ r: DaemonAPI.LockRecord, state: MachineLock.State) throws(Failure) -> MachineLock.Member {
        try checkShape(r)
        guard Array(r.person.utf8) == Array(state.person.utf8), r.root == state.root else { throw .otherLock }
        guard let member = state.members[r.key], member.isClient else { throw .notMember }
        guard LockChannel.verify(r.key, signedData(r), r.sig) else { throw .forged }
        return member
    }

    /// Whether a number may follow `had`, the last one; 0 for none.
    public static func checkSeq(had: Int64, seq: Int64) throws(Failure) {
        if had == 0 ? seq > mostFirstSeq : seq - had > mostSeqStep { throw .far }
        if seq < had { throw .older }
    }

    /// Whether the machine `host` takes the record; `made`, that it was just made.
    public static func takes(_ r: DaemonAPI.LockRecord, host: String, made: Bool = false) -> Bool {
        if r.scope == "made" { return made }
        return !host.isEmpty && Array(r.scope.utf8) == Array("m:\(host)".utf8)
    }
}
#endif
