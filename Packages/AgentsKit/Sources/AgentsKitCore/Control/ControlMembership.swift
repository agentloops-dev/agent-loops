import Foundation

/// What a client or a host keeps once it has paired or enrolled: who it is, the control
/// plane's key, and where to find it. A JSON file beside its own key file.
///
/// The same file on a Mac and on Linux (058, T044). Nothing in it needs CryptoKit.
public struct ControlMembership: Codable, Sendable, Hashable {
    public var client: UUID?
    public var host: MachineRef?
    public var controlKey: Data
    public var addresses: [String]
    public var name: String
    /// The control plane's one address, and its certificate's pin: a membership made from
    /// a version 2 code, which is dialled over a WebSocket (058 re-plan).
    public var url: String?
    public var pin: String?
    /// Every place the control plane answers, in order, as it last said in `ok`, and the
    /// `epoch` of that list (R16). Absent until a control plane announces one; `url` and
    /// `pin` are then its first entry.
    public var endpoints: [ControlEndpoint]?
    public var epoch: Int?

    public init(client: UUID? = nil, host: MachineRef? = nil, controlKey: Data, addresses: [String], name: String,
                url: String? = nil, pin: String? = nil, endpoints: [ControlEndpoint]? = nil, epoch: Int? = nil) {
        self.client = client
        self.host = host
        self.controlKey = controlKey
        self.addresses = addresses
        self.name = name
        self.url = url
        self.pin = pin
        self.endpoints = endpoints
        self.epoch = epoch
    }

    /// The largest membership `save` keeps (#7351): the control plane's key, its name and the
    /// endpoints it gave, which its settings bound at 64 KiB. Go's `controllink.MembershipMostBytes`.
    public static let mostBytes = 256 << 10
    /// The largest file `read` reads, and `write` puts back: four times `mostBytes`, so a
    /// pairing on disk is always read and put back whole, and a new one over `mostBytes` is refused.
    public static let mostReadBytes = 4 * mostBytes

    /// A membership refused for its size, with its words. The one on disk stays as it was.
    public struct TooLarge: LocalizedError, Sendable {
        public let bytes: Int
        public let most: Int
        public var errorDescription: String? {
            "This pairing is \(bytes) bytes, more than the \(most) this device keeps, so it is not kept; the one before stays."
        }
    }

    public static func load(_ file: URL) -> ControlMembership? {
        guard let data = read(file) else { return nil }
        return try? JSONDecoder().decode(ControlMembership.self, from: data)
    }

    /// A membership file's bytes, or nil when it is missing or larger than `mostReadBytes`.
    /// A file that is too large is left as it is.
    public static func read(_ file: URL) -> Data? {
        guard let handle = try? FileHandle(forReadingFrom: file) else { return nil }
        defer { try? handle.close() }
        guard let data = try? handle.read(upToCount: mostReadBytes + 1), data.count <= mostReadBytes else { return nil }
        return data
    }

    /// `backedUp: false` keeps the file out of iCloud and Finder backups (#1031): a client's
    /// pairing is this device's, and a restored device pairs again rather than carry it.
    /// A membership over `mostBytes` is refused with `TooLarge`.
    public func save(_ file: URL, backedUp: Bool = true) throws {
        let data = try JSONEncoder().encode(self)
        guard data.count <= Self.mostBytes else { throw TooLarge(bytes: data.count, most: Self.mostBytes) }
        try Self.write(data, to: file, backedUp: backedUp)
    }

    /// A membership's bytes, put in place as `save` puts them: 0600, and marked as not
    /// backed up when asked. Every write of a pairing comes here, so none loses its mark (#6800).
    /// Bytes over `mostReadBytes`, more than `read` gives, are refused with `TooLarge`.
    public static func write(_ data: Data, to file: URL, backedUp: Bool = true) throws {
        guard data.count <= mostReadBytes else { throw TooLarge(bytes: data.count, most: mostReadBytes) }
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        // store-bounds: kind=key-value; count=1; size=256KiB; age=until-deleted; applied=on save (refused, and the one before stays), a new pairing replaces it, and Forget or an unpair deletes it
        try data.write(to: file, options: .atomic)
        chmod(file.path, 0o600)
        #if canImport(Darwin)
        if !backedUp {
            // Set after each write: an atomic write puts a new file in place, without the old one's mark.
            var values = URLResourceValues()
            values.isExcludedFromBackup = true
            var marked = file
            try marked.setResourceValues(values)
        }
        #endif
    }
}
